package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/matthosch/avalanche/internal/events"
	"github.com/matthosch/avalanche/internal/ledger"
)

// pool is one throwaway ledger Postgres shared by every test in the package (seam 3 in spec
// #1: ledger ingest against real Postgres). Tests stay independent because each uses fresh
// Store IDs; Fee revenue is Platform-wide, so tests read it before and after.
var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runWithPostgres(m))
}

func runWithPostgres(m *testing.M) int {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("ledger"), tcpostgres.WithUsername("avalanche"), tcpostgres.WithPassword("avalanche"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres:", err)
		return 1
	}
	defer func() { _ = container.Terminate(context.Background()) }()
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "postgres url:", err)
		return 1
	}
	pool, err = pgxpool.New(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := ledger.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "migrate ledger:", err)
		return 1
	}
	return m.Run()
}

// orderCompleted is a fresh OrderCompleted for a new Store, of amountCents at feeBPS.
func orderCompleted(amountCents int64, feeBPS int) events.OrderCompleted {
	return events.OrderCompleted{
		SchemaVersion: events.OrderCompletedSchemaVersion,
		EventID:       uuid.Must(uuid.NewV7()),
		OrderID:       uuid.Must(uuid.NewV7()),
		HoldID:        uuid.Must(uuid.NewV7()),
		StoreID:       uuid.Must(uuid.NewV7()),
		FlashSaleID:   uuid.Must(uuid.NewV7()),
		AmountCents:   amountCents,
		Currency:      "USD",
		FeeBPS:        feeBPS,
		OccurredAt:    time.Now().UTC(),
	}
}

func storeBalance(t *testing.T, storeID uuid.UUID) int64 {
	t.Helper()
	balance, err := ledger.New(pool).StoreBalance(context.Background(), storeID)
	if err != nil {
		t.Fatalf("store balance: %v", err)
	}
	return balance
}

func feeRevenue(t *testing.T) int64 {
	t.Helper()
	revenue, err := ledger.New(pool).FeeRevenue(context.Background())
	if err != nil {
		t.Fatalf("fee revenue: %v", err)
	}
	return revenue
}

// The ADR-0004 example: a $100 Order at a 5% Fee pays the Store 9500 and the Platform 500.
func TestOrderPaysTheStoreAndThePlatformItsFee(t *testing.T) {
	ctx := context.Background()
	l := ledger.New(pool)
	event := orderCompleted(10000, 500)
	revenueBefore := feeRevenue(t)

	if err := l.Ingest(ctx, event); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if got := storeBalance(t, event.StoreID); got != 9500 {
		t.Errorf("store balance = %d, want 9500", got)
	}
	if got := feeRevenue(t) - revenueBefore; got != 500 {
		t.Errorf("fee revenue grew by %d, want 500", got)
	}
}

// Delivery is at-least-once, so the same event can arrive twice, even at once.
func TestARedeliveredEventPostsOnce(t *testing.T) {
	ctx := context.Background()
	l := ledger.New(pool)
	event := orderCompleted(10000, 500)
	revenueBefore := feeRevenue(t)

	errs := make(chan error, 3)
	for range 3 {
		go func() { errs <- l.Ingest(ctx, event) }()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	if err := l.Ingest(ctx, event); err != nil {
		t.Fatalf("ingest again: %v", err)
	}

	if got := storeBalance(t, event.StoreID); got != 9500 {
		t.Errorf("store balance = %d, want 9500 (one Journal Entry)", got)
	}
	if got := feeRevenue(t) - revenueBefore; got != 500 {
		t.Errorf("fee revenue grew by %d, want 500 (one Journal Entry)", got)
	}
}

// The database, not the Go code, keeps the books balanced: a Journal Entry whose Postings
// don't sum to zero is refused, and none of it is kept.
func TestTheDatabaseRefusesAnUnbalancedJournalEntry(t *testing.T) {
	ctx := context.Background()
	l := ledger.New(pool)
	storeID := uuid.Must(uuid.NewV7())
	entry := ledger.JournalEntry{
		EventID:  uuid.Must(uuid.NewV7()),
		Currency: "USD",
		Postings: []ledger.Posting{
			{LedgerAccount: ledger.ProcessorReceivable, AmountCents: 10000},
			{LedgerAccount: ledger.StorePayable(storeID), AmountCents: -9999},
		},
	}

	if err := l.Post(ctx, entry); !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("post unbalanced: err = %v, want ErrUnbalanced", err)
	}
	if got := storeBalance(t, storeID); got != 0 {
		t.Errorf("store balance = %d after a refused Journal Entry, want 0", got)
	}

	// The refusal kept nothing, so the event can still be booked, balanced.
	entry.Postings = append(entry.Postings, ledger.Posting{LedgerAccount: ledger.PlatformFeeRevenue, AmountCents: -1})
	if err := l.Post(ctx, entry); err != nil {
		t.Fatalf("post balanced: %v", err)
	}
	if got := storeBalance(t, storeID); got != 9999 {
		t.Errorf("store balance = %d, want 9999", got)
	}
}

// The Fee is rounded half up to a whole cent and the Store gets the remainder, so every cent
// of the Order lands somewhere.
func TestFeeRoundingGivesTheStoreTheRemainder(t *testing.T) {
	cases := []struct {
		amountCents int64
		feeBPS      int
		wantFee     int64
		wantStore   int64
	}{
		{0, 500, 0, 0},
		{1, 500, 0, 1},            // Fee 0.05¢ rounds down
		{9, 500, 0, 9},            // 0.45¢ rounds down
		{10, 500, 1, 9},           // 0.5¢ rounds up
		{30, 500, 2, 28},          // 1.5¢ rounds up
		{12900, 500, 645, 12255},  // the seeded Aurora Trail Runner
		{19999, 500, 1000, 18999}, // 999.95¢ rounds up
		{12345, 333, 411, 11934},  // 411.0885¢ rounds down
		{10000, 0, 0, 10000},      // no Fee
		{10000, 10000, 10000, 0},  // the whole Order
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d at %d bps", c.amountCents, c.feeBPS), func(t *testing.T) {
			event := orderCompleted(c.amountCents, c.feeBPS)
			revenueBefore := feeRevenue(t)

			if err := ledger.New(pool).Ingest(context.Background(), event); err != nil {
				t.Fatalf("ingest: %v", err)
			}

			if got := storeBalance(t, event.StoreID); got != c.wantStore {
				t.Errorf("store balance = %d, want %d", got, c.wantStore)
			}
			if got := feeRevenue(t) - revenueBefore; got != c.wantFee {
				t.Errorf("fee revenue grew by %d, want %d", got, c.wantFee)
			}
		})
	}
}

// There is no stored running balance: a Store's balance and the Platform's Fee revenue are
// whatever their Postings add up to, across every Journal Entry that touches them.
func TestBalancesAreTheSumOfTheirPostings(t *testing.T) {
	ctx := context.Background()
	l := ledger.New(pool)
	storeID := uuid.Must(uuid.NewV7())
	revenueBefore := feeRevenue(t)

	first, second := orderCompleted(12900, 500), orderCompleted(4500, 500)
	first.StoreID, second.StoreID = storeID, storeID
	other := orderCompleted(19900, 500) // another Store's Order counts toward Fee revenue only
	for _, event := range []events.OrderCompleted{first, second, other} {
		if err := l.Ingest(ctx, event); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	// A Journal Entry that moves money back out of the Store's payable account.
	if err := l.Post(ctx, ledger.JournalEntry{
		EventID:  uuid.Must(uuid.NewV7()),
		Currency: "USD",
		Postings: []ledger.Posting{
			{LedgerAccount: ledger.StorePayable(storeID), AmountCents: 1000},
			{LedgerAccount: ledger.ProcessorReceivable, AmountCents: -1000},
		},
	}); err != nil {
		t.Fatalf("post: %v", err)
	}

	// 12255 + 4275 − 1000; Fees 645 + 225 + 995.
	if got := storeBalance(t, storeID); got != 15530 {
		t.Errorf("store balance = %d, want 15530", got)
	}
	if got := feeRevenue(t) - revenueBefore; got != 1865 {
		t.Errorf("fee revenue grew by %d, want 1865", got)
	}
}
