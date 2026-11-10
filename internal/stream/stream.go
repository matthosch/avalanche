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
	// retryEvery is how often unacknowledged messages are claimed and handled again; a message
	// is claimed once it has been pending at least this long.
	retryEvery = 5 * time.Second
	readCount  = 100
)

// Consume reads OrdersCompleted as consumer in group until ctx is done, creating the group
// (from the start of the stream) if it doesn't exist. Delivery is at-least-once: a message is
// acknowledged (XACK) only after handle returns nil, so a crash or a failed handle leaves it
// pending. Every few seconds the consumer claims (XAUTOCLAIM) the group's messages that have
// been pending a while, whichever consumer read them, so a message read by a consumer that
// has since gone away under another name is still handled.
//
// A message that keeps failing is retried without limit for now; moving it to a dead-letter
// stream after 5 deliveries (ADR-0003) is #16.
func Consume(ctx context.Context, rdb *redis.Client, group, consumer string, handle HandleOrderCompleted) error {
	err := rdb.XGroupCreateMkStream(ctx, OrdersCompleted, group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("stream: create group %s on %s: %w", group, OrdersCompleted, err)
	}
	log := slog.With("stream", OrdersCompleted, "group", group)
	var lastRetry time.Time // zero, so messages left pending before a restart go first
	for ctx.Err() == nil {
		var messages []redis.XMessage
		if time.Since(lastRetry) >= retryEvery {
			lastRetry = time.Now()
			messages, _, err = rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
				Stream: OrdersCompleted, Group: group, Consumer: consumer, MinIdle: retryEvery, Start: "0-0", Count: readCount,
			}).Result()
		} else {
			var streams []redis.XStream
			streams, err = rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group: group, Consumer: consumer, Streams: []string{OrdersCompleted, ">"}, Count: readCount, Block: readBlock,
			}).Result()
			for _, s := range streams {
				messages = append(messages, s.Messages...)
			}
		}
		if errors.Is(err, redis.Nil) || ctx.Err() != nil {
			continue
		}
		if err != nil {
			log.ErrorContext(ctx, "read stream", "err", err)
			sleep(ctx, time.Second)
			continue
		}
		for _, message := range messages {
			if err := handleMessage(ctx, message, handle); err != nil {
				log.ErrorContext(ctx, "handle stream message; left pending", "message_id", message.ID, "err", err)
				continue
			}
			if err := rdb.XAck(ctx, OrdersCompleted, group, message.ID).Err(); err != nil {
				log.ErrorContext(ctx, "ack stream message", "message_id", message.ID, "err", err)
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
