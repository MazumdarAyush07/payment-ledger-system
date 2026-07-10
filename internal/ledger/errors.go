package ledger

import "errors"

/*
Domain-typed sentinel errors for the ledger engine.
Callers (e.g. the HTTP handler layer) switch on these to map the exact
failure mode to the correct HTTP status code — no string parsing required.
*/
var (
	/*
		ErrUnbalancedTransaction is returned when the sum of all entry amounts
		in a transaction does not equal zero. HTTP → 422 Unprocessable Entity.
	*/
	ErrUnbalancedTransaction = errors.New("ledger: transaction entries do not sum to zero")

	/*
		ErrMinimumEntriesNotMet is returned when fewer than 2 entries are
		provided. Double-entry bookkeeping requires at least one debit and one
		credit. HTTP → 422 Unprocessable Entity.
	*/
	ErrMinimumEntriesNotMet = errors.New("ledger: transaction requires at least 2 entries")

	/*
		ErrAccountNotFound is returned when an entry references an account_id
		that does not exist in the accounts table. HTTP → 404 Not Found.
	*/
	ErrAccountNotFound = errors.New("ledger: account not found")

	/*
		ErrCurrencyMismatch is returned when an entry's expected currency does
		not match the currency of the referenced account. HTTP → 422.
	*/
	ErrCurrencyMismatch = errors.New("ledger: entry currency does not match account currency")

	/*
		ErrDuplicateIdempotencyKey is returned when the DB unique constraint on
		transactions.idempotency_key fires — indicates the caller should fetch
		and return the original transaction instead of re-processing.
		This is an internal signal; callers should never surface it directly.
	*/
	ErrDuplicateIdempotencyKey = errors.New("ledger: idempotency key already exists")
)
