package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
)

// TestGetStatement is the single entry point for all GetStatement tests.
func TestGetStatement(t *testing.T) {
	t.Run("EntriesWithinRange", testGetStatement_EntriesWithinRange)
	t.Run("EntriesOutsideRange", testGetStatement_EntriesOutsideRange)
	t.Run("EmptyAccount", testGetStatement_EmptyAccount)
	t.Run("MultipleTransactionsOrdering", testGetStatement_MultipleTransactionsOrdering)
}

func testGetStatement_EntriesWithinRange(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	wallet, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	revenue, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Revenue", AccountType: "revenue", Currency: "INR",
	})

	_, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "stmt-test-001",
		Description:    "Top-up",
		Entries: []ledger.EntryInput{
			{AccountID: wallet.ID, Amount: 100000},
			{AccountID: revenue.ID, Amount: -100000},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}

	from := time.Now().UTC().Add(-1 * time.Minute)
	to := time.Now().UTC().Add(1 * time.Minute)

	entries, err := testService.GetStatement(context.Background(), wallet.ID, from, to)
	if err != nil {
		t.Fatalf("GetStatement: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Amount != 100000 {
		t.Errorf("expected amount 100000, got %d", entries[0].Amount)
	}
}

func testGetStatement_EntriesOutsideRange(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	wallet, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	revenue, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Revenue", AccountType: "revenue", Currency: "INR",
	})

	testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "stmt-range-001",
		Entries: []ledger.EntryInput{
			{AccountID: wallet.ID, Amount: 100000},
			{AccountID: revenue.ID, Amount: -100000},
		},
	})

	// Query a range entirely in the past — nothing should be returned
	from := time.Now().UTC().Add(-48 * time.Hour)
	to := time.Now().UTC().Add(-24 * time.Hour)

	entries, err := testService.GetStatement(context.Background(), wallet.ID, from, to)
	if err != nil {
		t.Fatalf("GetStatement: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries outside range, got %d", len(entries))
	}
}

func testGetStatement_EmptyAccount(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	wallet, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Empty Wallet", AccountType: "asset", Currency: "INR",
	})

	entries, err := testService.GetStatement(context.Background(), wallet.ID,
		time.Now().UTC().Add(-1*time.Hour),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("GetStatement: %v", err)
	}
	if entries == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func testGetStatement_MultipleTransactionsOrdering(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	wallet, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	revenue, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Revenue", AccountType: "revenue", Currency: "INR",
	})

	for i, key := range []string{"order-001", "order-002", "order-003"} {
		amount := int64((i + 1) * 10000) // 10000, 20000, 30000
		testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
			IdempotencyKey: key,
			Entries: []ledger.EntryInput{
				{AccountID: wallet.ID, Amount: amount},
				{AccountID: revenue.ID, Amount: -amount},
			},
		})
	}

	entries, err := testService.GetStatement(context.Background(), wallet.ID,
		time.Now().UTC().Add(-1*time.Minute),
		time.Now().UTC().Add(1*time.Minute),
	)
	if err != nil {
		t.Fatalf("GetStatement: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	expectedAmounts := []int64{10000, 20000, 30000}
	for i, e := range entries {
		if e.Amount != expectedAmounts[i] {
			t.Errorf("entry[%d]: expected amount %d, got %d", i, expectedAmounts[i], e.Amount)
		}
	}
}
