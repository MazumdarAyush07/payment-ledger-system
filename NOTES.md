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
