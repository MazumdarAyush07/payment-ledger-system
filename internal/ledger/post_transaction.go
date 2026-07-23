package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

/* pgUniqueViolationCode is the PostgreSQL error code for a UNIQUE constraint
violation (23505). We catch this specifically on idempotency_key conflicts. */
const pgUniqueViolationCode = "23505"

/*
ValidateMinEntries returns ErrMinimumEntriesNotMet if fewer than 2 entries
are provided. This is a pure function — no DB access, fully unit-testable.
*/
func ValidateMinEntries(entries []EntryInput) error {
	if len(entries) < 2 {
		return ErrMinimumEntriesNotMet
	}
	return nil
}

/*
ValidateBalance returns ErrUnbalancedTransaction if the sum of all entry
amounts is not zero. This is a pure function — no DB access, fully unit-testable.
The double-entry invariant: every debit must be matched by an equal credit.
*/
func ValidateBalance(entries []EntryInput) error {
	var sum int64
	for _, e := range entries {
		sum += e.Amount
	}
	if sum != 0 {
		return ErrUnbalancedTransaction
	}
	return nil
}

/*
PostTransaction validates and atomically posts a double-entry transaction.

Execution order:
 1. Validate minimum entry count (pure, no DB)
 2. Validate double-entry balance invariant (pure, no DB)
 3. Validate all account IDs exist (DB read)
 4. BEGIN DB transaction
 5. INSERT into transactions (catches idempotency_key duplicate via UNIQUE constraint)
 6. INSERT each entry row
 7. UPDATE transaction status → "posted", set posted_at = NOW()
 8. COMMIT

If any step fails, the DB transaction is rolled back and nothing lands in
the database. The caller receives a typed error they can act on.
*/
func (s *Service) PostTransaction(ctx context.Context, req PostTransactionRequest) (*db.Transaction, error) {
	/* Step 1 & 2: Pure validations — always enforced, no exceptions. */
	if err := ValidateMinEntries(req.Entries); err != nil {
		return nil, err
	}
	if err := ValidateBalance(req.Entries); err != nil {
		return nil, err
	}

	/* Step 3: Validate all account IDs exist */
	for _, e := range req.Entries {
		exists, err := s.accountExists(ctx, e.AccountID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrAccountNotFound, e.AccountID)
		}
	}

	reqHash := computeRequestHash(req.Entries)

	/* Steps 4–8: Atomic DB transaction */
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("ledger: begin transaction: %w", err)
	}
	/*
		Ensure rollback on any failure path. pgx.Tx.Rollback is a no-op after
		a successful Commit, so this defer is always safe.
	*/
	defer tx.Rollback(ctx) //nolint:errcheck

	/* Step 5: Insert transaction header (status = 'pending') */
	const insertTx = `
		INSERT INTO transactions (idempotency_key, request_hash, description, exchange_rate, rate_source)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, idempotency_key, request_hash, description, status, exchange_rate, rate_source, created_at, posted_at
	`
	var t db.Transaction
	err = tx.QueryRow(ctx, insertTx,
		req.IdempotencyKey,
		reqHash,
		req.Description,
		req.ExchangeRate,
		req.RateSource,
	).Scan(
		&t.ID, &t.IdempotencyKey, &t.RequestHash, &t.Description, &t.Status,
		&t.ExchangeRate, &t.RateSource, &t.CreatedAt, &t.PostedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolationCode {
			/*
				Idempotency collision: the key was already committed.
				Roll back immediately and fetch the original transaction.
			*/
			tx.Rollback(ctx) //nolint:errcheck
			origTx, fetchErr := s.getTransactionByKey(ctx, req.IdempotencyKey)
			if fetchErr != nil {
				return nil, fetchErr
			}
			if origTx.RequestHash != reqHash {
				return nil, ErrIdempotencyConflict
			}
			return origTx, nil
		}
		return nil, fmt.Errorf("ledger: insert transaction: %w", err)
	}

	/* Step 6: Insert each entry */
	const insertEntry = `
		INSERT INTO entries (transaction_id, account_id, amount)
		VALUES ($1, $2, $3)
	`
	for _, e := range req.Entries {
		if _, err := tx.Exec(ctx, insertEntry, t.ID, e.AccountID, e.Amount); err != nil {
			return nil, fmt.Errorf("ledger: insert entry for account %s: %w", e.AccountID, err)
		}
	}

	/* Step 7: Mark transaction as posted and stamp posted_at */
	const markPosted = `
		UPDATE transactions
		SET status = 'posted', posted_at = NOW()
		WHERE id = $1
		RETURNING status, posted_at
	`
	if err := tx.QueryRow(ctx, markPosted, t.ID).Scan(&t.Status, &t.PostedAt); err != nil {
		return nil, fmt.Errorf("ledger: mark posted: %w", err)
	}

	/* Step 8: Commit */
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("ledger: commit transaction: %w", err)
	}

	return &t, nil
}

/*
getTransactionByKey fetches an existing transaction by its idempotency key.
Called on an idempotency collision to return the original result to the caller.
*/
func (s *Service) getTransactionByKey(ctx context.Context, key string) (*db.Transaction, error) {
	const q = `
		SELECT id, idempotency_key, request_hash, description, status, exchange_rate, rate_source, created_at, posted_at
		FROM transactions
		WHERE idempotency_key = $1
	`
	var t db.Transaction
	err := s.pool.QueryRow(ctx, q, key).Scan(
		&t.ID, &t.IdempotencyKey, &t.RequestHash, &t.Description, &t.Status,
		&t.ExchangeRate, &t.RateSource, &t.CreatedAt, &t.PostedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("ledger: idempotency key %q not found after collision — unexpected state", key)
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: getTransactionByKey: %w", err)
	}
	return &t, nil
}

/* postedAt returns a non-nil time.Time for use in tests and assertions. */
func postedAt(t *db.Transaction) time.Time {
	if t.PostedAt != nil {
		return *t.PostedAt
	}
	return time.Time{}
}

/*
computeRequestHash deterministically hashes the core payload of a transaction request.
We sort the entries by AccountID+Amount so that mathematically identical requests
produce the exact same hash, even if the entries array arrives in a different order.
*/
func computeRequestHash(entries []EntryInput) string {
	sorted := make([]EntryInput, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		idI, idJ := sorted[i].AccountID.String(), sorted[j].AccountID.String()
		if idI != idJ {
			return idI < idJ
		}
		return sorted[i].Amount < sorted[j].Amount
	})

	hash := sha256.New()
	for _, e := range sorted {
		fmt.Fprintf(hash, "%s:%d;", e.AccountID.String(), e.Amount)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
