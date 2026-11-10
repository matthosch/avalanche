// Package ledger owns the ledger Postgres database: a double-entry, append-only record of
// money moving between Ledger Accounts, in integer cents (ADR-0004).
package ledger

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/events"
	"github.com/matthosch/avalanche/internal/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate brings the ledger schema up to date. It is safe to call from several processes at
// once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return migrate.Up(ctx, pool, "ledger", migrate.Sub(migrations, "migrations"))
}

// The Ledger Accounts an Order moves money between.
const (
	ProcessorReceivable = "processor_receivable"
	PlatformFeeRevenue  = "platform_fee_revenue"
)

// StorePayable is the Ledger Account of what the Platform owes a Store.
func StorePayable(storeID uuid.UUID) string { return "store_payable:" + storeID.String() }

// Posting is a single debit or credit to one Ledger Account. A debit is positive and a credit
// negative, so a balanced Journal Entry's Postings sum to zero.
type Posting struct {
	LedgerAccount string
	AmountCents   int64
}

// JournalEntry is one balanced, immutable record of money moving between Ledger Accounts,
// booked for the event EventID.
type JournalEntry struct {
	EventID  uuid.UUID
	Currency string
	Postings []Posting
}

// ErrUnbalanced means the database refused a Journal Entry whose Postings don't sum to zero.
var ErrUnbalanced = errors.New("ledger: journal entry postings don't sum to zero")

// Ledger posts Journal Entries and reads balances from their Postings.
type Ledger struct {
	db *pgxpool.Pool
}

// New returns a Ledger stored in pool.
func New(pool *pgxpool.Pool) *Ledger {
	return &Ledger{db: pool}
}

// Ingest books an OrderCompleted as its Journal Entry: Dr processor_receivable for the whole
// amount, Cr the Store's payable and Cr platform_fee_revenue with the Fee. Delivering the
// same event again posts nothing.
func (l *Ledger) Ingest(ctx context.Context, event events.OrderCompleted) error {
	if event.SchemaVersion != events.OrderCompletedSchemaVersion {
		return fmt.Errorf("ledger: OrderCompleted %s has schema_version %d, want %d", event.EventID, event.SchemaVersion, events.OrderCompletedSchemaVersion)
	}
	fee := Fee(event.AmountCents, event.FeeBPS)
	return l.Post(ctx, JournalEntry{
		EventID:  event.EventID,
		Currency: event.Currency,
		Postings: []Posting{
			{ProcessorReceivable, event.AmountCents},
			{StorePayable(event.StoreID), -(event.AmountCents - fee)},
			{PlatformFeeRevenue, -fee},
		},
	})
}

// Post writes a Journal Entry and its Postings in one transaction. The database checks that
// they sum to zero and refuses them otherwise, with ErrUnbalanced. A Journal Entry is booked
// once per event: posting one for an EventID that already has one writes nothing.
func (l *Ledger) Post(ctx context.Context, entry JournalEntry) error {
	if len(entry.Postings) == 0 {
		// The database checks the sum as Postings arrive, so a Journal Entry with none would
		// slip past it and use up the event.
		return fmt.Errorf("ledger: journal entry for event %s has no postings", entry.EventID)
	}
	err := pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
		var journalEntryID uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO journal_entries (event_id) VALUES ($1)
			ON CONFLICT (event_id) DO NOTHING
			RETURNING id`, entry.EventID).Scan(&journalEntryID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already booked
		}
		if err != nil {
			return err
		}
		for _, posting := range entry.Postings {
			if _, err := tx.Exec(ctx, `
				INSERT INTO postings (journal_entry_id, ledger_account, amount_cents, currency)
				VALUES ($1, $2, $3, $4)`, journalEntryID, posting.LedgerAccount, posting.AmountCents, entry.Currency); err != nil {
				return err
			}
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "postings_balance" {
		return fmt.Errorf("%w: event %s", ErrUnbalanced, entry.EventID)
	}
	if err != nil {
		return fmt.Errorf("ledger: post journal entry for event %s: %w", entry.EventID, err)
	}
	return nil
}

// Fee is the Platform's cut of amountCents at feeBPS basis points, rounded half up to a whole
// cent. The Store gets the remainder, so the Postings always balance exactly (ADR-0004).
func Fee(amountCents int64, feeBPS int) int64 {
	return (amountCents*int64(feeBPS) + 5000) / 10000
}

// StoreBalance is what the Platform owes a Store: the credits to its payable account less any
// debits, summed from Postings.
func (l *Ledger) StoreBalance(ctx context.Context, storeID uuid.UUID) (int64, error) {
	return l.creditBalance(ctx, StorePayable(storeID))
}

// FeeRevenue is the Platform's Fee revenue across every Order, summed from Postings.
func (l *Ledger) FeeRevenue(ctx context.Context) (int64, error) {
	return l.creditBalance(ctx, PlatformFeeRevenue)
}

// creditBalance is a credit-normal Ledger Account's balance: credits are negative Postings, so
// it is the negated sum.
func (l *Ledger) creditBalance(ctx context.Context, ledgerAccount string) (int64, error) {
	var sum int64
	if err := l.db.QueryRow(ctx, `SELECT coalesce(sum(amount_cents), 0) FROM postings WHERE ledger_account = $1`, ledgerAccount).Scan(&sum); err != nil {
		return 0, fmt.Errorf("ledger: balance of %s: %w", ledgerAccount, err)
	}
	return -sum, nil
}
