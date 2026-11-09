// Package commerce owns the commerce Postgres database: Stores, Flash Sales, Orders and the
// outbox.
package commerce

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/matthosch/avalanche/internal/events"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate brings the commerce schema up to date. It is safe to call from several processes at
// once (api and worker both migrate at startup): a Postgres advisory lock lets one apply the
// migrations while the others wait, then find nothing left to do.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	// Poll for the lock every second, for up to a minute, rather than goose's 5 s default, so a
	// process that loses the race starts about as soon as the winner has finished.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 60))
	if err != nil {
		return fmt.Errorf("commerce migrations lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationsDir(), goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("commerce migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("commerce migrate up: %w", err)
	}
	return nil
}

// Status is where a Flash Sale is in its lifecycle: SCHEDULED → LIVE → ENDED → CLOSED.
type Status string

const (
	Scheduled Status = "SCHEDULED"
	Live      Status = "LIVE"
	Ended     Status = "ENDED"
	Closed    Status = "CLOSED"
)

// FlashSale is a Flash Sale as listed to Shoppers.
type FlashSale struct {
	ID          uuid.UUID
	StoreID     uuid.UUID
	StoreName   string
	ProductName string
	PriceCents  int64
	Currency    string
	Units       int32
	StartsAt    time.Time
	EndsAt      time.Time
	Status      Status
}

// flashSaleColumns are the columns scanFlashSale reads, from flash_sales fs joined to stores s.
const flashSaleColumns = `fs.id, s.id, s.name, fs.product_name, fs.price_cents, fs.currency, fs.units, fs.starts_at, fs.ends_at, fs.status`

func scanFlashSale(r pgx.CollectableRow) (FlashSale, error) {
	var flashSale FlashSale
	err := r.Scan(&flashSale.ID, &flashSale.StoreID, &flashSale.StoreName, &flashSale.ProductName,
		&flashSale.PriceCents, &flashSale.Currency, &flashSale.Units, &flashSale.StartsAt, &flashSale.EndsAt, &flashSale.Status)
	return flashSale, err
}

// ListFlashSales returns every Flash Sale, soonest start first.
func ListFlashSales(ctx context.Context, q Querier) ([]FlashSale, error) {
	rows, err := q.Query(ctx, `
		SELECT `+flashSaleColumns+`
		FROM flash_sales fs JOIN stores s ON s.id = fs.store_id
		ORDER BY fs.starts_at, fs.id`)
	if err != nil {
		return nil, fmt.Errorf("list flash sales: %w", err)
	}
	return pgx.CollectRows(rows, scanFlashSale)
}

// ErrFlashSaleNotFound means there is no Flash Sale with that ID.
var ErrFlashSaleNotFound = errors.New("commerce: flash sale not found")

// GetFlashSale returns one Flash Sale, or ErrFlashSaleNotFound.
func GetFlashSale(ctx context.Context, q Querier, id uuid.UUID) (FlashSale, error) {
	rows, err := q.Query(ctx, `
		SELECT `+flashSaleColumns+`
		FROM flash_sales fs JOIN stores s ON s.id = fs.store_id
		WHERE fs.id = $1`, id)
	if err != nil {
		return FlashSale{}, fmt.Errorf("get flash sale %s: %w", id, err)
	}
	flashSale, err := pgx.CollectExactlyOneRow(rows, scanFlashSale)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlashSale{}, ErrFlashSaleNotFound
	}
	if err != nil {
		return FlashSale{}, fmt.Errorf("get flash sale %s: %w", id, err)
	}
	return flashSale, nil
}

// DueToGoLive is a SCHEDULED Flash Sale whose start time has passed.
type DueToGoLive struct {
	ID     uuid.UUID
	Units  int
	EndsAt time.Time
}

// ListDueToGoLive returns the SCHEDULED Flash Sales whose start time is at or before now.
func ListDueToGoLive(ctx context.Context, q Querier, now time.Time) ([]DueToGoLive, error) {
	rows, err := q.Query(ctx, `
		SELECT id, units, ends_at FROM flash_sales
		WHERE status = $1 AND starts_at <= $2
		ORDER BY starts_at, id`, Scheduled, now)
	if err != nil {
		return nil, fmt.Errorf("list flash sales due to go live: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DueToGoLive])
}

// MarkLive moves a Flash Sale SCHEDULED → LIVE. It does nothing to a Flash Sale that has
// already left SCHEDULED, so running it twice is harmless.
func MarkLive(ctx context.Context, q Querier, id uuid.UUID) error {
	if _, err := q.Exec(ctx, `UPDATE flash_sales SET status = $2 WHERE id = $1 AND status = $3`, id, Live, Scheduled); err != nil {
		return fmt.Errorf("mark flash sale %s live: %w", id, err)
	}
	return nil
}

// EndDue moves every LIVE Flash Sale whose end time is at or before now to ENDED.
func EndDue(ctx context.Context, q Querier, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE flash_sales SET status = $2 WHERE status = $3 AND ends_at <= $1`, now, Ended, Live); err != nil {
		return fmt.Errorf("end flash sales: %w", err)
	}
	return nil
}

// Order is the record of a Shopper's completed purchase, created when a Hold is sold.
type Order struct {
	ID              uuid.UUID
	HoldID          uuid.UUID
	FlashSaleID     uuid.UUID
	StoreID         uuid.UUID
	ShopperID       uuid.UUID
	AmountCents     int64
	Currency        string
	PaymentIntentID string
	CreatedAt       time.Time
}

// NewOrder is a paid Hold, to be recorded as an Order. FeeBPS is the Platform's Fee rate right
// now, captured into the Order's OrderCompleted event.
type NewOrder struct {
	HoldID          uuid.UUID
	FlashSaleID     uuid.UUID
	StoreID         uuid.UUID
	ShopperID       uuid.UUID
	AmountCents     int64
	Currency        string
	PaymentIntentID string
	FeeBPS          int
}

const orderColumns = `id, hold_id, flash_sale_id, store_id, shopper_id, amount_cents, currency, payment_intent_id, created_at`

// RecordOrder writes the Order for a paid Hold and its OrderCompleted outbox row in one
// transaction (ADR-0003, ADR-0006). A Hold has at most one Order: recording one again, as a
// later report of the same payment would, writes nothing and returns the first Order.
func RecordOrder(ctx context.Context, pool *pgxpool.Pool, o NewOrder) (Order, error) {
	var order Order
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			INSERT INTO orders (hold_id, flash_sale_id, store_id, shopper_id, amount_cents, currency, payment_intent_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (hold_id) DO NOTHING
			RETURNING `+orderColumns,
			o.HoldID, o.FlashSaleID, o.StoreID, o.ShopperID, o.AmountCents, o.Currency, o.PaymentIntentID)
		if err != nil {
			return err
		}
		order, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Order])
		if errors.Is(err, pgx.ErrNoRows) {
			// The Hold already has its Order, and that Order already has its outbox row.
			rows, err := tx.Query(ctx, `SELECT `+orderColumns+` FROM orders WHERE hold_id = $1`, o.HoldID)
			if err != nil {
				return err
			}
			order, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Order])
			return err
		}
		if err != nil {
			return err
		}
		eventID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(events.OrderCompleted{
			SchemaVersion: events.OrderCompletedSchemaVersion,
			EventID:       eventID,
			OrderID:       order.ID,
			HoldID:        order.HoldID,
			StoreID:       order.StoreID,
			FlashSaleID:   order.FlashSaleID,
			AmountCents:   order.AmountCents,
			Currency:      order.Currency,
			FeeBPS:        o.FeeBPS,
			OccurredAt:    order.CreatedAt.UTC(),
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO outbox (id, event_type, payload) VALUES ($1, $2, $3)`,
			eventID, events.OrderCompletedType, payload)
		return err
	})
	if err != nil {
		return Order{}, fmt.Errorf("record order for hold %s: %w", o.HoldID, err)
	}
	return order, nil
}

func migrationsDir() fs.FS {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the embed path is fixed at compile time
	}
	return dir
}

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
