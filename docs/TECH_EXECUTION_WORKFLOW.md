# Payment Ledger System (Go) — Tech Execution Workflow

**Type:** Learning project — Path B portfolio piece for backend/payments roles
**Goal:** Build a correct, idempotent, double-entry payment ledger backend in Go, deep enough to
speak to in interviews and demo live, shallow enough to actually finish in 2–3 weeks of
consistent evening work.

Use this file as the working checklist. Complete each phase in order and tick items as done.

## Rules of Execution
- Build in this sequence: **Data Contracts → DB Schema → Core Ledger Engine → Idempotency Layer →
  API → Tests/Invariants → Concurrency Hardening → Polish/Demo**.
- Every phase must have a clear "Done" outcome before moving forward.
- No API work until the ledger engine and DB schema are correct and tested in isolation.
- This is a learning project, not a product: prioritize understanding *why* each piece exists
  (why idempotency keys, why double-entry, why not just a `balance` column) over speed.
- Keep a `NOTES.md` alongside this file — for every phase, write 3–5 lines on what you learned
  and what confused you. This is what you'll actually talk about in interviews, not the code itself.
- Local/dev runs use `.env` (DB URL); no cloud deployment required for v1 — this is a portfolio
  piece, not a hosted product.

---

## Phase 0 — Project Setup
**Goal:** Reproducible local dev foundation in Go.

- [x] Create repo structure (`cmd/`, `internal/ledger/`, `internal/api/`, `internal/db/`, `migrations/`, `tests/`)
- [x] Initialize Go module (`go mod init github.com/<you>/payment-ledger`)
- [x] Add `.env.example` (`DATABASE_URL`)
- [x] Add Docker Compose for PostgreSQL
- [x] Pick and add a migration tool (`golang-migrate` or `goose` — either is fine, pick one and move on)
- [x] Add `README.md` stub explaining what the project is and why (you'll flesh this out in Phase 7)

**Deliverable:** `docker-compose up` gives you a running Postgres instance; `go run cmd/server/main.go` connects to it.

**Done when:** local app can connect to Postgres and run a migration.

> Status: ✅ Complete.

---

## Phase 1 — Data Contracts (Start Here)
**Goal:** Define exactly what a "ledger" means before writing any schema or code. This is the
phase people skip and regret — get the concepts right here, not while debugging a bug later.

- [x] Define core entities: `Account`, `Transaction`, `Entry` (a transaction has 2+ entries; entries are the actual debits/credits)
- [x] Write down, in plain English, the **double-entry invariant**: for every transaction, the sum
  of all entry amounts must equal zero (debits and credits balance)
- [x] Define `account_type` enum (e.g. `asset`, `liability`, `equity` — even a simplified 2–3 type
  set is fine for v1) and what "balance" means per type
- [x] Define what an **idempotency key** is and exactly where it's checked (at transaction creation,
  scoped per client/request, not per entry)
- [x] Define currency handling: store amounts as integer minor units (paise/cents), never floats —
  write down *why* in your own words
- [x] Define timestamp standard (UTC) and what "transaction time" vs "posted time" means

**Deliverable:** `docs/data_contracts.md` — a short doc, not a spec novel. 1–2 pages.

**Done when:** you can explain the double-entry invariant and idempotency model out loud without
looking at notes. If you can't, this phase isn't done — this is the part that actually matters
for interviews.

> Status: ✅ Complete.

---

## Phase 2 — PostgreSQL Schema & Migrations
**Goal:** Translate Phase 1 contracts into a normalized schema.

- [x] Create enums: `account_type` (`asset`, `liability`, `equity`, `revenue`, `expense`),
  `transaction_status` (`pending`, `posted`, `failed`), `rate_source` (`live`, `stale_cache`)
- [x] `accounts` table: `id` (UUID PK), `name`, `account_type`, `currency` (ISO 4217), `created_at` (UTC)
- [x] `transactions` table: `id` (UUID PK), `idempotency_key` (unique), `description`,
  `status` (`pending`/`posted`/`failed`), `exchange_rate` (nullable float8),
  `rate_source` (nullable enum), `created_at` (UTC), `posted_at` (nullable UTC)
- [x] `entries` table: `id` (UUID PK), `transaction_id` (FK), `account_id` (FK),
  `amount` (int8, signed minor units), `created_at` (UTC)
- [x] Unique constraint on `transactions.idempotency_key`
- [x] Index on `entries.account_id` (you'll query "all entries for account X" constantly)
- [x] Index on `entries.transaction_id`
- [x] Write the first migration and confirm it runs clean on an empty DB

**Deliverable:** migration files + a simple ERD (even a hand-drawn or text-based one is fine —
this isn't a deliverable anyone external sees).

**Done when:** migrations run clean on empty DB and match the Phase 1 contracts exactly. If
something doesn't fit the schema, go back and fix the contract — don't let schema drift from intent.

> Status: ✅ Complete. Migration `00001_init_schema.sql` ran in 13ms on Docker Postgres.

---

## Phase 3 — Core Ledger Engine (No API Yet)
**Goal:** Implement the actual ledger logic as a Go package you can unit test in isolation,
before any HTTP layer touches it. This is the heart of the project.

- [x] `internal/ledger/errors.go`: define domain-typed sentinel errors — no raw strings
  - `ErrUnbalancedTransaction` — entries don't sum to zero → HTTP 422
  - `ErrMinimumEntriesNotMet` — fewer than 2 entries provided → HTTP 422
  - `ErrAccountNotFound` — unknown `account_id` in an entry → HTTP 404
  - `ErrCurrencyMismatch` — entry currency doesn't match account currency → HTTP 422
- [x] `internal/ledger/engine.go`: define the `Engine` interface and `Service` struct that
  holds the DB pool — zero HTTP/JSON dependencies; all other files implement methods on this struct
- [x] `internal/ledger/create_account.go`: `CreateAccount(ctx, name, accountType, currency)`
  — required for test setup and for the API layer; validates account_type is a known enum value
- [x] `internal/ledger/post_transaction.go`: `PostTransaction(ctx, idempotencyKey, description,
  entries[], exchangeRate *float64, rateSource *string)` — validates double-entry invariant
  (sum = 0), inserts transaction + entries atomically inside a DB transaction
- [x] Validate invariants **before** any DB write — pure Go functions, fully unit-testable:
  - `ValidateBalance(entries)` — sum must equal zero
  - `ValidateMinEntries(entries)` — at least 2 entries required
- [x] `internal/ledger/get_balance.go`: `GetBalance(ctx, accountID)` — derives balance via
  `SELECT SUM(amount) FROM entries WHERE account_id = $1` (no stored balance column)
- [x] `internal/ledger/get_statement.go`: `GetStatement(ctx, accountID, from, to time.Time)` —
  returns entries filtered by `posted_at` range (not `created_at` — per data contracts,
  only fully committed entries appear on a statement)
- [x] Wrap `PostTransaction` in a DB transaction (`BEGIN`/`COMMIT`/`ROLLBACK`) — if any entry
  insert fails, the entire transaction rolls back and nothing lands in the DB

**Unit Tests — one `_test.go` per source file:**
- [x] `internal/ledger/create_account_test.go`: valid account creation; reject unknown account_type
- [x] `internal/ledger/post_transaction_test.go`:
  - Balanced entries → transaction + entries land in DB
  - Unbalanced entries (sum ≠ 0) → `ErrUnbalancedTransaction`, nothing written
  - Single entry → `ErrMinimumEntriesNotMet`, nothing written
  - `ValidateBalance` and `ValidateMinEntries` as standalone pure-function unit tests
    (no DB required for these — test the validation logic in complete isolation)
- [x] `internal/ledger/get_balance_test.go`: zero entries returns 0; known entries return correct sum
- [x] `internal/ledger/get_statement_test.go`: entries outside `posted_at` range are excluded;
  entries within range are returned in correct order

**Deliverable:** a `ledger` package with no HTTP/API dependency, fully testable on its own.

**Done when:** you can call `PostTransaction(...)` from a Go test file and see correct rows land
in Postgres, with an unbalanced transaction correctly rejected with `ErrUnbalancedTransaction`.

> Status: ✅ Complete. 22/22 tests pass (`go test ./internal/ledger/... -v`).

---

## Phase 4 — Idempotency Layer
**Goal:** Make `PostTransaction` safe to call twice with the same key — this is the single most
important "real payments system" behavior to demonstrate.

- [x] Before inserting, check if `idempotency_key` already exists
- [x] If it exists: return the *original* transaction's result, do not re-process
- [x] If it doesn't exist: proceed with Phase 3 logic
- [x] Handle the race condition: two concurrent requests with the same key should not both pass
  the "doesn't exist" check and double-insert — use the unique constraint from Phase 2 as the
  actual safety net (catch the constraint violation, fetch and return the existing transaction)
- [x] `internal/ledger/get_transaction.go`: `GetTransaction(ctx, id)` — returns transaction
  header + all entries as `TransactionDetail`; returns `ErrTransactionNotFound` on miss
- [x] Write a test that fires the same request twice (sequentially) and asserts only one
  transaction + entry set exists
- [x] Write a concurrent test: 10 goroutines with the same idempotency key fired simultaneously;
  assert all return same transaction ID and balance reflects exactly one transaction

**Deliverable:** idempotency is enforced at the DB constraint level, not just app-level checks
(app-level checks alone are not safe under concurrency — this is the actual lesson of this phase).

**Done when:** posting the same idempotency key twice produces one transaction, not two, even if
you call it concurrently from two goroutines in a test.

> Status: ✅ Complete. 28/28 tests pass under `-race` detector.

---

## Phase 5 — REST API
**Goal:** Expose the ledger engine over HTTP. Keep the API thin — it should call into the
`ledger` package, not contain business logic itself.

- [x] `POST /accounts` — create an account
- [x] `GET /accounts/{id}/balance` — current balance (includes currency)
- [x] `GET /accounts/{id}/statement` — entry history filtered by posted_at range
- [x] `POST /transactions` — post a transaction
  - `Idempotency-Key` header takes precedence over body field
- [x] `GET /transactions/{id}` — fetch a transaction and its entries
- [x] `GET /accounts/{id}` — fetch account by ID (used internally for balance currency lookup)
- [x] Basic input validation (malformed amounts, missing fields → 400, not 500)
- [x] Consistent error response shape (`{"error": "..."}`) across all endpoints
- [x] Sentinel error → HTTP status mapping (`ErrAccountNotFound` → 404, `ErrUnbalancedTransaction` → 422, etc.)

**Deliverable:** a running HTTP server (use `net/http` + a light router like `chi`, or `Gin`/`Fiber`
if you prefer — either is a reasonable, defensible choice).

**Done when:** you can `curl` every endpoint above and get correct, sane responses, including
correct error codes for bad input.

> Status: ✅ Complete. All endpoints verified via curl. Server runs on :8080.

---

## Phase 5.5 — Currency Conversion Module
**Goal:** Integrate an external exchange rate API to support cross-currency transactions,
with a layered fallback strategy that keeps the system available even when the external
service is down. Demonstrates real HTTP client engineering, graceful degradation, and
caching patterns.

- [ ] Create `internal/currency/` package — zero dependency on ledger or API packages
- [ ] Implement `RateService` that fetches live rates from `frankfurter.app` (free, no key required)
  - `GET https://api.frankfurter.app/latest?from=USD&to=INR`
- [ ] Add in-memory rate cache with a **1-hour TTL** (use `sync.Mutex` + timestamp, no Redis)
- [ ] Implement **layered fallback strategy**:
  1. Try primary API (3-second timeout)
  2. On failure → serve stale cache **if age < 24 hours** (`rate_source: stale_cache`)
  3. No cache or cache > 24 hours old → hard fail with `503 Service Unavailable`
- [ ] Expose `Convert(amount int64, from, to string) (int64, Rate, error)` — pure function, testable
- [ ] Store `exchange_rate` (float64) and `rate_source` (`live` / `stale_cache`) on the transaction
- [ ] Wire into API layer: pre-convert amounts before handing entries to ledger engine
- [ ] Unit tests for the fallback logic (mock the HTTP client)

**Deliverable:** `internal/currency/` package with live rate fetching, in-memory caching, and
stale-cache fallback. Cross-currency transactions store the rate used for full auditability.

**Done when:** A cross-currency `POST /transactions` succeeds with live rates, and a mocked
API-down scenario correctly serves a stale cached rate (or fails cleanly if cache is too old).

> Status: Not started.

---

## Phase 6 — Tests & Invariant Checks
**Goal:** Prove correctness, not just "it runs." This phase is what makes the project credible
in an interview — "I wrote tests proving the ledger can never go out of balance" is a strong line.

- [ ] Unit tests for `PostTransaction` (balanced entries succeed, unbalanced entries rejected)
- [ ] Unit tests for idempotency (duplicate key → same result, no duplicate rows)
- [ ] Integration test: post 50+ random valid transactions across a handful of accounts, then
  assert the **global invariant**: sum of all entries across the entire ledger = 0
- [ ] Concurrency test: fire N concurrent `PostTransaction` calls (some with shared idempotency
  keys, some without) and assert no double-processing and no constraint violations crash the app
- [ ] Basic API-level tests (httptest) for the main happy paths and 1–2 error paths per endpoint

**Deliverable:** `go test ./...` passes, with the global balance invariant test as the centerpiece.

**Done when:** the invariant test (`sum of all entries = 0` after random transaction load) passes
reliably, including under the concurrency test.

> Status: Not started.

---

## Phase 7 — Polish & Demo Readiness
**Goal:** Make the project legible to someone skimming your GitHub for 90 seconds, and to you in
an interview 3 months from now.

- [ ] Write the real `README.md`: what it is, why double-entry, why idempotency keys, schema
  diagram, how to run it locally, example `curl` commands
- [ ] Add a short "Design Decisions" section to the README covering: why integer minor units not
  floats, why derive balance instead of storing it (and the tradeoff), why idempotency is enforced
  at the DB layer not just app layer
- [ ] Clean up `NOTES.md` into 5–6 bullet points of "what I learned" — keep this, it's your
  interview prep
- [ ] Tag a `v1.0` release/commit
- [ ] (Optional, only if time allows) Add a minimal `docker-compose.yml` that brings up the full
  app + DB with one command, so anyone can run it without setup

**Deliverable:** a GitHub repo a recruiter or interviewer can read in 2 minutes and understand
exactly what was built and why.

**Done when:** you can walk through the whole system out loud in under 3 minutes, covering: the
data model, the invariant, idempotency, and one concurrency edge case you handled.

> Status: Not started.

---

## Explicitly Out of Scope for v1 (Don't Build These Yet)
Keeping this list visible is intentional — scope creep is the main risk to actually finishing.

- Authentication/authorization (not the point of this project — skip it)
- ~~Multi-currency conversion logic~~ → **Moved in-scope as Phase 5.5**
- Webhooks/notifications
- A frontend/UI
- Reversals/refunds as a separate concept (a reversal is just another balanced transaction —
  understanding that *is* a good thing to mention in an interview, but don't build special-case code for it in v1)
- Deployment to cloud infrastructure

If the project is going well and you want a v1.1 stretch goal after Phase 7, reversals-as-transactions
or a stored/cached balance with a reconciliation job against the derived balance (a real pattern at
scale) are the two best next steps — but only after v1 is genuinely done and demoable.

---

## Master Checklist (Quick Progress View)
- [x] Phase 0 — Project setup complete
- [x] Phase 1 — Data contracts frozen and understood
- [x] Phase 2 — Schema + migrations complete
- [x] Phase 3 — Core ledger engine tested in isolation
- [x] Phase 4 — Idempotency enforced and race-condition tested
- [x] Phase 5 — REST API complete
- [ ] Phase 5.5 — Currency conversion module with fallback strategy
- [ ] Phase 6 — Invariant + concurrency tests passing
- [ ] Phase 7 — README, design notes, and demo-ready

## Next Immediate Action
1. **Phase 2** — Write the PostgreSQL schema and first migration. Run it clean on the Docker Postgres instance.