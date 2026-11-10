// Package commerce owns the commerce Postgres database: Stores, Flash Sales, Orders and the
// outbox.
package commerce

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/events"
	"github.com/matthosch/avalanche/internal/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate brings the commerce schema up to date. It is safe to call from several processes at
// once (api and worker both migrate at startup).
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return migrate.Up(ctx, pool, "commerce", migrate.Sub(migrations, "migrations"))
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

// OutboxEvent is an event waiting in the outbox to be published.
type OutboxEvent struct {
	ID        uuid.UUID
	EventType string
	Payload   []byte
}

// PublishOutbox claims up to limit unpublished outbox rows, oldest first, hands each to
// publish, and marks the ones it published. It returns how many it published.
//
// Rows are claimed with FOR UPDATE SKIP LOCKED, so several relays never publish the same row
// at once (ADR-0003). It stops at the first failed publish, keeping what was published before
// it. A row published but not marked, if the commit fails, is published again later: delivery
// is at-least-once, and consumers keep only the first copy of each event_id.
func PublishOutbox(ctx context.Context, pool *pgxpool.Pool, limit int, publish func(context.Context, OutboxEvent) error) (int, error) {
	var published []uuid.UUID
	var publishErr error
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, event_type, payload FROM outbox
			WHERE published_at IS NULL
			ORDER BY created_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		claimed, err := pgx.CollectRows(rows, pgx.RowToStructByPos[OutboxEvent])
		if err != nil {
			return err
		}
		for _, event := range claimed {
			if publishErr = publish(ctx, event); publishErr != nil {
				break
			}
			published = append(published, event.ID)
		}
		if len(published) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, published)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("publish outbox: %w", err)
	}
	if publishErr != nil {
		return len(published), fmt.Errorf("publish outbox: %w", publishErr)
	}
	return len(published), nil
}

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
