// Package relay moves events from the commerce outbox onto their Redis Stream (ADR-0003).
package relay

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/matthosch/avalanche/internal/commerce"
	"github.com/matthosch/avalanche/internal/stream"
)

// batch is how many outbox rows one pass claims.
const batch = 100

// Run publishes the outbox to the stream until ctx is done. While a pass fills its batch the
// next starts at once; otherwise the relay waits interval, so an Order reaches the stream at
// most about that long after it is written.
func Run(ctx context.Context, commerceDB *pgxpool.Pool, rdb *redis.Client, interval time.Duration) {
	publish := func(ctx context.Context, event commerce.OutboxEvent) error {
		return stream.Publish(ctx, rdb, event.EventType, event.Payload)
	}
	for {
		n, err := commerce.PublishOutbox(ctx, commerceDB, batch, publish)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "relay outbox", "err", err)
		}
		if n == batch && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
