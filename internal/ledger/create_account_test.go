package ledger_test

import (
	"context"
	"testing"

	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
)

// TestCreateAccount is the single entry point for all CreateAccount tests.
// Subtests are called via t.Run so the output is grouped and easy to read.
func TestCreateAccount(t *testing.T) {
	t.Run("ValidAccountTypes", testCreateAccount_Valid)
	t.Run("InvalidAccountType", testCreateAccount_InvalidAccountType)
	t.Run("EmptyName", testCreateAccount_EmptyName)
	t.Run("InvalidCurrency", testCreateAccount_InvalidCurrency)
}

func testCreateAccount_Valid(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	cases := []struct {
		name        string
		accountType string
		currency    string
	}{
		{"Wallet", "asset", "INR"},
		{"Revenue Account", "revenue", "USD"},
		{"Expense Tracker", "expense", "INR"},
		{"Merchant Liability", "liability", "USD"},
		{"Equity Pool", "equity", "INR"},
	}

	for _, tc := range cases {
		t.Run(tc.accountType, func(t *testing.T) {
			acc, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
				Name:        tc.name,
				AccountType: tc.accountType,
				Currency:    tc.currency,
			})
			if err != nil {
				t.Fatalf("CreateAccount(%q) unexpected error: %v", tc.accountType, err)
			}
			if acc.ID.String() == "" {
				t.Error("expected non-empty UUID")
			}
			if acc.AccountType != tc.accountType {
				t.Errorf("account_type: got %q, want %q", acc.AccountType, tc.accountType)
			}
			if acc.Currency != tc.currency {
				t.Errorf("currency: got %q, want %q", acc.Currency, tc.currency)
			}
		})
	}
}

func testCreateAccount_InvalidAccountType(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	_, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name:        "Bad Account",
		AccountType: "savings", // not in our enum
		Currency:    "INR",
	})
	if err == nil {
		t.Fatal("expected error for invalid account_type, got nil")
	}
}

func testCreateAccount_EmptyName(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	_, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name:        "",
		AccountType: "asset",
		Currency:    "INR",
	})
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

func testCreateAccount_InvalidCurrency(t *testing.T) {
	requireDB(t)
	cleanDB(t)

	_, err := testService.CreateAccount(context.Background(), ledger.CreateAccountRequest{
		Name:        "Bad Currency",
		AccountType: "asset",
		Currency:    "RUPEE", // must be 3 chars
	})
	if err == nil {
		t.Fatal("expected error for invalid currency, got nil")
	}
}
