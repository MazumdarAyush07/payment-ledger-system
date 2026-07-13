package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

/*
AccountHandler holds the ledger engine dependency for account-related endpoints.
It has no knowledge of the DB — it delegates all business logic to the engine.
*/
type AccountHandler struct {
	engine ledger.Engine
}

/* NewAccountHandler constructs an AccountHandler with the given engine. */
func NewAccountHandler(engine ledger.Engine) *AccountHandler {
	return &AccountHandler{engine: engine}
}

/*
CreateAccount handles POST /accounts.

Request body:
  {"name": "...", "account_type": "asset", "currency": "INR"}

Responses:
  201 Created  — account created successfully
  400 Bad Request — malformed JSON or missing required fields
  422 Unprocessable Entity — invalid account_type or currency
*/
func (h *AccountHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" || req.AccountType == "" || req.Currency == "" {
		writeError(w, http.StatusBadRequest, "name, account_type, and currency are required")
		return
	}

	acc, err := h.engine.CreateAccount(r.Context(), ledger.CreateAccountRequest{
		Name:        req.Name,
		AccountType: req.AccountType,
		Currency:    req.Currency,
	})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, accountToResponse(acc))
}

/*
GetBalance handles GET /accounts/{id}/balance.

Path parameter: id (UUID)

Responses:
  200 OK — {"account_id": "...", "balance": 150000, "currency": "INR"}
  400 Bad Request — id is not a valid UUID
  404 Not Found — account does not exist
*/
func (h *AccountHandler) GetBalance(w http.ResponseWriter, r *http.Request) {
	accountID, ok := parseUUID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	balance, err := h.engine.GetBalance(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve balance")
		return
	}

	/*
		Fetch the account to include currency in the response.
		If the account doesn't exist, GetBalance would have returned 0 above —
		we still want to surface a 404 for unknown account IDs.
	*/
	acc, err := h.engine.GetAccount(r.Context(), accountID)
	if err != nil {
		mapLedgerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, BalanceResponse{
		AccountID: accountID,
		Balance:   balance,
		Currency:  acc.Currency,
	})
}

/*
GetStatement handles GET /accounts/{id}/statement.

Query parameters:
  from — RFC 3339 timestamp (required)
  to   — RFC 3339 timestamp (required)

Responses:
  200 OK — statement with entries in chronological order
  400 Bad Request — missing/invalid from or to params, or id is not a valid UUID
*/
func (h *AccountHandler) GetStatement(w http.ResponseWriter, r *http.Request) {
	accountID, ok := parseUUID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		writeError(w, http.StatusBadRequest, "query parameters 'from' and 'to' are required (RFC 3339)")
		return
	}

	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "'from' must be a valid RFC 3339 timestamp")
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "'to' must be a valid RFC 3339 timestamp")
		return
	}
	if !to.After(from) {
		writeError(w, http.StatusBadRequest, "'to' must be after 'from'")
		return
	}

	entries, err := h.engine.GetStatement(r.Context(), accountID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve statement")
		return
	}

	writeJSON(w, http.StatusOK, StatementResponse{
		AccountID: accountID,
		From:      from,
		To:        to,
		Entries:   entriesToResponse(entries),
	})
}

// ── Shared helpers ────────────────────────────────────────────────────────────

/*
parseUUID parses a UUID string from a path parameter and writes a 400 response
if it is invalid. Returns the parsed UUID and true on success; false on failure.
*/
func parseUUID(w http.ResponseWriter, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a valid UUID")
		return uuid.UUID{}, false
	}
	return id, true
}

/*
mapLedgerError maps domain sentinel errors from the ledger package to the
appropriate HTTP status code. Falls back to 500 for unknown errors.
*/
func mapLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrAccountNotFound),
		errors.Is(err, ledger.ErrTransactionNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ledger.ErrUnbalancedTransaction),
		errors.Is(err, ledger.ErrMinimumEntriesNotMet),
		errors.Is(err, ledger.ErrCurrencyMismatch):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}
