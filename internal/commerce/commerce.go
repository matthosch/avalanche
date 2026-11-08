// Package commerce owns the commerce Postgres database: Stores, Flash Sales and,
// later, Orders and the outbox.
package commerce

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate brings the commerce schema up to date.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationsDir())
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

// ListFlashSales returns every Flash Sale, soonest start first.
func ListFlashSales(ctx context.Context, q Querier) ([]FlashSale, error) {
	rows, err := q.Query(ctx, `
		SELECT fs.id, s.id, s.name, fs.product_name, fs.price_cents, fs.currency, fs.units, fs.starts_at, fs.ends_at, fs.status
		FROM flash_sales fs JOIN stores s ON s.id = fs.store_id
		ORDER BY fs.starts_at, fs.id`)
	if err != nil {
		return nil, fmt.Errorf("list flash sales: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FlashSale, error) {
		var flashSale FlashSale
		err := r.Scan(&flashSale.ID, &flashSale.StoreID, &flashSale.StoreName, &flashSale.ProductName,
			&flashSale.PriceCents, &flashSale.Currency, &flashSale.Units, &flashSale.StartsAt, &flashSale.EndsAt, &flashSale.Status)
		return flashSale, err
	})
}

// DueToGoLive is a SCHEDULED Flash Sale whose start time has passed.
type DueToGoLive struct {
	ID    uuid.UUID
	Units int
}

// ListDueToGoLive returns the SCHEDULED Flash Sales whose start time is at or before now.
func ListDueToGoLive(ctx context.Context, q Querier, now time.Time) ([]DueToGoLive, error) {
	rows, err := q.Query(ctx, `
		SELECT id, units FROM flash_sales
		WHERE status = 'SCHEDULED' AND starts_at <= $1
		ORDER BY starts_at, id`, now)
	if err != nil {
		return nil, fmt.Errorf("list flash sales due to go live: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DueToGoLive])
}

// MarkLive moves a Flash Sale SCHEDULED → LIVE. It does nothing to a Flash Sale that has
// already left SCHEDULED, so running it twice is harmless.
func MarkLive(ctx context.Context, q Querier, id uuid.UUID) error {
	if _, err := q.Exec(ctx, `UPDATE flash_sales SET status = 'LIVE' WHERE id = $1 AND status = 'SCHEDULED'`, id); err != nil {
		return fmt.Errorf("mark flash sale %s live: %w", id, err)
	}
	return nil
}

// EndDue moves every LIVE Flash Sale whose end time is at or before now to ENDED.
func EndDue(ctx context.Context, q Querier, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE flash_sales SET status = 'ENDED' WHERE status = 'LIVE' AND ends_at <= $1`, now); err != nil {
		return fmt.Errorf("end flash sales: %w", err)
	}
	return nil
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
