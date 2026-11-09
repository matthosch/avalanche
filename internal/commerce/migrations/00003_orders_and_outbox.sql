-- +goose Up
-- One Order per Hold: hold_id is unique, so however many times a payment's success is
-- reported, only the first writes an Order (ADR-0006).
CREATE TABLE orders (
    id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    hold_id           uuid        NOT NULL UNIQUE,
    flash_sale_id     uuid        NOT NULL REFERENCES flash_sales (id),
    store_id          uuid        NOT NULL REFERENCES stores (id),
    shopper_id        uuid        NOT NULL,
    amount_cents      bigint      NOT NULL CHECK (amount_cents >= 0),
    currency          text        NOT NULL CHECK (currency = 'USD'),
    payment_intent_id text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- Events waiting to be published, written in the same transaction as the change they report
-- (ADR-0003). The id is the event's event_id.
CREATE TABLE outbox (
    id           uuid        PRIMARY KEY,
    event_type   text        NOT NULL,
    payload      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

-- The relay looks up unpublished rows, oldest first.
CREATE INDEX outbox_unpublished_idx ON outbox (created_at) WHERE published_at IS NULL;

-- +goose Down
DROP TABLE outbox;
DROP TABLE orders;
