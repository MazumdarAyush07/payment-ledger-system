package ledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/google/uuid"
)

/* TestGetTransaction is the single entry point for all GetTransaction tests. */
func TestGetTransaction(t *testing.T) {
	t.Run("ReturnsHeaderAndEntries", testGetTransaction_ReturnsHeaderAndEntries)
	t.Run("NotFound", testGetTransaction_NotFound)
	t.Run("EntryCountMatchesRequest", testGetTransaction_EntryCountMatchesRequest)
}

func testGetTransaction_ReturnsHeaderAndEntries(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	src, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Wallet", AccountType: "asset", Currency: "INR",
	})
	dst, _ := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name: "Merchant", AccountType: "asset", Currency: "INR",
	})

	posted, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "get-tx-001",
		Description:    "Test payment",
		Entries: []ledger.EntryInput{
			{AccountID: src.ID, Amount: -50000},
			{AccountID: dst.ID, Amount: 50000},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}

	detail, err := testService.GetTransaction(context.Background(), posted.ID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}

	/* Assert transaction header fields. */
	if detail.Transaction.ID != posted.ID {
		t.Errorf("transaction ID: got %s, want %s", detail.Transaction.ID, posted.ID)
	}
	if detail.Transaction.Status != "posted" {
		t.Errorf("status: got %q, want \"posted\"", detail.Transaction.Status)
	}
	if detail.Transaction.Description != "Test payment" {
		t.Errorf("description: got %q, want \"Test payment\"", detail.Transaction.Description)
	}

	/* Assert entries returned. */
	if len(detail.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(detail.Entries))
	}
}

func testGetTransaction_NotFound(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	_, err := testService.GetTransaction(context.Background(), uuid.New())
	if !errors.Is(err, ledger.ErrTransactionNotFound) {
		t.Errorf("expected ErrTransactionNotFound, got %v", err)
	}
}

func testGetTransaction_EntryCountMatchesRequest(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	accounts := make([]uuid.UUID, 4)
	for i := range accounts {
		acc, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
			Name:        "Account",
			AccountType: "asset",
			Currency:    "INR",
		})
		if err != nil {
			t.Fatalf("create account %d: %v", i, err)
		}
		accounts[i] = acc.ID
	}

	/* Four-way split: one credit, three debits — still sums to zero. */
	posted, err := testService.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		IdempotencyKey: "get-tx-split-001",
		Description:    "Four-way split",
		Entries: []ledger.EntryInput{
			{AccountID: accounts[0], Amount: -30000},
			{AccountID: accounts[1], Amount: 10000},
			{AccountID: accounts[2], Amount: 10000},
			{AccountID: accounts[3], Amount: 10000},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}

	detail, err := testService.GetTransaction(context.Background(), posted.ID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if len(detail.Entries) != 4 {
		t.Errorf("expected 4 entries, got %d", len(detail.Entries))
	}
}
