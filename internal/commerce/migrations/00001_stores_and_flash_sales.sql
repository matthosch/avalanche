-- +goose Up
CREATE TABLE stores (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE flash_sales (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    store_id     uuid        NOT NULL REFERENCES stores (id),
    product_name text        NOT NULL,
    price_cents  bigint      NOT NULL CHECK (price_cents >= 0),
    currency     text        NOT NULL DEFAULT 'USD' CHECK (currency = 'USD'),
    units        integer     NOT NULL CHECK (units > 0),
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);

CREATE INDEX flash_sales_starts_at_idx ON flash_sales (starts_at);

-- +goose Down
DROP TABLE flash_sales;
DROP TABLE stores;
