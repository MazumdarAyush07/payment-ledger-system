-- +goose Up
-- +goose StatementBegin

-- ---------------------------------------------------------------------------
-- Enums
-- ---------------------------------------------------------------------------

CREATE TYPE account_type AS ENUM (
    'asset',
    'liability',
    'equity',
    'revenue',
    'expense'
);

CREATE TYPE transaction_status AS ENUM (
    'pending',
    'posted',
    'failed'
);

CREATE TYPE rate_source AS ENUM (
    'live',
    'stale_cache'
);

-- ---------------------------------------------------------------------------
-- accounts
-- ---------------------------------------------------------------------------
-- An account is a named financial container. Balance is never stored here;
-- it is always derived by summing entries (SUM(amount) WHERE account_id = X).
-- ---------------------------------------------------------------------------

CREATE TABLE accounts (
    id           UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT          NOT NULL,
    account_type account_type  NOT NULL,
    currency     CHAR(3)       NOT NULL,     -- ISO 4217 (e.g. 'INR', 'USD')
    created_at   TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- transactions
-- ---------------------------------------------------------------------------
-- A transaction is the atomic financial event header. It owns 2+ entries.
-- idempotency_key is UNIQUE — this is the actual safety net against duplicate
-- processing under concurrency (not application-level checks alone).
-- exchange_rate and rate_source are NULL for same-currency transactions.
-- ---------------------------------------------------------------------------

CREATE TABLE transactions (
    id               UUID                NOT NULL DEFAULT gen_random_uuid(),
    idempotency_key  TEXT                NOT NULL,
    description      TEXT                NOT NULL DEFAULT '',
    status           transaction_status  NOT NULL DEFAULT 'pending',
    exchange_rate    FLOAT8              NULL,     -- ratio, e.g. 83.50 for USD→INR
    rate_source      rate_source         NULL,     -- 'live' | 'stale_cache'
    created_at       TIMESTAMPTZ         NOT NULL DEFAULT NOW(),
    posted_at        TIMESTAMPTZ         NULL,     -- set on COMMIT; NULL until posted

    CONSTRAINT transactions_pkey PRIMARY KEY (id),
    CONSTRAINT transactions_idempotency_key_unique UNIQUE (idempotency_key)
);

-- ---------------------------------------------------------------------------
-- entries
-- ---------------------------------------------------------------------------
-- An entry is a single immutable debit/credit line on one account.
-- amount is a SIGNED int8 in minor units (paise/cents).
--   Positive = Debit  |  Negative = Credit
-- The double-entry invariant: SUM(amount) = 0 for every transaction_id.
-- Entries are NEVER updated or deleted after being written.
-- ---------------------------------------------------------------------------

CREATE TABLE entries (
    id              UUID          NOT NULL DEFAULT gen_random_uuid(),
    transaction_id  UUID          NOT NULL,
    account_id      UUID          NOT NULL,
    amount          BIGINT        NOT NULL,   -- int8, signed minor units
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

    CONSTRAINT entries_pkey PRIMARY KEY (id),
    CONSTRAINT entries_transaction_id_fkey
        FOREIGN KEY (transaction_id) REFERENCES transactions (id),
    CONSTRAINT entries_account_id_fkey
        FOREIGN KEY (account_id) REFERENCES accounts (id)
);

-- ---------------------------------------------------------------------------
-- Indexes
-- ---------------------------------------------------------------------------

-- Covers: "get all entries for account X" (balance + statement queries)
CREATE INDEX idx_entries_account_id ON entries (account_id);

-- Covers: "get all entries for transaction Y" (transaction detail queries)
CREATE INDEX idx_entries_transaction_id ON entries (transaction_id);

-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS entries;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS accounts;

DROP TYPE IF EXISTS rate_source;
DROP TYPE IF EXISTS transaction_status;
DROP TYPE IF EXISTS account_type;

-- +goose StatementEnd
