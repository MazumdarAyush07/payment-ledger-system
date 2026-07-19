package api

import (
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/google/uuid"
)

// ── Request DTOs ──────────────────────────────────────────────────────────────

/* CreateAccountRequest is the JSON body for POST /accounts. */
type CreateAccountRequest struct {
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
	Currency    string `json:"currency"`
}

/* EntryInput is a single line item inside PostTransactionRequest. */
type EntryInput struct {
	AccountID uuid.UUID `json:"account_id"`
	Amount    int64     `json:"amount"` // signed minor units
}

/* PostTransactionRequest is the JSON body for POST /transactions. */
type PostTransactionRequest struct {
	IdempotencyKey string       `json:"idempotency_key"`
	Description    string       `json:"description"`
	Entries        []EntryInput `json:"entries"`
	/*
		FromCurrency and ToCurrency are required ISO 4217 currency codes.
		Set both to the same currency for a same-currency transaction (no conversion).
		Set them to different currencies to trigger cross-currency conversion via
		the rate service — all entry amounts are converted to ToCurrency before
		posting to the ledger so the zero-balance invariant is always preserved.
	*/
	FromCurrency string `json:"from_currency"`
	ToCurrency   string `json:"to_currency"`
}


/* StatementQuery holds validated query parameters for GET /accounts/{id}/statement. */
type StatementQuery struct {
	From time.Time
	To   time.Time
}

// ── Response DTOs ─────────────────────────────────────────────────────────────

/* AccountResponse is the JSON representation of a created account. */
type AccountResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	AccountType string    `json:"account_type"`
	Currency    string    `json:"currency"`
	CreatedAt   time.Time `json:"created_at"`
}

/* BalanceResponse is the JSON response for GET /accounts/{id}/balance. */
type BalanceResponse struct {
	AccountID uuid.UUID `json:"account_id"`
	Balance   int64     `json:"balance"` // signed minor units
	Currency  string    `json:"currency"`
}

/* EntryResponse is the JSON representation of a single entry. */
type EntryResponse struct {
	ID            uuid.UUID `json:"id"`
	TransactionID uuid.UUID `json:"transaction_id"`
	AccountID     uuid.UUID `json:"account_id"`
	Amount        int64     `json:"amount"`
	CreatedAt     time.Time `json:"created_at"`
}

/* StatementResponse is the JSON response for GET /accounts/{id}/statement. */
type StatementResponse struct {
	AccountID uuid.UUID       `json:"account_id"`
	From      time.Time       `json:"from"`
	To        time.Time       `json:"to"`
	Entries   []EntryResponse `json:"entries"`
}

/* TransactionResponse is the JSON representation of a transaction header. */
type TransactionResponse struct {
	ID             uuid.UUID  `json:"id"`
	IdempotencyKey string     `json:"idempotency_key"`
	Description    string     `json:"description"`
	Status         string     `json:"status"`
	ExchangeRate   *float64   `json:"exchange_rate,omitempty"`
	RateSource     *string    `json:"rate_source,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	PostedAt       *time.Time `json:"posted_at,omitempty"`
}

/* TransactionDetailResponse is the JSON response for GET /transactions/{id}. */
type TransactionDetailResponse struct {
	Transaction TransactionResponse `json:"transaction"`
	Entries     []EntryResponse     `json:"entries"`
}

// ── Mapping helpers ───────────────────────────────────────────────────────────

func accountToResponse(a *db.Account) AccountResponse {
	return AccountResponse{
		ID:          a.ID,
		Name:        a.Name,
		AccountType: a.AccountType,
		Currency:    a.Currency,
		CreatedAt:   a.CreatedAt,
	}
}

func transactionToResponse(t *db.Transaction) TransactionResponse {
	return TransactionResponse{
		ID:             t.ID,
		IdempotencyKey: t.IdempotencyKey,
		Description:    t.Description,
		Status:         t.Status,
		ExchangeRate:   t.ExchangeRate,
		RateSource:     t.RateSource,
		CreatedAt:      t.CreatedAt,
		PostedAt:       t.PostedAt,
	}
}

func entryToResponse(e db.Entry) EntryResponse {
	return EntryResponse{
		ID:            e.ID,
		TransactionID: e.TransactionID,
		AccountID:     e.AccountID,
		Amount:        e.Amount,
		CreatedAt:     e.CreatedAt,
	}
}

func entriesToResponse(entries []db.Entry) []EntryResponse {
	out := make([]EntryResponse, len(entries))
	for i, e := range entries {
		out[i] = entryToResponse(e)
	}
	return out
}
