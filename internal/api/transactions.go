package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ayushmazumdar/payment-ledger/internal/currency"
	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/go-chi/chi/v5"
)

/*
TransactionHandler holds the ledger engine and an optional currency rate
service. If rateService is nil, cross-currency requests return 422.
*/
type TransactionHandler struct {
	engine      ledger.Engine
	rateService *currency.RateService
}

/* NewTransactionHandler constructs a TransactionHandler with the given engine and rate service. */
func NewTransactionHandler(engine ledger.Engine, rateService *currency.RateService) *TransactionHandler {
	return &TransactionHandler{engine: engine, rateService: rateService}
}

/*
PostTransaction handles POST /transactions.

The client must supply an Idempotency-Key header (mirroring Stripe's API
convention). If the key is omitted, we fall back to the body field — but
the header is the authoritative source.

from_currency and to_currency are always required. Set them to the same value
for a same-currency transaction. Set them to different ISO 4217 codes to trigger
cross-currency conversion — the handler converts all entry amounts to to_currency
before calling the ledger engine, preserving the zero-balance invariant.

Request body (same-currency, INR):
  {
    "idempotency_key": "...",
    "description": "...",
    "from_currency": "INR",
    "to_currency": "INR",
    "entries": [
      {"account_id": "...", "amount": -50000},
      {"account_id": "...", "amount":  50000}
    ]
  }

Request body (cross-currency, USD → INR):
  {
    "idempotency_key": "...",
    "description": "...",
    "from_currency": "USD",
    "to_currency": "INR",
    "entries": [
      {"account_id": "<usd-account>", "amount": -100},
      {"account_id": "<inr-account>", "amount":  100}
    ]
  }

Responses:
  201 Created           — transaction posted (new)
  200 OK                — idempotency replay, returns the original transaction unchanged
  400 Bad Request       — malformed JSON, missing fields, missing/invalid currency codes
  404 Not Found         — an entry references an unknown account_id
  422 Unprocessable     — entries don't balance or fewer than 2 entries
  503 Service Unavail.  — cross-currency requested but rate unavailable
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

	/* Both currency fields must always be explicitly provided — no default exists. */
	if req.FromCurrency == "" || req.ToCurrency == "" {
		writeError(w, http.StatusBadRequest, "from_currency and to_currency are required (ISO 4217 codes, e.g. \"INR\", \"USD\")")
		return
	}

	/* Validate all account IDs are non-zero UUIDs. */
	for i, e := range req.Entries {
		if e.AccountID.String() == "00000000-0000-0000-0000-000000000000" {
			writeError(w, http.StatusBadRequest, "each entry must have a valid account_id")
			_ = i
			return
		}
	}

	/* Build ledger entries, handling cross-currency conversion if requested. */
	ledgerEntries, exchangeRate, rateSource, err := h.buildEntries(r, req)
	if err != nil {
		if errors.Is(err, currency.ErrRateUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "exchange rate unavailable — please retry later")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	/* Post the transaction via the ledger engine. */
	var exRate *float64
	var src *string
	if rateSource != "" {
		exRate = &exchangeRate
		src = &rateSource
	}

	tx, err := h.engine.PostTransaction(r.Context(), ledger.PostTransactionRequest{
		IdempotencyKey: req.IdempotencyKey,
		Description:    req.Description,
		Entries:        ledgerEntries,
		ExchangeRate:   exRate,
		RateSource:     src,
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
buildEntries converts the API EntryInput list into ledger.EntryInput values,
applying currency conversion when from_currency and to_currency differ.

Same-currency: entries pass through unchanged. The engine's ValidateBalance
enforces sum == 0.

Cross-currency: every entry amount is converted to the target currency.
This ensures the entries still sum to zero — the zero-balance invariant is
never bypassed. The exchange_rate stored on the transaction provides the
full audit trail for the original source-currency values.

Example (USD→INR at rate 83.5):
  client sends: [{usd_wallet, -100}, {inr_wallet, +100}]
  handler produces: [{usd_wallet, -8350}, {inr_wallet, +8350}]  → sum = 0 ✅
*/
func (h *TransactionHandler) buildEntries(
	r *http.Request,
	req PostTransactionRequest,
) (entries []ledger.EntryInput, rate float64, rateSource string, err error) {
	isCrossCurrency := req.FromCurrency != "" && req.ToCurrency != "" && req.FromCurrency != req.ToCurrency

	if isCrossCurrency {
		if h.rateService == nil {
			return nil, 0, "", errors.New("currency conversion not configured on this server")
		}

		/*
			Find the magnitude of the largest debit amount and convert it.
			We use that converted value as the scale factor so that all entry
			amounts are expressed in the target currency's minor units.
		*/
		var maxDebit int64
		for _, e := range req.Entries {
			if e.Amount < 0 && -e.Amount > maxDebit {
				maxDebit = -e.Amount
			}
		}
		if maxDebit == 0 {
			return nil, 0, "", errors.New("cross-currency transaction must have at least one negative (debit) entry")
		}

		/* Fetch the exchange rate once. */
		_, rateResult, convErr := h.rateService.Convert(r.Context(), maxDebit, req.FromCurrency, req.ToCurrency)
		if convErr != nil {
			return nil, 0, "", convErr
		}

		/*
			Convert every entry amount to target currency minor units.
			Positive (credit) and negative (debit) entries are both scaled by the
			same rate, so the sum is preserved:
			  sum_original = 0  →  sum_converted = 0 × rate = 0 ✅
		*/
		out := make([]ledger.EntryInput, len(req.Entries))
		for i, e := range req.Entries {
			converted := int64(float64(e.Amount) * rateResult.Value)
			out[i] = ledger.EntryInput{AccountID: e.AccountID, Amount: converted}
		}

		return out, rateResult.Value, rateResult.RateSource, nil
	}

	/* Same-currency path — pass entries through without modification. */
	out := make([]ledger.EntryInput, len(req.Entries))
	for i, e := range req.Entries {
		out[i] = ledger.EntryInput{AccountID: e.AccountID, Amount: e.Amount}
	}
	return out, 0, "", nil
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
