package ledger

import (
	"context"
	"fmt"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/google/uuid"
)

/* validAccountTypes is the set of accepted account_type values.
Mirrors the account_type enum defined in the DB migration. */
var validAccountTypes = map[string]bool{
	"asset":     true,
	"liability": true,
	"equity":    true,
	"revenue":   true,
	"expense":   true,
}

/*
CreateAccount inserts a new account into the accounts table and returns
the created account. It validates account_type against the known enum values
before touching the database to produce a clear error rather than a raw
Postgres constraint violation.
*/
func (s *Service) CreateAccount(ctx context.Context, req CreateAccountRequest) (*db.Account, error) {
	if !validAccountTypes[req.AccountType] {
		return nil, fmt.Errorf("ledger: invalid account_type %q — must be one of: asset, liability, equity, revenue, expense", req.AccountType)
	}
	if req.Name == "" {
		return nil, fmt.Errorf("ledger: account name must not be empty")
	}
	if len(req.Currency) != 3 {
		return nil, fmt.Errorf("ledger: currency must be a 3-character ISO 4217 code, got %q", req.Currency)
	}

	const q = `
		INSERT INTO accounts (name, account_type, currency)
		VALUES ($1, $2, $3)
		RETURNING id, name, account_type, currency, created_at
	`

	row := s.pool.QueryRow(ctx, q, req.Name, req.AccountType, req.Currency)

	var a db.Account
	if err := row.Scan(&a.ID, &a.Name, &a.AccountType, &a.Currency, &a.CreatedAt); err != nil {
		return nil, fmt.Errorf("ledger: CreateAccount scan: %w", err)
	}

	return &a, nil
}

/*
accountExists checks whether an account with the given ID is present in the
accounts table. Used by PostTransaction to validate entry account IDs before
beginning the DB transaction.
*/
func (s *Service) accountExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	const q = `SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1)`
	if err := s.pool.QueryRow(ctx, q, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("ledger: accountExists: %w", err)
	}
	return exists, nil
}
