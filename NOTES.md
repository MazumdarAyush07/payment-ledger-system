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
