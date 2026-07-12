package ledger

import (
	"context"
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

/*
Engine is the interface that defines all ledger operations.
Keeping this as an interface (not just a concrete struct) means the HTTP
handler layer depends on the abstraction, making it trivial to swap in a
mock during handler unit tests without needing a real database.
*/
type Engine interface {
	CreateAccount(ctx context.Context, req CreateAccountRequest) (*db.Account, error)
	PostTransaction(ctx context.Context, req PostTransactionRequest) (*db.Transaction, error)
	GetTransaction(ctx context.Context, transactionID uuid.UUID) (*TransactionDetail, error)
	GetBalance(ctx context.Context, accountID uuid.UUID) (int64, error)
	GetStatement(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]db.Entry, error)
}

/*
Service is the concrete implementation of Engine. It holds a reference to
the pgxpool connection pool and has no knowledge of HTTP, JSON, or any
transport layer.
*/
type Service struct {
	pool *pgxpool.Pool
}

/*
NewService creates a new ledger Service from a live connection pool.
This is the only constructor — dependency-inject the pool from main.go.
*/
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// ── Request / Response types ─────────────────────────────────────────────────

/* CreateAccountRequest carries the input for creating a new ledger account. */
type CreateAccountRequest struct {
	Name        string
	AccountType string // must be one of: asset, liability, equity, revenue, expense
	Currency    string // ISO 4217, e.g. "INR", "USD"
}

/*
EntryInput is a single debit or credit line within a PostTransactionRequest.
Amount is signed: positive = debit, negative = credit (minor units).
*/
type EntryInput struct {
	AccountID uuid.UUID
	Amount    int64 // signed minor units
}

/*
PostTransactionRequest carries all input needed to post a double-entry
transaction. ExchangeRate and RateSource are populated by the currency
conversion layer (Phase 5.5) and are nil for same-currency transactions.
*/
type PostTransactionRequest struct {
	IdempotencyKey string
	Description    string
	Entries        []EntryInput
	ExchangeRate   *float64 // nil for same-currency
	RateSource     *string  // "live" | "stale_cache" | nil
}

/*
TransactionDetail is the response type for GetTransaction. It bundles the
transaction header and all its entries together — the API layer converts this
to a JSON response without needing a second query.
*/
type TransactionDetail struct {
	Transaction *db.Transaction
	Entries     []db.Entry
}
