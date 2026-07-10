package ledger

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

/*
GetBalance derives the current balance for an account by summing all of its
entry amounts. Returns 0 for accounts with no entries (correct: a new account
with no transactions has a zero balance).

This is the "correct but slow" v1 approach — no denormalized balance column.
Intentional trade-off: correctness and write throughput over read speed.
The scaling path (CQRS read model / balance snapshots) is documented in
ARCHITECTURE_AND_DESIGN.md.
*/
func (s *Service) GetBalance(ctx context.Context, accountID uuid.UUID) (int64, error) {
	const q = `
		SELECT COALESCE(SUM(e.amount), 0)
		FROM entries e
		INNER JOIN transactions t ON e.transaction_id = t.id
		WHERE e.account_id = $1
		  AND t.status = 'posted'
	`
	var balance int64
	if err := s.pool.QueryRow(ctx, q, accountID).Scan(&balance); err != nil {
		return 0, fmt.Errorf("ledger: GetBalance for account %s: %w", accountID, err)
	}
	return balance, nil
}
