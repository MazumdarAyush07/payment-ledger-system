package ledger_test

import (
	"context"
	"os"
	"testing"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// testPool is the shared connection pool used by all DB-integration tests.
// It is initialised once in TestMain and closed after all tests complete.
var testPool *pgxpool.Pool

// testService is a ledger.Service wired to testPool.
var testService *ledger.Service

// TestMain sets up the test database connection once for the entire package.
// Tests that require a real DB will skip gracefully if DATABASE_URL is unset,
// allowing `go test ./...` to run cleanly without Docker.
func TestMain(m *testing.M) {
	// Load .env from project root (two levels up from internal/ledger/)
	_ = godotenv.Load("../../.env")

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL != "" {
		var err error
		testPool, err = db.ConnectPool(context.Background(), dbURL)
		if err != nil {
			// Not fatal — tests will skip if pool is nil.
			testPool = nil
		} else {
			testService = ledger.NewService(testPool)
		}
	}

	code := m.Run()

	if testPool != nil {
		testPool.Close()
	}

	os.Exit(code)
}

// requireDB skips the calling test if no database connection is available.
// Call this at the top of any test that needs a real Postgres connection.
func requireDB(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("skipping: DATABASE_URL not set or DB unreachable")
	}
}

// cleanDB truncates all data tables between tests to ensure isolation.
// Truncation cascades via FK constraints (entries → transactions; entries → accounts).
func cleanDB(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`TRUNCATE TABLE entries, transactions, accounts RESTART IDENTITY CASCADE`,
	)
	if err != nil {
		t.Fatalf("cleanDB: %v", err)
	}
}
