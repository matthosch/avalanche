// Package events holds the payloads of the events services publish to each other, shared as
// Go types so producer and consumers agree at compile time (ADR-0005).
package events

import (
	"time"

	"github.com/google/uuid"
)

// OrderCompletedType names OrderCompleted events in the outbox and on the stream.
const OrderCompletedType = "OrderCompleted"

// OrderCompletedSchemaVersion is the version of OrderCompleted that this code writes.
const OrderCompletedSchemaVersion = 1

// OrderCompleted is the fact, published by commerce, that an Order was paid for (ADR-0003).
type OrderCompleted struct {
	SchemaVersion int       `json:"schema_version"`
	EventID       uuid.UUID `json:"event_id"`
	OrderID       uuid.UUID `json:"order_id"`
	HoldID        uuid.UUID `json:"hold_id"`
	StoreID       uuid.UUID `json:"store_id"`
	FlashSaleID   uuid.UUID `json:"flash_sale_id"`
	AmountCents   int64     `json:"amount_cents"`
	Currency      string    `json:"currency"`
	// FeeBPS is the Platform's Fee rate at the moment of the Order, so replaying the event
	// never depends on today's configuration.
	FeeBPS     int       `json:"fee_bps"`
	OccurredAt time.Time `json:"occurred_at"`
}
