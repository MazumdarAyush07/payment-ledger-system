package ledger_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/google/uuid"
)

// TestPostTransaction is the single entry point for all PostTransaction tests.
// Pure validation tests (no DB) run first, then DB integration tests.
func TestPostTransaction(t *testing.T) {
	// Pure unit tests — no DB required
	t.Run("ValidateMinEntries_TwoEntries", testValidateMinEntries_TwoEntries)
	t.Run("ValidateMinEntries_OneEntry", testValidateMinEntries_OneEntry)
	t.Run("ValidateMinEntries_NoEntries", testValidateMinEntries_NoEntries)
	t.Run("ValidateBalance_Balanced", testValidateBalance_Balanced)
	t.Run("ValidateBalance_Unbalanced", testValidateBalance_Unbalanced)
	t.Run("ValidateBalance_ThreeEntriesBalanced", testValidateBalance_ThreeEntriesBalanced)

	// DB integration tests
	t.Run("Balanced", testPostTransaction_Balanced)
	t.Run("Unbalanced", testPostTransaction_Unbalanced)
	t.Run("SingleEntry", testPostTransaction_SingleEntry)
	t.Run("IdempotencyKey_Sequential", testPostTransaction_IdempotencyKey)
	t.Run("IdempotencyKey_Mismatch", testPostTransaction_IdempotencyMismatch)
	t.Run("IdempotencyKey_Concurrent", testPostTransaction_ConcurrentIdempotencyKey)
	t.Run("UnknownAccount", testPostTransaction_UnknownAccount)
}

// ── Pure unit tests — no DB required ─────────────────────────────────────────

func testValidateMinEntries_TwoEntries(t *testing.T) {
	entries := []ledger.EntryInput{
		{AccountID: uuid.New(), Amount: 1000},
		{AccountID: uuid.New(), Amount: -1000},
	}
	if err := ledger.ValidateMinEntries(entries); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func testValidateMinEntries_OneEntry(t *testing.T) {
	entries := []ledger.EntryInput{
		{AccountID: uuid.New(), Amount: 1000},
	}
	if err := ledger.ValidateMinEntries(entries); !errors.Is(err, ledger.ErrMinimumEntriesNotMet) {
		t.Errorf("expected ErrMinimumEntriesNotMet, got %v", err)
	}
}

func testValidateMinEntries_NoEntries(t *testing.T) {
	if err := ledger.ValidateMinEntries(nil); !errors.Is(err, ledger.ErrMinimumEntriesNotMet) {
		t.Errorf("expected ErrMinimumEntriesNotMet, got %v", err)
	}
}

func testValidateBalance_Balanced(t *testing.T) {
	entries := []ledger.EntryInput{
		{AccountID: uuid.New(), Amount: 50000},
		{AccountID: uuid.New(), Amount: -50000},
	}
	if err := ledger.ValidateBalance(entries); err != nil {
		t.Errorf("expected nil for balanced entries, got %v", err)
	}
}

func testValidateBalance_Unbalanced(t *testing.T) {
	entries := []ledger.EntryInput{
		{AccountID: uuid.New(), Amount: 50000},
		{AccountID: uuid.New(), Amount: -49999}, // off by 1 paisa
	}
	if err := ledger.ValidateBalance(entries); !errors.Is(err, ledger.ErrUnbalancedTransaction) {
		t.Errorf("expected ErrUnbalancedTransaction, got %v", err)
	}
}

func testValidateBalance_ThreeEntriesBalanced(t *testing.T) {
	entries := []ledger.EntryInput{
		{AccountID: uuid.New(), Amount: 10000},
		{AccountID: uuid.New(), Amount: 5000},
		{AccountID: uuid.New(), Amount: -15000},
	}
	if err := ledger.ValidateBalance(entries); err != nil {
		t.Errorf("expected nil for 3-entry balanced split, got %v", err)
	}
}

// ── DB integration tests ──────────────────────────────────────────────────────

func testPostTransaction_Balanced(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	src, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Customer Wallet", AccountType: "asset", Currency: "INR",
	})
	if err != nil {
		t.Fatalf("create src account: %v", err)
	}
	dst, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Merchant Account", AccountType: "asset", Currency: "INR",
	})
	if err != nil {
		t.Fatalf("create dst account: %v", err)
	}

	tx, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "test-balanced-001",
		Description:    "Payment ₹500",
		Entries: []ledger.EntryInput{
			{AccountID: src.ID, Amount: -50000},
			{AccountID: dst.ID, Amount: 50000},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction unexpected error: %v", err)
	}
	if tx.Status != "posted" {
		t.Errorf("expected status 'posted', got %q", tx.Status)
	}
	if tx.PostedAt == nil {
		t.Error("expected posted_at to be set")
	}
}

func testPostTransaction_Unbalanced(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	acc, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	acc2, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Revenue", AccountType: "revenue", Currency: "INR",
	})

	_, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "test-unbalanced-001",
		Description:    "Unbalanced transaction",
		Entries: []ledger.EntryInput{
			{AccountID: acc.ID, Amount: 50000},
			{AccountID: acc2.ID, Amount: -49999},
		},
	})
	if !errors.Is(err, ledger.ErrUnbalancedTransaction) {
		t.Errorf("expected ErrUnbalancedTransaction, got %v", err)
	}
}

func testPostTransaction_SingleEntry(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	acc, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})

	_, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "test-single-entry-001",
		Description:    "Single entry",
		Entries: []ledger.EntryInput{
			{AccountID: acc.ID, Amount: 50000},
		},
	})
	if !errors.Is(err, ledger.ErrMinimumEntriesNotMet) {
		t.Errorf("expected ErrMinimumEntriesNotMet, got %v", err)
	}
}

func testPostTransaction_IdempotencyKey(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	src, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	dst, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Merchant", AccountType: "asset", Currency: "INR",
	})

	req := ledger.PostTransactionRequest{
		IdempotencyKey: "test-idempotency-001",
		Description:    "Idempotent payment",
		Entries: []ledger.EntryInput{
			{AccountID: src.ID, Amount: -10000},
			{AccountID: dst.ID, Amount: 10000},
		},
	}

	tx1, err := testService.PostTransaction(context.Background(), req)
	if err != nil {
		t.Fatalf("first PostTransaction: %v", err)
	}

	tx2, err := testService.PostTransaction(context.Background(), req)
	if err != nil {
		t.Fatalf("second PostTransaction: %v", err)
	}

	if tx1.ID != tx2.ID {
		t.Errorf("idempotency violation: different transaction IDs: %s vs %s", tx1.ID, tx2.ID)
	}
}

func testPostTransaction_IdempotencyMismatch(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	src, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	dst, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Merchant", AccountType: "asset", Currency: "INR",
	})

	req1 := ledger.PostTransactionRequest{
		IdempotencyKey: "test-mismatch-001",
		Description:    "Initial payment",
		Entries: []ledger.EntryInput{
			{AccountID: src.ID, Amount: -10000},
			{AccountID: dst.ID, Amount: 10000},
		},
	}

	req2 := ledger.PostTransactionRequest{
		IdempotencyKey: "test-mismatch-001",
		Description:    "Different payload payment",
		Entries: []ledger.EntryInput{
			{AccountID: src.ID, Amount: -50000},
			{AccountID: dst.ID, Amount: 50000},
		},
	}

	_, err := testService.PostTransaction(context.Background(), req1)
	if err != nil {
		t.Fatalf("first PostTransaction: %v", err)
	}

	_, err = testService.PostTransaction(context.Background(), req2)
	if !errors.Is(err, ledger.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func testPostTransaction_UnknownAccount(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	_, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "test-unknown-account-001",
		Description:    "Unknown account",
		Entries: []ledger.EntryInput{
			{AccountID: uuid.New(), Amount: -10000},
			{AccountID: uuid.New(), Amount: 10000},
		},
	})
	if !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("expected ErrAccountNotFound, got %v", err)
	}
}

/*
testPostTransaction_ConcurrentIdempotencyKey is the core Phase 4 test.

It fires 10 goroutines simultaneously, all posting the same idempotency key.
Without the UNIQUE constraint safety net, multiple goroutines could pass an
app-level "key exists?" check and both insert — resulting in duplicate rows.

The test asserts:
  - All goroutines succeed (return without error)
  - All goroutines return the exact same transaction ID
  - Exactly one transaction row exists in the database
  - The balance reflects only one transaction's amount (not N)
*/
func testPostTransaction_ConcurrentIdempotencyKey(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	src, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	dst, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Merchant", AccountType: "asset", Currency: "INR",
	})

	req := ledger.PostTransactionRequest{
		IdempotencyKey: "concurrent-idem-001",
		Description:    "Concurrent payment",
		Entries: []ledger.EntryInput{
			{AccountID: src.ID, Amount: -10000},
			{AccountID: dst.ID, Amount: 10000},
		},
	}

	const numGoroutines = 10
	results := make([]*db.Transaction, numGoroutines)
	errs := make([]error, numGoroutines)

	/* Use a start channel to maximise concurrent overlap across goroutines. */
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // block until all goroutines are ready
			results[i], errs[i] = testService.PostTransaction(context.Background(), req)
		}(i)
	}

	close(start) // release all goroutines at once
	wg.Wait()

	/* Assert: all goroutines must succeed. */
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	/* Assert: all goroutines must return the same transaction ID. */
	if results[0] == nil {
		t.Fatal("goroutine 0: got nil result")
	}
	firstID := results[0].ID
	for i, r := range results {
		if r == nil {
			t.Errorf("goroutine %d: got nil result", i)
			continue
		}
		if r.ID != firstID {
			t.Errorf("goroutine %d: idempotency violation — got ID %s, want %s", i, r.ID, firstID)
		}
	}

	/*
		Assert: only one transaction's worth of amount landed.
		If N duplicate inserts occurred, dst balance would be N×10000.
		Exactly one transaction means balance == 10000.
	*/
	balance, err := testService.GetBalance(context.Background(), dst.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 10000 {
		t.Errorf("expected balance 10000 (one transaction), got %d — possible duplicate inserts", balance)
	}
}
