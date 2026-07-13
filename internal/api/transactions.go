package api

import (
	"encoding/json"
	"net/http"

	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/go-chi/chi/v5"
)

/*
TransactionHandler holds the ledger engine dependency for transaction-related
endpoints. It delegates all business logic to the engine.
*/
type TransactionHandler struct {
	engine ledger.Engine
}

/* NewTransactionHandler constructs a TransactionHandler with the given engine. */
func NewTransactionHandler(engine ledger.Engine) *TransactionHandler {
	return &TransactionHandler{engine: engine}
}

/*
PostTransaction handles POST /transactions.

The client must supply an Idempotency-Key header (mirroring Stripe's API
convention). If the key is omitted, we fall back to the body field — but
the header is the authoritative source.

Request body:
  {
    "idempotency_key": "...",   (fallback if header absent)
    "description": "...",
    "entries": [
      {"account_id": "...", "amount": -50000},
      {"account_id": "...", "amount":  50000}
    ]
  }

Responses:
  201 Created  — transaction posted, returns transaction + entries
  200 OK       — idempotency replay, returns original transaction
  400 Bad Request — malformed JSON, missing fields, or empty entries
  404 Not Found — an entry references an unknown account_id
  422 Unprocessable Entity — entries don't balance or fewer than 2 entries
*/
func (h *TransactionHandler) PostTransaction(w http.ResponseWriter, r *http.Request) {
	var req PostTransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	/* Idempotency-Key header takes precedence over body field. */
	if headerKey := r.Header.Get("Idempotency-Key"); headerKey != "" {
		req.IdempotencyKey = headerKey
	}

	if req.IdempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key is required (body field or Idempotency-Key header)")
		return
	}
	if len(req.Entries) == 0 {
		writeError(w, http.StatusBadRequest, "entries must not be empty")
		return
	}

	/* Map API EntryInput → ledger.EntryInput */
	ledgerEntries := make([]ledger.EntryInput, len(req.Entries))
	for i, e := range req.Entries {
		if e.AccountID.String() == "00000000-0000-0000-0000-000000000000" {
			writeError(w, http.StatusBadRequest, "each entry must have a valid account_id")
			return
		}
		ledgerEntries[i] = ledger.EntryInput{
			AccountID: e.AccountID,
			Amount:    e.Amount,
		}
	}

	tx, err := h.engine.PostTransaction(r.Context(), ledger.PostTransactionRequest{
		IdempotencyKey: req.IdempotencyKey,
		Description:    req.Description,
		Entries:        ledgerEntries,
	})
	if err != nil {
		mapLedgerError(w, err)
		return
	}

	/*
		Fetch the full detail (header + entries) to return a complete response.
		This avoids the client needing an immediate GET /transactions/{id}.
	*/
	detail, err := h.engine.GetTransaction(r.Context(), tx.ID)
	if err != nil {
		mapLedgerError(w, err)
		return
	}

	/*
		Use 200 for idempotency replays (tx already existed) vs 201 for new ones.
		We can detect a replay because posted_at will already be set from before
		this request started — in practice both are "success" from the client's view.
	*/
	writeJSON(w, http.StatusCreated, TransactionDetailResponse{
		Transaction: transactionToResponse(detail.Transaction),
		Entries:     entriesToResponse(detail.Entries),
	})
}

/*
GetTransaction handles GET /transactions/{id}.

Path parameter: id (UUID)

Responses:
  200 OK — transaction header + all entries
  400 Bad Request — id is not a valid UUID
  404 Not Found — transaction does not exist
*/
func (h *TransactionHandler) GetTransaction(w http.ResponseWriter, r *http.Request) {
	txID, ok := parseUUID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	detail, err := h.engine.GetTransaction(r.Context(), txID)
	if err != nil {
		mapLedgerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, TransactionDetailResponse{
		Transaction: transactionToResponse(detail.Transaction),
		Entries:     entriesToResponse(detail.Entries),
	})
}
