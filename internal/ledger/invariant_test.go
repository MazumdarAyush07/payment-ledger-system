package ledger_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/google/uuid"
)

/*
TestLedgerInvariants is the centrepiece Phase 6 test.

It proves that the ledger is structurally correct under load — not just that
individual transactions work, but that the system as a whole maintains its
core mathematical property at all times.

Subtests:
  - GlobalZeroSum: post 60 random balanced transactions across 5 accounts,
    then query the DB directly: SUM(amount) across all entries must be 0.
  - ConcurrentGlobalZeroSum: same assertion with 30 goroutines posting
    simultaneously — proves the invariant holds under concurrency.
*/
func TestLedgerInvariants(t *testing.T) {
	t.Run("GlobalZeroSum", testGlobalZeroSum)
	t.Run("ConcurrentGlobalZeroSum", testConcurrentGlobalZeroSum)
}

// ── Shared helpers ────────────────────────────────────────────────────────────

/*
assertGlobalSum queries the database and asserts that the sum of all entry
amounts across the entire ledger is zero.

This is the double-entry invariant: every debit must be matched by an equal
and opposite credit, globally. If this ever fails it means a transaction was
posted with unbalanced entries — which ValidateBalance should make impossible.
*/
func assertGlobalSum(t *testing.T) {
	t.Helper()
	var sum int64
	err := testPool.QueryRow(
		context.Background(),
		`SELECT COALESCE(SUM(amount), 0) FROM entries`,
	).Scan(&sum)
	if err != nil {
		t.Fatalf("assertGlobalSum: query failed: %v", err)
	}
	if sum != 0 {
		t.Errorf("GLOBAL INVARIANT VIOLATED: SUM(entries.amount) = %d, expected 0", sum)
	}
}

/*
seedAccounts creates n accounts for use within a test.
All are "asset" accounts in INR. Returns their UUIDs as a slice.
*/
func seedAccounts(t *testing.T, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	for i := range ids {
		acc, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
			Name:        fmt.Sprintf("Account-%d", i+1),
			AccountType: "asset",
			Currency:    "INR",
		})
		if err != nil {
			t.Fatalf("seedAccounts: %v", err)
		}
		ids[i] = acc.ID
	}
	return ids
}

// ── GlobalZeroSum ─────────────────────────────────────────────────────────────

/*
testGlobalZeroSum posts 60 random balanced transactions across 5 accounts,
then asserts SUM(entries.amount) = 0 across the entire database.

Each transaction picks two random distinct accounts and moves a random amount
between them. Because every transaction is balanced, the global sum must always
be zero regardless of how many transactions exist or which accounts are involved.
*/
func testGlobalZeroSum(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	const numAccounts = 5
	const numTransactions = 60

	accounts := seedAccounts(t, numAccounts)

	for i := 0; i < numTransactions; i++ {
		srcIdx := rand.Intn(numAccounts)
		dstIdx := rand.Intn(numAccounts)
		for dstIdx == srcIdx {
			dstIdx = rand.Intn(numAccounts)
		}

		/* Random amount: ₹1 to ₹1,000 in paise. */
		amount := int64(rand.Intn(100_000) + 1)

		_, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
			IdempotencyKey: fmt.Sprintf("gzs-tx-%d", i),
			Description:    fmt.Sprintf("Random payment #%d", i),
			Entries: []ledger.EntryInput{
				{AccountID: accounts[srcIdx], Amount: -amount},
				{AccountID: accounts[dstIdx], Amount: amount},
			},
		})
		if err != nil {
			t.Fatalf("PostTransaction[%d]: %v", i, err)
		}
	}

	assertGlobalSum(t)
}

// ── ConcurrentGlobalZeroSum ───────────────────────────────────────────────────

/*
testConcurrentGlobalZeroSum fires 30 goroutines simultaneously, each posting
a unique balanced transaction, then asserts SUM(entries.amount) = 0.

This is the hardest correctness test: concurrent writes against a shared DB.
Any race that allowed a partial write or a duplicate entry would produce a
non-zero global sum and fail this assertion.

The channel start-gun pattern (close(start)) ensures maximum goroutine overlap
at the DB layer — all goroutines are created first, then all are unblocked at
the same instant.
*/
func testConcurrentGlobalZeroSum(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	const numAccounts = 4
	const numGoroutines = 30

	accounts := seedAccounts(t, numAccounts)

	errs := make([]error, numGoroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // block until all goroutines are ready, then release together

			srcIdx := i % numAccounts
			dstIdx := (i + 1) % numAccounts
			amount := int64((i + 1) * 1000)

			_, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
				IdempotencyKey: fmt.Sprintf("cgzs-%d", i),
				Description:    fmt.Sprintf("Concurrent payment #%d", i),
				Entries: []ledger.EntryInput{
					{AccountID: accounts[srcIdx], Amount: -amount},
					{AccountID: accounts[dstIdx], Amount: amount},
				},
			})
			errs[i] = err
		}(i)
	}

	close(start) // release all goroutines simultaneously
	wg.Wait()

	/* All goroutines must have succeeded. */
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	/* Global invariant must hold after all concurrent writes. */
	assertGlobalSum(t)
}
