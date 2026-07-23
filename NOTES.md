# NOTES.md — Payment Ledger System
> One place to capture what I learned phase by phase.
> These are the things I'll actually talk about in interviews — not the code itself.

---

## Phase 0 — Project Setup

**Goose for migrations, not auto-migrate**
Goose uses raw SQL files versioned by number (`00001_init_schema.sql`). Unlike ORM auto-migrate, you see exactly what runs against your DB. In production, that matters — no surprises.

**Port conflict: 5432 already bound**
Local Postgres was running on 5432. Moved Docker Postgres to 5433. Real lesson: always check what's already listening (`lsof -i :5432`) before wiring up config.

---

## Phase 1 — Data Contracts

**Why integer minor units instead of floats**
IEEE 754 floating point can't represent 0.1 exactly. Across thousands of transactions, rounding errors compound. Storing ₹10.50 as `1050` (paise) means all arithmetic is integer arithmetic — no precision loss ever.

**Why derive balance instead of storing it**
A stored `balance` column is a cache. Caches go stale. If you write to `entries` but update `balance` non-atomically, they drift. Deriving via `SUM(entries)` is always correct by definition. The trade-off is read speed — acceptable for v1, and solvable later with a read model or snapshot table.

**Why idempotency keys belong at the DB layer, not just the app layer**
An app-layer check (`if key exists, return early`) has a race condition: two requests with the same key can both pass the check before either commits. A `UNIQUE` constraint on `transactions.idempotency_key` makes the DB the source of truth — only one will commit, the other gets a constraint violation which we catch by error code (`23505`).

---

## Phase 2 — PostgreSQL Schema

**Postgres enums vs. CHECK constraints**
Used `CREATE TYPE account_type AS ENUM (...)` rather than `CHECK (account_type IN (...))`. Enums are validated at the type level, appear correctly in `\d` schema output, and are more explicit. The downside: adding a new value requires `ALTER TYPE` not just a constraint change.

**Why `posted_at` is nullable**
A transaction starts as `pending` (no `posted_at`). It only gets stamped when entries are committed. This lets you distinguish "received but not yet processed" from "fully committed" — important for statement queries which must only show posted entries.

---

## Phase 3 — Core Ledger Engine

**Interface over concrete struct for the Engine**
`ledger.Engine` is an interface, not just `*ledger.Service`. This means HTTP handlers depend on the abstraction. In tests, you can swap in a mock without a real database. This is the standard Go pattern for dependency injection without a framework.

**Validate before touching the DB**
`ValidateBalance` and `ValidateMinEntries` are pure functions — no DB calls. They run first. If validation fails, nothing hits Postgres. This keeps the happy path fast and makes the validation logic trivially unit-testable without any DB setup.

**Why `defer tx.Rollback()` is always safe**
`pgx.Tx.Rollback` is a no-op after a successful `Commit`. Deferring it unconditionally means: if anything panics or returns early, the transaction rolls back automatically. You never need to remember to rollback in every error path.

---

## Connection Pooling — Why pgxpool, Not a Single Connection

**The problem with one connection per request**
Opening a raw DB connection involves a TCP handshake, TLS negotiation, Postgres auth, and session setup. At 200 concurrent requests, that's 200 connection setups — and Postgres has a hard cap (~100 by default). You'd start seeing `too many connections`.

**What a pool does**
Keeps N connections pre-warmed. Requests borrow a connection, run their query, and return it. Beyond `MaxConns`, requests queue — this is backpressure by design.

**Why it matters for system design**
- **Reuse** — connection setup cost paid once at startup, not per request
- **Bounded load** — pool caps how hard you hit Postgres regardless of traffic spikes
- **Concurrency safe** — `pgxpool` is goroutine-safe; one pool instance shared across all handlers
- **Health recovery** — dead connections are replaced automatically; a single `*pgx.Conn` just stays broken
- **Postgres memory** — each connection = one backend process (~5–10 MB). 10 pooled connections vs. 200 raw = a big difference under load

**One-liner for interviews:**
> "A connection pool amortises connection setup cost, bounds DB backend processes, and gives you automatic health recovery — it's the standard pattern for any concurrent request handler."

---

## General Standards

**ISO 4217 — Currency Codes**
The international standard for 3-letter alphabetic currency codes, maintained by ISO.

Structure: first 2 letters = country code (ISO 3166-1), third = first letter of currency name.
- `INR` → India + Rupee
- `USD` → United States + Dollar
- `EUR` → Eurozone + Euro

Exceptions: codes starting with `X` are not country-bound (`XAU` = gold, `XDR` = IMF SDR).

**Why it matters in code:**
Without a standard, you get `"rupee"`, `"Rs"`, `"inr"`, `"INR"` all meaning the same thing.
ISO 4217 gives a canonical 3-char string safe to use as a DB column, map key, or API field.

**Minor units per currency (also defined in the standard):**
| Currency | Minor unit | Example |
|----------|-----------|---------|
| INR | 2 (paise) | ₹10.50 → `1050` |
| USD | 2 (cents) | $10.50 → `1050` |
| JPY | 0 | ¥500 → `500` |
| KWD | 3 (fils) | 1.000 KD → `1000` |

This is why storing amounts as `int64` minor units works universally — the standard defines exactly what "minor unit" means per currency, so your arithmetic stays in integers regardless of which currency you handle.

**One-liner for interviews:**
> "We store amounts as int64 minor units. ISO 4217 tells us how many decimal places each currency has, so we never lose precision to floating point and the ledger invariant holds as pure integer arithmetic."

---

**Parameterized Queries — How They Work and Why They Prevent SQL Injection**

In every SQL query in this project, values are passed as separate parameters (`$1`, `$2`, `$3`) — never concatenated into the SQL string:

```go
const q = `INSERT INTO accounts (name, account_type, currency) VALUES ($1, $2, $3)`
pool.QueryRow(ctx, q, req.Name, req.AccountType, req.Currency)
```

pgx sends two separate things to Postgres:
1. The query template (parsed as SQL — structure is locked)
2. The parameter values (treated as typed data, never as SQL)

**The attack this prevents — SQL Injection:**
If you naively concatenate user input:
```go
q := "INSERT INTO accounts (name) VALUES ('" + req.Name + "')"
// req.Name = "x'); DROP TABLE accounts; --"
// Result:   INSERT INTO accounts (name) VALUES ('x'); DROP TABLE accounts; --'
```
Postgres would execute two statements: the INSERT and then the DROP. Your data is gone.

With `$1`, the malicious string is inserted as a literal name value. The SQL structure was already parsed before the value arrived — there is no point where user input is interpreted as SQL syntax.

**The `RETURNING` clause:**
A Postgres-specific feature that makes `INSERT` return the inserted row immediately. Without it, you'd need a second query (`SELECT ... WHERE id = $1`) to get the `id` and `created_at` that Postgres generated. `RETURNING` collapses two round-trips into one.

**One-liner for interviews:**
> "We use parameterized queries exclusively — `$1`, `$2`, etc. The query template and the values travel to Postgres separately, so user input is never interpreted as SQL. SQL injection requires concatenation; parameterized queries eliminate the concatenation entirely."

---

## Phase 4 — Idempotency Layer

**Why app-layer checks are not enough**
The naive approach: before inserting, check if the idempotency key already exists. If yes, return early. The problem: two concurrent requests with the same key can both pass the check before either has committed. Both see "key doesn't exist", both insert, you now have duplicate rows. This is a classic TOCTOU (time-of-check to time-of-use) race.

**The UNIQUE constraint is the actual safety net**
The DB-layer `UNIQUE` constraint on `transactions.idempotency_key` makes the race impossible. Only one `INSERT` can win — the other gets a constraint violation. We catch it by error code `23505` (Postgres unique violation):

```go
var pgErr *pgconn.PgError
if errors.As(err, &pgErr) && pgErr.Code == "23505" {
    // Key already exists — fetch and return the original transaction
    return s.getTransactionByKey(ctx, req.IdempotencyKey)
}
```

No app-level lock, no SELECT before INSERT. The DB handles the race atomically.

**Why the "check then act" pattern is never safe under concurrency**
Any sequence of "read → decide → write" can be interrupted between steps by another goroutine or process. The only truly safe pattern is to make the write itself enforce the constraint — let the DB reject duplicates, then react to the rejection. This applies beyond idempotency: it's the same reason you use `INSERT ... ON CONFLICT` or `UPDATE ... WHERE version = $1` (optimistic locking) instead of "read balance, check, write balance".

**How to write a meaningful concurrency test in Go**
Use a channel as a start gun to maximise goroutine overlap:
```go
start := make(chan struct{})
for i := 0; i < 10; i++ {
    go func(i int) {
        <-start  // all goroutines block here
        results[i], errs[i] = postTransaction(req)
    }(i)
}
close(start)  // releases all 10 goroutines simultaneously
wg.Wait()
```
Without the channel, goroutines launch sequentially and the "concurrent" test is really just sequential with goroutine overhead — it wouldn't catch the race.

**Run tests with `-race`**
Go's race detector (`go test -race`) instruments memory accesses and reports data races at runtime. It doesn't prove absence of races (it only catches races that actually occur during the test run), but it's the standard tool. All Phase 4 tests pass clean under `-race`.

**One-liner for interviews:**
> "Idempotency is enforced by a UNIQUE constraint on the DB, not application-level checks. App checks have a TOCTOU race — two requests can both pass the check before either commits. The constraint makes the write itself atomic: only one succeeds, we catch the `23505` violation and return the original result."

---

## Go Concepts — Goroutines

**What is a goroutine?**
A goroutine is Go's unit of concurrent execution. You launch one by putting `go` in front of a function call:
```go
go func() {
    doSomething()
}()
```
It runs independently — the calling code doesn't wait for it. Unlike OS threads, goroutines are extremely cheap (~2 KB stack at startup vs. ~1 MB for a thread). A Go program can run tens of thousands of goroutines on a handful of OS threads. The Go runtime multiplexes them automatically.

**Goroutine vs. thread**
| | OS Thread | Goroutine |
|---|---|---|
| Stack size | ~1 MB fixed | ~2 KB, grows as needed |
| Creation cost | Expensive (kernel call) | ~200ns |
| Managed by | OS scheduler | Go runtime |
| Typical count | Hundreds | Tens of thousands |

**`sync.WaitGroup` — waiting for goroutines to finish**
Goroutines fire and forget by default. `WaitGroup` is how you wait for a group of them to all complete:
```go
var wg sync.WaitGroup
wg.Add(1)       // tell the group: one more goroutine is starting
go func() {
    defer wg.Done()  // decrement when this goroutine finishes
    doWork()
}()
wg.Wait()       // block here until count reaches 0
```

**How goroutines are used in the concurrent idempotency test**
The goal was to simulate 10 real HTTP requests arriving at the exact same millisecond with the same idempotency key. Without goroutines, you'd test them one at a time — that's not a concurrency test, it's just 10 sequential calls.

```go
const numGoroutines = 10
start := make(chan struct{})  // empty channel used as a signal

for i := 0; i < numGoroutines; i++ {
    wg.Add(1)
    go func(i int) {
        defer wg.Done()
        <-start  // all 10 goroutines park here, waiting for the signal
        results[i], errs[i] = testService.PostTransaction(ctx, req)
    }(i)
}

close(start)  // closing a channel unblocks ALL receivers simultaneously
wg.Wait()     // wait for all 10 to finish
```

The `close(start)` trick: closing a channel causes every goroutine blocked on `<-start` to unblock at the same moment. This is the closest you can get to "fire all at once" in a test — without it, goroutines start one-by-one in a loop and the "concurrent" window is much smaller.

**What the test actually proves**
Without the DB-layer `UNIQUE` constraint, some of the 10 goroutines would race past any app-level check and both insert — resulting in duplicate transaction rows and a `dst` balance of `N × 10000` instead of `10000`. The test asserts:
1. All goroutines return without error (the losers get the original tx back, not a crash)
2. All return the same transaction ID (no duplicates)
3. `GetBalance(dst)` == `10000`, not `100000` (only one transaction posted)

**How this makes the app safe in production**
In production, every HTTP request runs in its own goroutine (Go's `net/http` does this automatically). A payment API under load will have hundreds of goroutines hitting `PostTransaction` simultaneously. The concurrent test proves that even under that exact scenario — with the worst possible timing — the ledger invariant holds: one idempotency key = one transaction, no matter how many goroutines try to create it at once.

**One-liner for interviews:**
> "Go's `net/http` handles each request in its own goroutine automatically. Our concurrent test uses 10 goroutines with a channel start gun to simulate exactly that — multiple requests landing simultaneously with the same idempotency key — and proves the DB constraint, not our app code, is what makes it safe."

---

## Phase 5 — REST API

**Why a thin API layer matters**
The HTTP handlers contain no business logic — they decode JSON, call the `ledger.Engine` interface, and encode the response. All rules (double-entry balance, account validation, idempotency) live in the `ledger` package. This means:
- You can swap the transport (REST → gRPC → CLI) without touching business logic
- You can test the ledger engine without starting an HTTP server
- Handler tests only need to check JSON encoding, status codes, and routing — not financial rules

**The interface as a seam**
`ledger.Engine` is an interface. The handlers depend on it, not on `*ledger.Service`. This means in handler tests you can inject a fake implementation that returns whatever you want — no real DB needed. This is the standard Go pattern for testability: depend on interfaces, not concrete types.

**DTOs — why the API layer owns its own types**
We define separate request/response structs in `internal/api/dto.go` rather than returning `db.Account`, `db.Transaction` etc. directly. Reasons:
- DB models are internal implementation details. Exposing them leaks schema to clients.
- DB field names (`IdempotencyKey`, `RateSource`) might differ from what the API should expose (`idempotency_key`, `rate_source`).
- We can evolve the DB schema without breaking the API contract, and vice versa.

**Sentinel errors → HTTP status codes**
The ledger engine returns typed errors (`ErrAccountNotFound`, `ErrUnbalancedTransaction`, etc.). The `mapLedgerError` function in the API layer switches on these to produce the correct HTTP status:
```go
switch {
case errors.Is(err, ledger.ErrAccountNotFound):      → 404
case errors.Is(err, ledger.ErrUnbalancedTransaction): → 422
default:                                               → 500
}
```
This is the clean separation: the ledger package says *what* went wrong (domain language), the API layer decides *how to communicate it* (HTTP language). The ledger package never imports `net/http`.

**Idempotency-Key as a header, not just a body field**
We accept the key both as an `Idempotency-Key` HTTP header and as a body field, with the header taking precedence. This mirrors how Stripe, Braintree, and most major payment APIs do it. Headers are easier to set by load balancers and gateways without modifying the body, and they're a well-established API convention — important to mention in interviews.

**RFC 3339 for timestamps**
Statement queries take `from` and `to` as RFC 3339 strings (e.g. `2026-01-01T00:00:00Z`). RFC 3339 is a profile of ISO 8601 — it mandates a timezone offset, so there's no ambiguity about whether a timestamp is local time or UTC. Go's `time.Parse(time.RFC3339, ...)` strictly enforces this format and returns a `time.Time` with timezone information intact.

**chi as the router**
`net/http`'s default mux can't do path parameters like `{id}`. `chi` adds this (and named routes, middleware chaining) while staying 100% compatible with `net/http` — any `http.Handler` works, any standard middleware works. It adds no framework lock-in.

**Middleware stack and what each layer does**
```
RequestID   → injects X-Request-Id — makes every request traceable in logs
requestLogger → structured slog output: method, path, status, duration_ms, request_id
Recoverer   → catches panics, returns 500 instead of crashing the server
```
The order matters: `RequestID` runs first so `requestLogger` can read it.

`middleware.RealIP` was intentionally omitted. It reads `X-Forwarded-For` / `True-Client-IP` headers and overwrites `r.RemoteAddr` — but those headers can be set by anyone. Without a trusted proxy confirming the value, you're letting a client spoof its own IP address (CVEs: GHSA-3fxj-6jh8-hvhx, GHSA-rjr7-jggh-pgcp). The kernel-provided `r.RemoteAddr` is the only trustworthy source at this stage. If a real proxy layer is added later, this decision should be revisited with explicit trusted-IP allowlisting.

**One-liner for interviews:**
> "The API layer is just translation — JSON in, domain call, JSON out. All financial rules live in the ledger package which has zero `net/http` imports. Sentinel errors bubble up and get mapped to HTTP status codes at the boundary. This means I can test the entire ledger engine without starting a server."

---

## Request / Response Lifecycle — `POST /transactions`

This traces a single `POST /transactions` call from the moment it hits the network to the moment JSON lands back in the client. Every file touched is listed in order.

---

### Step 1 — OS hands the connection to `net/http`
**File: `cmd/server/main.go`**

`http.ListenAndServe(":8080", router)` is blocking at startup. When a TCP connection arrives, `net/http` accepts it and spawns a new goroutine. That goroutine parses the HTTP request line and headers, then calls `router.ServeHTTP(w, r)`.

---

### Step 2 — Middleware runs, top to bottom
**File: `internal/api/router.go`**

The chi router wraps every request in the middleware stack before a handler ever runs:

```
RequestID middleware   → generates a unique ID, sets it on r.Context() and as X-Request-Id header
requestLogger start    → records start time, wraps ResponseWriter to capture status code later
Recoverer middleware   → defers a panic handler around everything below
```

At this point no handler logic has run. The request is just an `*http.Request` moving through a chain of decorators.

---

### Step 3 — chi routes the request to the handler
**File: `internal/api/router.go`**

chi matches `POST /transactions` against the registered routes and calls `transactions.PostTransaction(w, r)`. Path parameters (like `{id}` on other routes) would be extracted here and stored in the request context.

---

### Step 4 — Handler decodes and validates the HTTP request
**File: `internal/api/transactions.go` → `PostTransaction`**

```
json.NewDecoder(r.Body).Decode(&req)
```

The handler:
1. Decodes the JSON body into an API-layer `PostTransactionRequest` DTO
2. Checks if `Idempotency-Key` header is set — overrides body field if present
3. Validates that `idempotency_key` is non-empty and `entries` is non-empty
4. Maps `[]api.EntryInput` → `[]ledger.EntryInput` (crossing the package boundary)

If any of these fail, `writeError(w, 400, "...")` is called and the function returns. The ledger engine is never touched.

---

### Step 5 — Handler calls the ledger engine (the domain boundary)
**File: `internal/api/transactions.go` → `internal/ledger/post_transaction.go`**

```go
tx, err := h.engine.PostTransaction(r.Context(), ledger.PostTransactionRequest{...})
```

`h.engine` is the `ledger.Engine` interface. The concrete type behind it is `*ledger.Service`. This is the boundary between transport and domain.

---

### Step 6 — Pure validations (no DB)
**File: `internal/ledger/post_transaction.go`**

```
ValidateMinEntries(entries)   → at least 2 entries?
ValidateBalance(entries)      → sum of all amounts == 0?
```

These are pure functions. If either fails, the engine returns a typed sentinel error (`ErrMinimumEntriesNotMet`, `ErrUnbalancedTransaction`). No database has been touched. The handler receives the error, calls `mapLedgerError` → writes a 422 response.

---

### Step 7 — Account existence check (DB read)
**File: `internal/ledger/post_transaction.go` → `internal/ledger/create_account.go`**

```go
for _, e := range entries {
    exists, _ := s.accountExists(ctx, e.AccountID)
    if !exists { return ErrAccountNotFound }
}
```

`accountExists` runs `SELECT EXISTS(...)` against Postgres via the connection pool. If any account ID doesn't exist, `ErrAccountNotFound` is returned → handler maps to 404.

---

### Step 8 — Atomic DB transaction (BEGIN → INSERT → UPDATE → COMMIT)
**File: `internal/ledger/post_transaction.go`**

```
pool.Begin(ctx)
  INSERT INTO transactions → catches 23505 (idempotency_key collision)
  INSERT INTO entries × N
  UPDATE transactions SET status = 'posted', posted_at = NOW()
pool.Commit()
```

The `defer tx.Rollback()` sits at the top — if anything between Begin and Commit fails, every write is undone atomically. Postgres guarantees all-or-nothing. The ledger engine returns `*db.Transaction` on success.

---

### Step 9 — Engine fetches full detail for the response
**File: `internal/api/transactions.go` → `internal/ledger/get_transaction.go`**

```go
detail, _ := h.engine.GetTransaction(r.Context(), tx.ID)
```

Two queries run:
1. `SELECT ... FROM transactions WHERE id = $1`
2. `SELECT ... FROM entries WHERE transaction_id = $1`

These are separate from the write path — the write path returns only the transaction header, so a second read is needed to include entries in the response.

---

### Step 10 — Handler maps domain types to DTOs and writes the response
**File: `internal/api/transactions.go` → `internal/api/dto.go`**

```go
writeJSON(w, http.StatusCreated, TransactionDetailResponse{
    Transaction: transactionToResponse(detail.Transaction),
    Entries:     entriesToResponse(detail.Entries),
})
```

`db.Transaction` → `api.TransactionResponse` (renames fields, formats timestamps).
`[]db.Entry` → `[]api.EntryResponse`.

`writeJSON` sets `Content-Type: application/json`, writes the status code, and encodes the struct to the `ResponseWriter`.

---

### Step 11 — Middleware finishes (post-handler)
**File: `internal/api/router.go` → `requestLogger`**

The `requestLogger` middleware deferred `next.ServeHTTP(ww, r)` — after the handler returns, it reads the captured status code from the wrapped `ResponseWriter` and logs:

```
method=POST path=/transactions status=201 duration_ms=14 request_id=...
```

The Recoverer middleware's deferred panic handler also runs here (finding nothing to recover from).

---

### Full file trace for `POST /transactions`

```
cmd/server/main.go               → accepts connection, dispatches to router
internal/api/router.go           → middleware chain + chi routing
internal/api/transactions.go     → decode JSON, validate HTTP concerns, call engine
internal/ledger/post_transaction.go → pure validation, account check, atomic DB write
internal/ledger/create_account.go   → accountExists() helper
internal/db/connection.go           → pgxpool executes SQL against Postgres
internal/ledger/get_transaction.go  → fetch header + entries for response
internal/api/dto.go                 → map db types → response types
internal/api/errors.go              → writeJSON / writeError
internal/api/router.go              → requestLogger logs the completed request
```

The ledger package (`internal/ledger/`) never imports `net/http`. The api package (`internal/api/`) never contains business logic. The db package (`internal/db/`) never knows about either. Each layer only talks to the one directly below it.

---

## Phase 5.5 — Currency Conversion & the `GetRate` Function

### `sync.Mutex` — protecting shared state across goroutines

`net/http` runs every request in its own goroutine. `GetRate` can be called from hundreds of goroutines simultaneously. The `cache` map is shared state — if two goroutines write to it at the same time without coordination, Go will detect a **data race** and crash (or silently corrupt data without the race detector).

`s.mu.Lock()` / `s.mu.Unlock()` ensures only one goroutine accesses the cache at a time:

```go
s.mu.Lock()
entry, ok := s.cache[cacheKey]
s.mu.Unlock()
```

**Why unlock immediately after the read?** Because `fetchLive` (the HTTP call) can take up to 3 seconds. Holding the lock during a network call would block every other goroutine until it returns. Instead:
1. Lock → read cache → unlock (microseconds)
2. HTTP call with no lock held (up to 3 seconds, but not blocking others)
3. Lock → write result back → unlock (microseconds)

Keep the critical section as short as possible. This is the standard Go pattern.

---

### The `cacheKey` — why concatenate strings

```go
cacheKey := from + "_" + to
```

The cache is a `map[string]cachedRate`. We need one key per currency pair. Using a separator ensures `USD_INR` and `INR_USD` are different keys — they are different rates (one is the inverse of the other). Without a separator, two currency codes that happen to share characters could collide.

---

### The layered fallback — step by step

**Function signature:**
```go
func (s *RateService) GetRate(ctx context.Context, from, to string) (Rate, error)
```
`ctx` carries a deadline. If the caller's request is cancelled mid-flight, the `fetchLive` HTTP call aborts automatically — no manual timeout handling needed.

---

**Same-currency short-circuit:**
```go
if from == to {
    return Rate{..., Value: 1.0, RateSource: "live"}, nil
}
```
No network call, no cache lookup. 1 unit of X always equals 1 unit of X.

---

**Step 1 — Fresh cache:**
```go
entry, ok := s.cache[cacheKey]

if ok && time.Since(entry.fetchedAt) < s.liveTTL {
    return Rate{..., RateSource: "live"}, nil
}
```

`entry, ok := map[key]` is Go's two-value map lookup — `ok` is `true` if the key exists, `false` if missing. This avoids a separate `Contains` check.

`time.Since(entry.fetchedAt)` returns the age of the cached rate. If it's under 1 hour (`liveTTL`), return it immediately. `RateSource = "live"` — from the client's perspective it's indistinguishable from a real API fetch.

---

**Step 2 — Live API:**
```go
value, fetchedAt, err := s.fetchLive(ctx, from, to)
if err == nil {
    s.mu.Lock()
    s.cache[cacheKey] = cachedRate{value: value, fetchedAt: fetchedAt}
    s.mu.Unlock()
    return Rate{..., RateSource: "live"}, nil
}
```

`fetchLive` makes the actual HTTP GET to `api.frankfurter.app`. If it succeeds (`err == nil`), write the new rate into the cache and return. The success branch uses `if err == nil` rather than `if err != nil` — the error case deliberately falls through to step 3.

---

**Step 3 — Stale cache (graceful degradation):**
```go
if ok && time.Since(entry.fetchedAt) < s.staleTTL {
    return Rate{..., RateSource: "stale_cache"}, nil
}
```

`ok` and `entry` are still in scope from step 1 — Go variables live for their entire enclosing function, not just the block where they were declared. If the cached entry is less than 24 hours old, serve it despite the API being down.

`RateSource = "stale_cache"` is stored on the transaction row. Auditors can see that the rate came from a cache, not a live fetch. This is **graceful degradation** — the system stays available during upstream outages, at the cost of slightly stale rates.

---

**Step 4 — Hard fail:**
```go
return Rate{}, fmt.Errorf("%w: %s→%s: %v", ErrRateUnavailable, from, to, err)
```

`%w` **wraps** `ErrRateUnavailable` inside the error. This means callers can use `errors.Is(err, ErrRateUnavailable)` to check the error type without string matching — the sentinel survives being wrapped in extra context. `%s→%s` adds the currency pair. `%v` appends the underlying API error for logs.

The API layer catches `ErrRateUnavailable` and returns `503 Service Unavailable` — the correct HTTP signal that a dependency is down and the client should retry later.

---

**One-liner for interviews:**
> "The RateService has three levels of availability. First it checks an in-memory cache with a 1-hour TTL — no network call at all. If stale, it tries the live API. If the API is down, it falls back to the stale cache for up to 24 hours and marks `rate_source = stale_cache` on the transaction for auditability. Only if there's no cache at all does it hard-fail with 503. The mutex only wraps the map reads and writes — not the HTTP call — so parallel requests never block each other."

---

## Phase 6 — Tests & Invariant Checks

### The global zero-sum invariant — what it actually proves

The centrepiece of Phase 6 is one SQL query:

```sql
SELECT COALESCE(SUM(amount), 0) FROM entries;
```

If this returns `0` after any number of transactions, the double-entry invariant holds across the entire ledger — not just per-transaction. This is a meaningful guarantee because the invariant could theoretically hold per-transaction (each individual call was balanced) while still being violated globally if a bug caused partial writes, duplicate entries, or missing entries under concurrency.

The test posts 60 random transactions across 5 accounts with random amounts, then runs this query. The result must be `0`. It doesn't matter which accounts were involved or what amounts moved — if every debit has a matching credit, the sum is always zero. This is the mathematical property that double-entry bookkeeping is built on.

**One-liner for interviews:**
> "I wrote a test that posts 60 random transactions across 5 accounts, then queries `SUM(entries.amount)` directly from Postgres and asserts it equals zero. That's the ledger invariant — it doesn't prove anything about individual transactions, it proves the whole system is balanced. If there were a bug in the write path that dropped an entry, or wrote the same entry twice, this would catch it."

---

### The channel start-gun pattern — maximising goroutine overlap

In both concurrency tests, goroutines are created first and then all released at once:

```go
start := make(chan struct{})

for i := 0; i < numGoroutines; i++ {
    wg.Add(1)
    go func(i int) {
        defer wg.Done()
        <-start  // block here until released
        // ... do work
    }(i)
}

close(start)  // release all goroutines simultaneously
wg.Wait()
```

**Why not just `go func()` in a loop?** Goroutines spawned in a loop start immediately. By the time goroutine 10 is created, goroutine 1 may have already finished. There's very little actual overlap — you're not really testing concurrency, you're testing sequential execution with overhead.

`close(start)` unblocks all goroutines at once because a read from a closed channel returns immediately. All goroutines hit the DB at the same time, which is what exercises the race conditions you care about: concurrent `INSERT`, concurrent idempotency key checks, concurrent `SELECT FOR UPDATE`.

**When `close(start)` fires:** all goroutines were already blocked on `<-start`. The OS schedules them all to run. With 10-30 goroutines all hitting Postgres simultaneously, the `UNIQUE` constraint on `idempotency_key` and the `FOR UPDATE` advisory lock in `PostTransaction` are the only things preventing duplicate rows. The test proves they hold.

---

### Why split tests into pure unit tests and DB integration tests?

Every test file in `internal/ledger/` calls `requireDB(t)` at the top of any test that needs Postgres. Tests that don't touch the DB (pure validation logic) never call `requireDB`.

```go
func requireDB(t *testing.T) {
    t.Helper()
    if testPool == nil {
        t.Skip("skipping: DATABASE_URL not set or DB unreachable")
    }
}
```

This means `go test ./...` always passes — even in CI without a database, even in an editor sandbox, even on a machine that has never run Docker. Pure validation logic is always tested. DB tests skip gracefully and are run explicitly with a live database.

**The tradeoff:** you have to be disciplined about what belongs in pure tests vs. integration tests. `ValidateBalance` and `ValidateMinEntries` are pure — they take a slice of entries and return an error, no I/O. `PostTransaction` end-to-end is an integration test because it involves inserting rows, checking constraints, and reading back data.

---

### Mock Engine for httptest — testing the HTTP layer in isolation

The API handler tests use `httptest.NewRecorder()` and a mock that implements `ledger.Engine`:

```go
type mockEngine struct {
    postTransactionFn func(ctx context.Context, req ledger.PostTransactionRequest) (*db.Transaction, error)
    // ... one field per interface method
}
```

Each test sets only the functions it needs. For a `GetBalance_NotFound` test, only `getBalanceFn` and `getAccountFn` are set — the rest are nil and will panic if accidentally called, which surfaces bugs immediately.

**Why test through the full router, not the handler directly?**

```go
func newTestRouter(engine ledger.Engine) http.Handler {
    accounts := api.NewAccountHandler(engine)
    transactions := api.NewTransactionHandler(engine, nil)
    return api.NewRouter(accounts, transactions)
}
```

Using `api.NewRouter` means the test exercises chi's routing (correct path params extracted), the request-ID middleware (headers set), and the request logger — the full stack, not just the handler function. A handler test that bypasses the router wouldn't catch a routing bug like `/accounts/{id}` accidentally matching `/accounts/{id}/balance`.

---

### The idempotency request_hash — what it protects against

Current idempotency behaviour (without `request_hash`):
- Same key + same payload → 200, returns original transaction ✅
- Same key + **different** payload → **200, silently returns original transaction** ❌

The second case is dangerous. A client might reuse a key accidentally (bug in their ID generator), or a retry might corrupt the request body. The server sees a known key and returns `200 OK` — the client thinks it succeeded with the new payload, but the ledger has the original one. Silent data discrepancy.

**The fix**: at write time, compute a SHA-256 hash of the canonical entry set (entries sorted by `account_id + amount`, deterministic) and store it in a `request_hash` column on `transactions`. On replay, recompute the hash from the incoming payload and compare. If they differ:

```json
409 Conflict
{ "error": "idempotency key reused with a different payload — suspected client bug" }
```

This turns a silent failure into a loud, debuggable error. The client is forced to either use a new key or fix their payload. `409 Conflict` is the correct HTTP status — the request is well-formed, but it conflicts with the server's stored state for that key.

**Why SHA-256 and not a direct equality check on the entries?**
- The hash is O(1) to store (fixed 64-char hex string regardless of how many entries)
- Comparison is O(1) string equality
- No need to store the full original request body; the hash is sufficient to detect any change

**One-liner for interviews:**
> "Idempotency keys protect against duplicate submission. But without a payload hash, the server can't tell if the client reused a key with a different amount — it just silently returns the original transaction. By storing a SHA-256 of the entry set at write time and comparing on replay, we turn that silent discrepancy into an explicit 409 Conflict. Every serious payments API does this — Stripe calls it 'idempotency key conflict'."

---

### Deterministic Hashing: How `computeRequestHash` Works

To make payload verification robust, the hash must represent the *semantic meaning* of the transaction, not just the raw bytes of the HTTP request. 

Consider a client sending a payment between Account A and Account B. A network retry might send the exact same entries, but in a different order:
1. `[{Account: B, Amount: 100}, {Account: A, Amount: -100}]`
2. `[{Account: A, Amount: -100}, {Account: B, Amount: 100}]`

These are mathematically identical. If we just hashed the raw JSON request body, the hashes would differ, resulting in a false-positive `409 Conflict`.

To prevent this, `computeRequestHash` uses **canonicalization**:

```go
func computeRequestHash(entries []EntryInput) string {
	sorted := make([]EntryInput, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		idI, idJ := sorted[i].AccountID.String(), sorted[j].AccountID.String()
		if idI != idJ {
			return idI < idJ
		}
		return sorted[i].Amount < sorted[j].Amount
	})
    // ...
```

1. **Copy & Sort**: It creates a copy of the slice to avoid mutating the original request data, then sorts it deterministically (first by Account ID, then by Amount). This guarantees that equivalent transactions always resolve to the exact same order before hashing.
2. **Direct Hashing**: Instead of marshalling back to JSON, it streams the sorted data directly into a SHA-256 writer: `fmt.Fprintf(hash, "%s:%d;", e.AccountID, e.Amount)`. This avoids JSON reflection overhead and ensures a strict, unambiguous byte representation.
3. **Hex Encoding**: The raw 32-byte hash is converted to a 64-character hex string for easy, human-readable storage in the `request_hash` database column.

---

