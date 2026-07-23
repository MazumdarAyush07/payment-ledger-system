# Data Contracts — Payment Ledger System

> This document defines the canonical data model for the payment ledger.
> All schema design and Go code must conform to these contracts.
> Written before any schema or code — intentionally.

---

## 1. Core Entities

### Account
An **Account** is a named financial container that holds a running balance.
Every credit or debit in the system must be tied to an account.

| Field          | Type      | Description                                              |
|----------------|-----------|----------------------------------------------------------|
| `id`           | UUID      | Globally unique identifier (generated server-side)       |
| `name`         | string    | Human-readable label (e.g. "Ayush's Wallet", "Revenue") |
| `account_type` | enum      | See account type definitions below                       |
| `currency`     | string    | ISO 4217 currency code (e.g. `INR`, `USD`)               |
| `created_at`   | timestamp | UTC timestamp of account creation                        |

An account has **no balance column**. Its balance is always derived by summing its entries.

---

### Transaction
A **Transaction** is an atomic financial event. It groups two or more entries that collectively represent a complete transfer of value.

A transaction is the "header" — it carries metadata but no amounts itself.
The actual money movement lives in the entries it owns.

| Field              | Type      | Description                                                                 |
|--------------------|-----------|-----------------------------------------------------------------------------|
| `id`               | UUID      | Globally unique identifier                                                  |
| `idempotency_key`  | string    | Client-provided unique key to prevent duplicate processing (see Section 4)  |
| `request_hash`     | string    | SHA-256 hash of request payload for detecting idempotency mismatch (409)    |
| `description`      | string    | Human-readable reason (e.g. "Payment for order #1042")                      |
| `status`           | enum      | `pending` → `posted` → `failed` (see status lifecycle below)                |
| `created_at`       | timestamp | UTC timestamp: when the transaction was **received** (transaction time)      |
| `posted_at`        | timestamp | UTC timestamp: when the transaction was **committed to the ledger**          |
| `exchange_rate`    | float64   | *(nullable)* Rate used for conversion. `NULL` for same-currency transactions |
| `rate_source`      | enum      | *(nullable)* `live` or `stale_cache`. `NULL` for same-currency transactions  |

**Status lifecycle:**
```
pending  →  posted    (happy path: all entries inserted atomically)
pending  →  failed    (any validation or DB error; entries never land)
```
A `failed` transaction is a terminal state — it is never retried. The client must issue a new request with a new idempotency key.

---

### Entry
An **Entry** is a single debit or credit line on one account. Every transaction must produce at least **two entries**. Entries are **immutable once written** — they are never updated or deleted.

| Field            | Type      | Description                                                                 |
|------------------|-----------|-----------------------------------------------------------------------------|
| `id`             | UUID      | Globally unique identifier                                                  |
| `transaction_id` | UUID (FK) | The transaction this entry belongs to                                        |
| `account_id`     | UUID (FK) | The account being debited or credited                                        |
| `amount`         | int64     | Signed integer in minor units. **Positive = Debit. Negative = Credit.**      |
| `created_at`     | timestamp | UTC timestamp — always equals the parent transaction's `posted_at`           |

> **Why a signed integer instead of a separate debit/credit column?**
> A signed `amount` makes the double-entry invariant a single SQL expression:
> `SUM(amount) = 0`. Two separate columns would require more complex validation logic.

---

## 2. The Double-Entry Invariant

**In plain English:**

> For every transaction posted to the ledger, the sum of all entry amounts must equal exactly zero.

If money leaves one account, it must arrive in another. There is no such thing as money appearing from nowhere or vanishing. Every transaction is financially balanced by construction.

**Example — a ₹500 payment from a customer wallet to a merchant account:**

| Entry | Account         | Amount  | Direction |
|-------|-----------------|---------|-----------|
| 1     | Customer Wallet | -50000  | Credit    |
| 2     | Merchant Account| +50000  | Debit     |
| **Σ** |                 | **0**   |           |

**How the system enforces this:**

The ledger engine validates `SUM(entry.amount) == 0` **before** any database write.
This validation is a pure Go function — testable in complete isolation from the database.
If validation fails, the transaction is rejected with `422 Unprocessable Entity`. Nothing is written.

---

## 3. Account Types

The `account_type` enum determines the **normal balance direction** of an account and how its balance is interpreted. We use a simplified 5-type set covering standard double-entry accounting.

| Account Type  | Normal Balance | Meaning                                           | Examples                       |
|---------------|----------------|---------------------------------------------------|--------------------------------|
| `asset`       | Debit (+)      | Things the business owns                          | Customer wallets, Bank accounts|
| `liability`   | Credit (-)     | Things the business owes                          | Merchant payouts pending       |
| `equity`      | Credit (-)     | Owner's residual claim on assets                  | Retained earnings              |
| `revenue`     | Credit (-)     | Income earned by the business                     | Transaction fees collected     |
| `expense`     | Debit (+)      | Costs incurred by the business                    | Refunds, chargebacks           |

**What "balance" means per type:**

All balances are computed identically: `SUM(amount) FROM entries WHERE account_id = X`.

- For **asset** and **expense** accounts, a **positive** sum means the account is in its normal, healthy state (e.g. "Customer has ₹5,000 in their wallet").
- For **liability**, **equity**, and **revenue** accounts, a **negative** sum means the account is in its normal state (e.g. "We owe the merchant ₹5,000").

This is standard accounting convention. The raw number from the DB is unambiguous — interpretation depends on the account type when displaying to users.

---

## 4. Idempotency Key

### What it is
An **idempotency key** is a unique string provided by the API client on every `POST /transactions` request. It represents the client's intent to perform a specific financial operation **exactly once**.

```
POST /transactions
Idempotency-Key: order_1042_payment_attempt_1
```

### What it solves
Networks are unreliable. A client sends a payment request, the server processes it and commits it to the database, but the response is lost in transit. The client, receiving no confirmation, retries the request. Without idempotency, the customer is charged twice.

### Exactly where it is checked
1. The client sends `Idempotency-Key` as an HTTP header.
2. The API layer extracts it and passes it to the ledger engine as part of the request.
3. The ledger engine attempts `INSERT INTO transactions (idempotency_key, ...)`.
4. PostgreSQL's `UNIQUE` constraint on `transactions.idempotency_key` is the **actual safety net**.
5. If a duplicate key arrives:
   - PostgreSQL returns error code `23505` (unique_violation).
   - The engine catches this specific error, fetches and returns the **original transaction**.
   - The client receives `200 OK` with the original result — indistinguishable from a fresh success.
6. If no duplicate: the transaction proceeds normally and returns `201 Created`.

### Why DB-level enforcement, not app-level?
An application-level check (`SELECT ... WHERE idempotency_key = ?` before inserting) has a race condition: two concurrent requests can both pass the SELECT check before either INSERT completes. The database's unique constraint is atomic and cannot be bypassed by concurrency.

**Scope:** Idempotency keys are scoped to `transactions` only, not individual entries. One key = one transaction = two or more entries.

---

## 5. Currency Handling — Integer Minor Units

### The rule
All monetary amounts are stored and processed as **`int64` integers representing minor units**.

- `₹ 100.50` → stored as `10050` (paise)
- `$ 9.99` → stored as `999` (cents)

### Why not floats?
IEEE 754 binary floating-point cannot represent most decimal fractions exactly.

```
0.1 + 0.2 = 0.30000000000000004  ← in every language using float64
```

In a payment system processing millions of transactions, these rounding errors compound. A ledger that is off by a single paisa per transaction will fail audits and cannot be reconciled with bank statements.

### Why not `NUMERIC` or `DECIMAL`?
`NUMERIC(15,2)` in PostgreSQL is exact, but:
- Slower than integer arithmetic on modern CPUs
- Requires arbitrary-precision math libraries in Go, adding complexity
- Adds serialization/deserialization overhead between Go and the DB

Integer minor units are **exact, fast, simple, and the industry standard** (Stripe, Razorpay, Braintree all store amounts in minor units).

### Conversion responsibility
Conversion between human-readable decimals and minor units is the responsibility of the **API layer only** — the ledger engine never sees decimals.

---

## 6. Timestamp Standard

### Rule: All timestamps are UTC
No exceptions. The database stores all timestamps in UTC. The API accepts and returns timestamps in **ISO 8601 UTC format** (`2026-06-28T14:30:00Z`).

Timezone conversion is a display concern — handled client-side, never server-side.

### Transaction Time vs. Posted Time

| Field        | Column        | Meaning                                                                      |
|--------------|---------------|------------------------------------------------------------------------------|
| Transaction Time | `created_at` | When the `POST /transactions` request was **received** by the server.    |
| Posted Time      | `posted_at`  | When the transaction was **atomically committed** to the ledger (COMMIT). |

**Why the distinction matters:**
- Under normal load: these timestamps are milliseconds apart.
- Under failure: a transaction can be `created` (received) but never `posted` (failed before commit). The gap between `created_at` and `posted_at` is meaningful for debugging and audit trails.
- For reporting and reconciliation: always use `posted_at`. It represents when value actually moved.

---

## Entity Relationship Summary

```
accounts (1) ──────────── (many) entries
transactions (1) ────── (many) entries

One Transaction → many Entries
One Account → many Entries (across many Transactions)
An Entry belongs to exactly one Transaction and exactly one Account
```

**The core invariant (restated as a SQL assertion):**
```sql
SELECT SUM(amount) FROM entries WHERE transaction_id = $1;
-- Must always equal 0 for every valid transaction_id.
```

---

## 7. Currency Conversion Contract

### When conversion applies
Conversion only occurs when a transaction spans accounts with **different currencies**
(e.g., a USD source account paying into an INR destination account).
Same-currency transactions bypass the conversion module entirely.

### Conversion is a pre-processing step
The ledger engine has no knowledge of exchange rates. Conversion happens in the **API layer**
before entries are constructed. By the time the ledger engine receives entries, all amounts
are already in the correct target currency and the double-entry invariant still holds.

### Rate fetching strategy (layered fallback)

```
1. Try frankfurter.app  (timeout: 3s)
   ✔ Success  → cache for 1 hour  → use rate  (rate_source = "live")
   ✘ Failure   → go to step 2

2. Check in-memory cache
   ✔ Cache age < 24 hours  → use stale rate  (rate_source = "stale_cache")
   ✘ Cache too old or empty → go to step 3

3. Hard fail → 503 Service Unavailable
   {"error": "Exchange rate service unavailable. No recent rate found. Retry later."}
```

### What gets stored on the transaction
- `exchange_rate`: the float64 rate used (e.g., `83.50` for USD→INR)
- `rate_source`: `"live"` or `"stale_cache"`

This ensures every cross-currency transaction is **fully auditable** — you can always reproduce
exactly what conversion was applied and whether it came from a live or cached source.

### External API
- **Provider:** `frankfurter.app` (European Central Bank data, free, no API key required)
- **Endpoint:** `GET https://api.frankfurter.app/latest?from={FROM}&to={TO}`
- **Response example:**
  ```json
  { "amount": 1.0, "base": "USD", "date": "2026-06-28", "rates": { "INR": 83.50 } }
  ```

### Conversion function contract
```go
// Convert converts `amount` (in minor units of `from` currency) to minor units of `to` currency.
// Returns the converted amount, the Rate used, and any error.
func Convert(amount int64, from, to string) (int64, Rate, error)

type Rate struct {
    From      string
    To        string
    Rate      float64
    FetchedAt time.Time
    Source    RateSource // "live" | "stale_cache"
}
```

> **Note on float64 for the rate itself:** The exchange rate (e.g., `83.50`) is stored as
> `float64` because it is a ratio, not a monetary amount. The converted monetary amount is
> immediately rounded to an `int64` minor unit — no float arithmetic ever represents money.
