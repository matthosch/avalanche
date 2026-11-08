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
}

// ListFlashSales returns every Flash Sale, soonest start first.
func ListFlashSales(ctx context.Context, q Querier) ([]FlashSale, error) {
	rows, err := q.Query(ctx, `
		SELECT fs.id, s.id, s.name, fs.product_name, fs.price_cents, fs.currency, fs.units, fs.starts_at, fs.ends_at
		FROM flash_sales fs JOIN stores s ON s.id = fs.store_id
		ORDER BY fs.starts_at, fs.id`)
	if err != nil {
		return nil, fmt.Errorf("list flash sales: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FlashSale, error) {
		var flashSale FlashSale
		err := r.Scan(&flashSale.ID, &flashSale.StoreID, &flashSale.StoreName, &flashSale.ProductName,
			&flashSale.PriceCents, &flashSale.Currency, &flashSale.Units, &flashSale.StartsAt, &flashSale.EndsAt)
		return flashSale, err
	})
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
}
