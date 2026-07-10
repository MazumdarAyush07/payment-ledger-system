package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/google/uuid"
)

/*
GetStatement returns all entries for an account whose parent transaction was
posted within the [from, to] time range (inclusive). Entries are ordered by
posted_at ascending so callers see the chronological transaction history.

Filtering is on transactions.posted_at (not entries.created_at) — per the
data contract, only fully committed entries appear on a statement. An entry
whose parent transaction failed or is still pending is never surfaced here.
*/
func (s *Service) GetStatement(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]db.Entry, error) {
	const q = `
		SELECT e.id, e.transaction_id, e.account_id, e.amount, e.created_at
		FROM entries e
		INNER JOIN transactions t ON e.transaction_id = t.id
		WHERE e.account_id = $1
		  AND t.status    = 'posted'
		  AND t.posted_at >= $2
		  AND t.posted_at <= $3
		ORDER BY t.posted_at ASC, e.id ASC
	`

	rows, err := s.pool.Query(ctx, q, accountID, from, to)
	if err != nil {
		return nil, fmt.Errorf("ledger: GetStatement query: %w", err)
	}
	defer rows.Close()

	var entries []db.Entry
	for rows.Next() {
		var e db.Entry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.AccountID, &e.Amount, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("ledger: GetStatement scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: GetStatement rows: %w", err)
	}

	/* Return an empty slice (not nil) so callers don't need nil checks. */
	if entries == nil {
		entries = []db.Entry{}
	}

	return entries, nil
}
