-- +goose Up
-- One Journal Entry per event: event_id is unique, so a redelivered event posts nothing
-- (ADR-0003).
CREATE TABLE journal_entries (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    event_id   uuid        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A Posting's amount is signed: a debit is positive, a credit negative, so a Journal Entry
-- balances when its Postings sum to zero (ADR-0004).
CREATE TABLE postings (
    id               uuid   PRIMARY KEY DEFAULT uuidv7(),
    journal_entry_id uuid   NOT NULL REFERENCES journal_entries (id),
    ledger_account   text   NOT NULL,
    amount_cents     bigint NOT NULL,
    currency         text   NOT NULL CHECK (currency = 'USD')
);

-- Balances are summed per Ledger Account.
CREATE INDEX postings_ledger_account_idx ON postings (ledger_account);
CREATE INDEX postings_journal_entry_id_idx ON postings (journal_entry_id);

-- +goose StatementBegin
CREATE FUNCTION postings_balance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    total bigint;
BEGIN
    SELECT sum(amount_cents) INTO total FROM postings WHERE journal_entry_id = NEW.journal_entry_id;
    IF total <> 0 THEN
        RAISE EXCEPTION 'journal entry % postings sum to %, not 0', NEW.journal_entry_id, total
            USING ERRCODE = 'check_violation', CONSTRAINT = 'postings_balance';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Checked at commit, once every Posting of the Journal Entry is in.
CREATE CONSTRAINT TRIGGER postings_balance
    AFTER INSERT ON postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION postings_balance();

-- +goose StatementBegin
CREATE FUNCTION ledger_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'the ledger is append-only: % on % is not allowed', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

-- Corrections are reversing Journal Entries, never edits (ADR-0004).
CREATE TRIGGER journal_entries_append_only
    BEFORE UPDATE OR DELETE ON journal_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER postings_append_only
    BEFORE UPDATE OR DELETE ON postings
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

-- +goose Down
DROP TABLE postings;
DROP TABLE journal_entries;
DROP FUNCTION ledger_append_only;
DROP FUNCTION postings_balance;
