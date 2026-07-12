package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

/*
GetTransaction fetches a transaction header and all of its entries in a single
call. The API layer uses this to serve GET /transactions/{id} without needing
a second round-trip query for the entries.

Returns ErrTransactionNotFound if the ID does not exist.
*/
func (s *Service) GetTransaction(ctx context.Context, transactionID uuid.UUID) (*TransactionDetail, error) {
	/* Step 1: Fetch the transaction header */
	const txQuery = `
		SELECT id, idempotency_key, description, status,
		       exchange_rate, rate_source, created_at, posted_at
		FROM transactions
		WHERE id = $1
	`
	var t db.Transaction
	err := s.pool.QueryRow(ctx, txQuery, transactionID).Scan(
		&t.ID, &t.IdempotencyKey, &t.Description, &t.Status,
		&t.ExchangeRate, &t.RateSource, &t.CreatedAt, &t.PostedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrTransactionNotFound, transactionID)
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: GetTransaction header scan: %w", err)
	}

	/* Step 2: Fetch all entries for this transaction */
	const entryQuery = `
		SELECT id, transaction_id, account_id, amount, created_at
		FROM entries
		WHERE transaction_id = $1
		ORDER BY id ASC
	`
	rows, err := s.pool.Query(ctx, entryQuery, transactionID)
	if err != nil {
		return nil, fmt.Errorf("ledger: GetTransaction entries query: %w", err)
	}
	defer rows.Close()

	var entries []db.Entry
	for rows.Next() {
		var e db.Entry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.AccountID, &e.Amount, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("ledger: GetTransaction entries scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: GetTransaction entries rows: %w", err)
	}

	/* Return an empty slice (not nil) so callers don't need nil checks. */
	if entries == nil {
		entries = []db.Entry{}
	}

	return &TransactionDetail{
		Transaction: &t,
		Entries:     entries,
	}, nil
}
