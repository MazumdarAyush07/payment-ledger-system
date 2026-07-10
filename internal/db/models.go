package db

import (
	"time"

	"github.com/google/uuid"
)

/*
Account maps to the accounts table. Balance is never stored here —
it is derived by summing the entries belonging to this account.
*/
type Account struct {
	ID          uuid.UUID
	Name        string
	AccountType string
	Currency    string // ISO 4217, e.g. "INR", "USD"
	CreatedAt   time.Time
}

/*
Transaction maps to the transactions table.
ExchangeRate and RateSource are nil for same-currency transactions.
PostedAt is nil until the transaction is atomically committed.
*/
type Transaction struct {
	ID             uuid.UUID
	IdempotencyKey string
	Description    string
	Status         string // "pending" | "posted" | "failed"
	ExchangeRate   *float64
	RateSource     *string // "live" | "stale_cache"
	CreatedAt      time.Time
	PostedAt       *time.Time
}

/*
Entry maps to the entries table. Entries are immutable once written.
Amount is a signed int64 in minor units (paise/cents):

	Positive = Debit | Negative = Credit

The double-entry invariant: SUM(amount) = 0 for every transaction_id.
*/
type Entry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	Amount        int64 // signed minor units
	CreatedAt     time.Time
}
