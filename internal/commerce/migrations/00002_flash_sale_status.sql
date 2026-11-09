-- +goose Up
ALTER TABLE flash_sales
    ADD COLUMN status text NOT NULL DEFAULT 'SCHEDULED'
        CHECK (status IN ('SCHEDULED', 'LIVE', 'ENDED', 'CLOSED'));

-- The lifecycle tick looks up Flash Sales still due to move.
CREATE INDEX flash_sales_open_status_idx ON flash_sales (status) WHERE status IN ('SCHEDULED', 'LIVE');

-- +goose Down
DROP INDEX flash_sales_open_status_idx;
ALTER TABLE flash_sales DROP COLUMN status;
