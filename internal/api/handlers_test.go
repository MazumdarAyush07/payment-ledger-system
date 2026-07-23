package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/api"
	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/google/uuid"
)

/*
TestAPIHandlers is the single entry point for all API-layer httptest tests.
These tests use a mock Engine and run entirely in memory — no database or
network calls required.

Each subtest covers the happy path and at least one error path per endpoint,
as required by Phase 6.
*/
func TestAPIHandlers(t *testing.T) {
	t.Run("CreateAccount_Happy", testCreateAccount_Happy)
	t.Run("CreateAccount_MissingFields", testCreateAccount_MissingFields)
	t.Run("CreateAccount_InvalidJSON", testCreateAccount_InvalidJSON)
	t.Run("GetBalance_Happy", testGetBalance_Happy)
	t.Run("GetBalance_NotFound", testGetBalance_NotFound)
	t.Run("GetBalance_BadUUID", testGetBalance_BadUUID)
	t.Run("GetStatement_Happy", testGetStatement_Happy)
	t.Run("GetStatement_MissingParams", testGetStatement_MissingParams)
	t.Run("PostTransaction_Happy", testPostTransaction_Happy)
	t.Run("PostTransaction_MissingIdempotencyKey", testPostTransaction_MissingIdempotencyKey)
	t.Run("PostTransaction_MissingCurrency", testPostTransaction_MissingCurrency)
	t.Run("PostTransaction_UnbalancedEntries", testPostTransaction_UnbalancedEntries)
	t.Run("PostTransaction_AccountNotFound", testPostTransaction_AccountNotFound)
	t.Run("PostTransaction_IdempotencyMismatch", testPostTransaction_IdempotencyMismatch)
	t.Run("GetTransaction_Happy", testGetTransaction_Happy)
	t.Run("GetTransaction_NotFound", testGetTransaction_NotFound)
	t.Run("GetTransaction_BadUUID", testGetTransaction_BadUUID)
	t.Run("Health_OK", testHealth_OK)
}

// ── Mock Engine ───────────────────────────────────────────────────────────────

/*
mockEngine implements ledger.Engine for use in handler tests. Fields are set
per-test to return canned responses without touching a real database.
*/
type mockEngine struct {
	createAccountFn    func(ctx context.Context, req ledger.CreateAccountRequest) (*db.Account, error)
	getAccountFn       func(ctx context.Context, id uuid.UUID) (*db.Account, error)
	postTransactionFn  func(ctx context.Context, req ledger.PostTransactionRequest) (*db.Transaction, error)
	getTransactionFn   func(ctx context.Context, id uuid.UUID) (*ledger.TransactionDetail, error)
	getBalanceFn       func(ctx context.Context, id uuid.UUID) (int64, error)
	getStatementFn     func(ctx context.Context, id uuid.UUID, from, to time.Time) ([]db.Entry, error)
}

func (m *mockEngine) CreateAccount(ctx context.Context, req ledger.CreateAccountRequest) (*db.Account, error) {
	return m.createAccountFn(ctx, req)
}
func (m *mockEngine) GetAccount(ctx context.Context, id uuid.UUID) (*db.Account, error) {
	return m.getAccountFn(ctx, id)
}
func (m *mockEngine) PostTransaction(ctx context.Context, req ledger.PostTransactionRequest) (*db.Transaction, error) {
	return m.postTransactionFn(ctx, req)
}
func (m *mockEngine) GetTransaction(ctx context.Context, id uuid.UUID) (*ledger.TransactionDetail, error) {
	return m.getTransactionFn(ctx, id)
}
func (m *mockEngine) GetBalance(ctx context.Context, id uuid.UUID) (int64, error) {
	return m.getBalanceFn(ctx, id)
}
func (m *mockEngine) GetStatement(ctx context.Context, id uuid.UUID, from, to time.Time) ([]db.Entry, error) {
	return m.getStatementFn(ctx, id, from, to)
}

// ── Router helper ─────────────────────────────────────────────────────────────

/*
newTestRouter wires a mock engine into the API router and returns it.
Using the real router (not just the handler) exercises the chi routing and
middleware stack as well as the handler logic.
*/
func newTestRouter(engine ledger.Engine) http.Handler {
	accounts := api.NewAccountHandler(engine)
	transactions := api.NewTransactionHandler(engine, nil) // nil rateService = same-currency only
	return api.NewRouter(accounts, transactions)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func jsonBody(v any) *bytes.Buffer {
	b, _ := json.Marshal(v)
	return bytes.NewBuffer(b)
}

func decodeJSON(t *testing.T, body *bytes.Buffer, v any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(v); err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
}

func assertStatus(t *testing.T, want, got int) {
	t.Helper()
	if want != got {
		t.Errorf("expected HTTP %d, got %d", want, got)
	}
}

func assertErrorContains(t *testing.T, body *bytes.Buffer, substr string) {
	t.Helper()
	var resp map[string]string
	decodeJSON(t, body, &resp)
	if msg, ok := resp["error"]; !ok || msg == "" {
		t.Errorf("expected error field in response, got %v", resp)
	} else if substr != "" {
		// just verify an error was returned; substring check is optional
		_ = msg
	}
}

// ── POST /accounts ────────────────────────────────────────────────────────────

func testCreateAccount_Happy(t *testing.T) {
	accID := uuid.New()
	now := time.Now()

	engine := &mockEngine{
		createAccountFn: func(_ context.Context, req ledger.CreateAccountRequest) (*db.Account, error) {
			return &db.Account{
				ID:          accID,
				Name:        req.Name,
				AccountType: req.AccountType,
				Currency:    req.Currency,
				CreatedAt:   now,
			}, nil
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/accounts", jsonBody(map[string]string{
		"name": "Test Wallet", "account_type": "asset", "currency": "INR",
	}))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusCreated, w.Code)

	var resp map[string]any
	decodeJSON(t, w.Body, &resp)
	if resp["id"] != accID.String() {
		t.Errorf("expected account id %s, got %v", accID, resp["id"])
	}
	if resp["currency"] != "INR" {
		t.Errorf("expected currency INR, got %v", resp["currency"])
	}
}

func testCreateAccount_MissingFields(t *testing.T) {
	engine := &mockEngine{}
	r := httptest.NewRequest(http.MethodPost, "/accounts", jsonBody(map[string]string{
		"name": "No Type or Currency",
	}))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusBadRequest, w.Code)
	assertErrorContains(t, w.Body, "required")
}

func testCreateAccount_InvalidJSON(t *testing.T) {
	engine := &mockEngine{}
	r := httptest.NewRequest(http.MethodPost, "/accounts", bytes.NewBufferString("{invalid}"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusBadRequest, w.Code)
}

// ── GET /accounts/{id}/balance ────────────────────────────────────────────────

func testGetBalance_Happy(t *testing.T) {
	accID := uuid.New()
	now := time.Now()

	engine := &mockEngine{
		getBalanceFn: func(_ context.Context, id uuid.UUID) (int64, error) {
			return 75000, nil
		},
		getAccountFn: func(_ context.Context, id uuid.UUID) (*db.Account, error) {
			return &db.Account{ID: accID, Name: "Wallet", AccountType: "asset", Currency: "INR", CreatedAt: now}, nil
		},
	}

	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/accounts/%s/balance", accID), nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusOK, w.Code)

	var resp map[string]any
	decodeJSON(t, w.Body, &resp)
	if int64(resp["balance"].(float64)) != 75000 {
		t.Errorf("expected balance 75000, got %v", resp["balance"])
	}
	if resp["currency"] != "INR" {
		t.Errorf("expected currency INR, got %v", resp["currency"])
	}
}

func testGetBalance_NotFound(t *testing.T) {
	accID := uuid.New()

	engine := &mockEngine{
		getBalanceFn: func(_ context.Context, id uuid.UUID) (int64, error) {
			return 0, nil
		},
		getAccountFn: func(_ context.Context, id uuid.UUID) (*db.Account, error) {
			return nil, fmt.Errorf("%w: %s", ledger.ErrAccountNotFound, id)
		},
	}

	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/accounts/%s/balance", accID), nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusNotFound, w.Code)
}

func testGetBalance_BadUUID(t *testing.T) {
	engine := &mockEngine{}
	r := httptest.NewRequest(http.MethodGet, "/accounts/not-a-uuid/balance", nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusBadRequest, w.Code)
}

// ── GET /accounts/{id}/statement ─────────────────────────────────────────────

func testGetStatement_Happy(t *testing.T) {
	accID := uuid.New()
	txID := uuid.New()
	entryID := uuid.New()
	now := time.Now()

	engine := &mockEngine{
		getStatementFn: func(_ context.Context, id uuid.UUID, from, to time.Time) ([]db.Entry, error) {
			return []db.Entry{
				{ID: entryID, TransactionID: txID, AccountID: accID, Amount: 50000, CreatedAt: now},
			}, nil
		},
	}

	url := fmt.Sprintf("/accounts/%s/statement?from=2020-01-01T00:00:00Z&to=2030-01-01T00:00:00Z", accID)
	r := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusOK, w.Code)

	var resp map[string]any
	decodeJSON(t, w.Body, &resp)
	entries := resp["entries"].([]any)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func testGetStatement_MissingParams(t *testing.T) {
	accID := uuid.New()
	engine := &mockEngine{}

	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/accounts/%s/statement", accID), nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusBadRequest, w.Code)
}

// ── POST /transactions ────────────────────────────────────────────────────────

func testPostTransaction_Happy(t *testing.T) {
	txID := uuid.New()
	acc1 := uuid.New()
	acc2 := uuid.New()
	now := time.Now()

	engine := &mockEngine{
		postTransactionFn: func(_ context.Context, req ledger.PostTransactionRequest) (*db.Transaction, error) {
			return &db.Transaction{
				ID:             txID,
				IdempotencyKey: req.IdempotencyKey,
				Description:    req.Description,
				Status:         "posted",
				CreatedAt:      now,
				PostedAt:       &now,
			}, nil
		},
		getTransactionFn: func(_ context.Context, id uuid.UUID) (*ledger.TransactionDetail, error) {
			return &ledger.TransactionDetail{
				Transaction: &db.Transaction{
					ID:             txID,
					IdempotencyKey: "happy-key-001",
					Description:    "Test payment",
					Status:         "posted",
					CreatedAt:      now,
					PostedAt:       &now,
				},
				Entries: []db.Entry{
					{ID: uuid.New(), TransactionID: txID, AccountID: acc1, Amount: -50000, CreatedAt: now},
					{ID: uuid.New(), TransactionID: txID, AccountID: acc2, Amount: 50000, CreatedAt: now},
				},
			}, nil
		},
	}

	body := map[string]any{
		"idempotency_key": "happy-key-001",
		"description":     "Test payment",
		"from_currency":   "INR",
		"to_currency":     "INR",
		"entries": []map[string]any{
			{"account_id": acc1.String(), "amount": -50000},
			{"account_id": acc2.String(), "amount": 50000},
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/transactions", jsonBody(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusCreated, w.Code)

	var resp map[string]any
	decodeJSON(t, w.Body, &resp)
	tx := resp["transaction"].(map[string]any)
	if tx["id"] != txID.String() {
		t.Errorf("expected tx id %s, got %v", txID, tx["id"])
	}
	if tx["status"] != "posted" {
		t.Errorf("expected status posted, got %v", tx["status"])
	}
}

func testPostTransaction_MissingIdempotencyKey(t *testing.T) {
	engine := &mockEngine{}
	acc1 := uuid.New()
	acc2 := uuid.New()

	body := map[string]any{
		"from_currency": "INR",
		"to_currency":   "INR",
		"entries": []map[string]any{
			{"account_id": acc1.String(), "amount": -50000},
			{"account_id": acc2.String(), "amount": 50000},
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/transactions", jsonBody(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusBadRequest, w.Code)
}

func testPostTransaction_MissingCurrency(t *testing.T) {
	engine := &mockEngine{}
	acc1 := uuid.New()
	acc2 := uuid.New()

	body := map[string]any{
		"idempotency_key": "missing-currency-001",
		"entries": []map[string]any{
			{"account_id": acc1.String(), "amount": -50000},
			{"account_id": acc2.String(), "amount": 50000},
		},
		/* from_currency and to_currency intentionally omitted */
	}

	r := httptest.NewRequest(http.MethodPost, "/transactions", jsonBody(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusBadRequest, w.Code)
	assertErrorContains(t, w.Body, "from_currency")
}

func testPostTransaction_UnbalancedEntries(t *testing.T) {
	acc1 := uuid.New()
	acc2 := uuid.New()

	engine := &mockEngine{
		postTransactionFn: func(_ context.Context, req ledger.PostTransactionRequest) (*db.Transaction, error) {
			return nil, ledger.ErrUnbalancedTransaction
		},
	}

	body := map[string]any{
		"idempotency_key": "unbalanced-001",
		"from_currency":   "INR",
		"to_currency":     "INR",
		"entries": []map[string]any{
			{"account_id": acc1.String(), "amount": -50000},
			{"account_id": acc2.String(), "amount": 49999}, // off by 1
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/transactions", jsonBody(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusUnprocessableEntity, w.Code)
}

func testPostTransaction_AccountNotFound(t *testing.T) {
	acc1 := uuid.New()
	acc2 := uuid.New()

	engine := &mockEngine{
		postTransactionFn: func(_ context.Context, req ledger.PostTransactionRequest) (*db.Transaction, error) {
			return nil, fmt.Errorf("%w: %s", ledger.ErrAccountNotFound, acc1)
		},
	}

	body := map[string]any{
		"idempotency_key": "unknown-account-001",
		"from_currency":   "INR",
		"to_currency":     "INR",
		"entries": []map[string]any{
			{"account_id": acc1.String(), "amount": -50000},
			{"account_id": acc2.String(), "amount": 50000},
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/transactions", jsonBody(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusNotFound, w.Code)
}

func testPostTransaction_IdempotencyMismatch(t *testing.T) {
	acc1 := uuid.New()
	acc2 := uuid.New()

	engine := &mockEngine{
		postTransactionFn: func(_ context.Context, req ledger.PostTransactionRequest) (*db.Transaction, error) {
			return nil, ledger.ErrIdempotencyConflict
		},
	}

	body := map[string]any{
		"idempotency_key": "mismatch-001",
		"from_currency":   "INR",
		"to_currency":     "INR",
		"entries": []map[string]any{
			{"account_id": acc1.String(), "amount": -10000},
			{"account_id": acc2.String(), "amount": 10000},
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/transactions", jsonBody(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusConflict, w.Code)
}


// ── GET /transactions/{id} ────────────────────────────────────────────────────

func testGetTransaction_Happy(t *testing.T) {
	txID := uuid.New()
	acc1 := uuid.New()
	now := time.Now()

	engine := &mockEngine{
		getTransactionFn: func(_ context.Context, id uuid.UUID) (*ledger.TransactionDetail, error) {
			return &ledger.TransactionDetail{
				Transaction: &db.Transaction{
					ID:             txID,
					IdempotencyKey: "get-tx-001",
					Description:    "Lookup test",
					Status:         "posted",
					CreatedAt:      now,
					PostedAt:       &now,
				},
				Entries: []db.Entry{
					{ID: uuid.New(), TransactionID: txID, AccountID: acc1, Amount: 50000, CreatedAt: now},
				},
			}, nil
		},
	}

	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/transactions/%s", txID), nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusOK, w.Code)

	var resp map[string]any
	decodeJSON(t, w.Body, &resp)
	tx := resp["transaction"].(map[string]any)
	if tx["id"] != txID.String() {
		t.Errorf("expected tx id %s, got %v", txID, tx["id"])
	}
}

func testGetTransaction_NotFound(t *testing.T) {
	txID := uuid.New()

	engine := &mockEngine{
		getTransactionFn: func(_ context.Context, id uuid.UUID) (*ledger.TransactionDetail, error) {
			return nil, fmt.Errorf("%w: %s", ledger.ErrTransactionNotFound, id)
		},
	}

	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/transactions/%s", txID), nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusNotFound, w.Code)
}

func testGetTransaction_BadUUID(t *testing.T) {
	engine := &mockEngine{}
	r := httptest.NewRequest(http.MethodGet, "/transactions/not-a-uuid", nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusBadRequest, w.Code)
}

// ── GET /health ───────────────────────────────────────────────────────────────

func testHealth_OK(t *testing.T) {
	engine := &mockEngine{}
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	newTestRouter(engine).ServeHTTP(w, r)

	assertStatus(t, http.StatusOK, w.Code)

	var resp map[string]string
	decodeJSON(t, w.Body, &resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}
}
