package ledger_test

import (
	"context"
	"testing"

	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/google/uuid"
)

// TestGetBalance is the single entry point for all GetBalance tests.
func TestGetBalance(t *testing.T) {
	t.Run("NoEntries", testGetBalance_NoEntries)
	t.Run("CorrectSum", testGetBalance_CorrectSum)
	t.Run("MultipleTransactions", testGetBalance_MultipleTransactions)
	t.Run("UnknownAccount", testGetBalance_UnknownAccount)
}

func testGetBalance_NoEntries(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	acc, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Empty Wallet", AccountType: "asset", Currency: "INR",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	balance, err := testService.GetBalance(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 0 {
		t.Errorf("expected 0 for new account, got %d", balance)
	}
}

func testGetBalance_CorrectSum(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	wallet, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Customer Wallet", AccountType: "asset", Currency: "INR",
	})
	revenue, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Revenue", AccountType: "revenue", Currency: "INR",
	})

	_, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "balance-test-001",
		Description:    "Top-up ₹1000",
		Entries: []ledger.EntryInput{
			{AccountID: wallet.ID, Amount: 100000},
			{AccountID: revenue.ID, Amount: -100000},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}

	balance, err := testService.GetBalance(context.Background(), wallet.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 100000 {
		t.Errorf("expected balance 100000, got %d", balance)
	}
}

func testGetBalance_MultipleTransactions(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	wallet, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	revenue, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Revenue", AccountType: "revenue", Currency: "INR",
	})

	// Top-up ₹2000
	testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "balance-multi-001",
		Entries: []ledger.EntryInput{
			{AccountID: wallet.ID, Amount: 200000},
			{AccountID: revenue.ID, Amount: -200000},
		},
	})

	// Spend ₹500
	testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "balance-multi-002",
		Entries: []ledger.EntryInput{
			{AccountID: wallet.ID, Amount: -50000},
			{AccountID: revenue.ID, Amount: 50000},
		},
	})

	// Expected: 200000 - 50000 = 150000
	balance, err := testService.GetBalance(context.Background(), wallet.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 150000 {
		t.Errorf("expected balance 150000, got %d", balance)
	}
}

func testGetBalance_UnknownAccount(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	balance, err := testService.GetBalance(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetBalance for unknown account: unexpected error: %v", err)
	}
	if balance != 0 {
		t.Errorf("expected 0 for unknown account, got %d", balance)
	}
}
