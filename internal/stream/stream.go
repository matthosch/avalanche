// Package stream carries events between services over Redis Streams (ADR-0003): commerce's
// relay publishes them, and each consuming service reads them through its own consumer group.
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/matthosch/avalanche/internal/events"
)

// OrdersCompleted is the stream of OrderCompleted events.
const OrdersCompleted = "orders.completed"

// Message fields: the event's type and its JSON payload.
const (
	fieldEventType = "event_type"
	fieldPayload   = "payload"
)

// Publish appends an event, already encoded as JSON, to OrdersCompleted.
func Publish(ctx context.Context, rdb *redis.Client, eventType string, payload []byte) error {
	err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: OrdersCompleted,
		Values: map[string]any{fieldEventType: eventType, fieldPayload: payload},
	}).Err()
	if err != nil {
		return fmt.Errorf("stream: publish %s to %s: %w", eventType, OrdersCompleted, err)
	}
	return nil
}

// HandleOrderCompleted processes one OrderCompleted. Returning nil acknowledges the message,
// so it must only do so once the event's effects are durable.
type HandleOrderCompleted func(ctx context.Context, event events.OrderCompleted) error

const (
	// readBlock is how long a read waits for new messages before checking ctx again.
	readBlock = 2 * time.Second
	// retryEvery is how often this consumer's unacknowledged messages are handled again.
	retryEvery = 5 * time.Second
	readCount  = 100
)

// Consume reads OrdersCompleted as consumer in group until ctx is done, creating the group
// (from the start of the stream) if it doesn't exist. Delivery is at-least-once: a message is
// acknowledged (XACK) only after handle returns nil, so a crash or a failed handle leaves it
// pending, and it is handled again, on start and every few seconds after.
func Consume(ctx context.Context, rdb *redis.Client, group, consumer string, handle HandleOrderCompleted) error {
	err := rdb.XGroupCreateMkStream(ctx, OrdersCompleted, group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("stream: create group %s on %s: %w", group, OrdersCompleted, err)
	}
	var lastRetry time.Time // zero, so pending messages from before a restart go first
	for ctx.Err() == nil {
		// "0" reads this consumer's pending messages; ">" reads new ones.
		id, block := ">", readBlock
		if time.Since(lastRetry) >= retryEvery {
			id, block, lastRetry = "0", -1, time.Now()
		}
		streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: group, Consumer: consumer, Streams: []string{OrdersCompleted, id}, Count: readCount, Block: block,
		}).Result()
		if errors.Is(err, redis.Nil) || ctx.Err() != nil {
			continue
		}
		if err != nil {
			slog.ErrorContext(ctx, "read stream", "stream", OrdersCompleted, "group", group, "err", err)
			sleep(ctx, time.Second)
			continue
		}
		for _, s := range streams {
			for _, message := range s.Messages {
				if err := handleMessage(ctx, message, handle); err != nil {
					slog.ErrorContext(ctx, "handle stream message; left pending", "stream", OrdersCompleted, "group", group, "message_id", message.ID, "err", err)
					continue
				}
				if err := rdb.XAck(ctx, OrdersCompleted, group, message.ID).Err(); err != nil {
					slog.ErrorContext(ctx, "ack stream message", "stream", OrdersCompleted, "group", group, "message_id", message.ID, "err", err)
				}
			}
		}
	}
	return nil
}

func handleMessage(ctx context.Context, message redis.XMessage, handle HandleOrderCompleted) error {
	if eventType, _ := message.Values[fieldEventType].(string); eventType != events.OrderCompletedType {
		return fmt.Errorf("event type %q, want %s", eventType, events.OrderCompletedType)
	}
	payload, _ := message.Values[fieldPayload].(string)
	var event events.OrderCompleted
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return fmt.Errorf("decode %s: %w", events.OrderCompletedType, err)
	}
	return handle(ctx, event)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
