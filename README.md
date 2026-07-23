# Payment Ledger System

A **production-grade, concurrent-safe double-entry payment ledger API** built in Go. Designed to demonstrate the financial systems engineering patterns used at companies like Stripe, Square, and Robinhood.

This project focuses on **correctness**, **idempotency**, and **concurrency safety** over feature bloat.

---

## 🌟 Key Features

1. **Strict Double-Entry Invariants:** Every transaction creates balanced debits and credits. Money is never mutated; it is immutably recorded. The system mathematically enforces `∑ entries = 0` at the database level.
2. **Database-Enforced Idempotency:** The same payment request sent twice (e.g., due to network retries) produces exactly one transaction. Enforced via PostgreSQL `UNIQUE` constraints to eliminate time-of-check to time-of-use (TOCTOU) race conditions.
3. **Payload Mismatch Detection:** Reusing an idempotency key with a *different* payload computes a deterministic SHA-256 hash mismatch, rejecting the request with a `409 Conflict` instead of silently corrupting data.
4. **Cross-Currency Support with Layered Caching:** Seamlessly transfer between different currencies. Exchange rates are fetched from a live provider, cached in-memory for 1 hour, and fall back to stale data for up to 24 hours if the provider goes down.
5. **Exact Integer Arithmetic:** All monetary amounts are stored as `int64` minor units (cents/paise). No floating-point math, no rounding drift.
6. **Dynamic Derived Balances:** Account balances are computed dynamically (`SUM(amount)`). There is no mutable `balance` column, completely eliminating row-lock contention (`SELECT FOR UPDATE`) on high-velocity accounts (like a corporate treasury).

---

## 🛠 Tech Stack

- **Language:** Go 1.22
- **Database:** PostgreSQL 16
- **Routing:** `go-chi/chi/v5`
- **DB Driver:** `jackc/pgx/v5` (pgxpool for high concurrency)
- **Migrations:** `pressly/goose/v3`
- **Deployment:** Docker & Docker Compose

---

## 🚀 How to Run Locally

You can spin up the entire application (API + Postgres) with a single command.

### Prerequisites
- [Docker & Docker Compose](https://www.docker.com/products/docker-desktop/)

### Quick Start
```bash
# 1. Clone the repository
git clone https://github.com/ayushmazumdar/payment-ledger.git
cd payment-ledger-system

# 2. Start the API and Database
docker-compose up --build -d
```
The API will be available at `http://localhost:8080`. Migrations are applied automatically on startup.

*(Alternatively, to run natively: spin up Postgres via `docker-compose`, copy `.env.example` to `.env`, and run `go run cmd/server/main.go`)*

---

## 📖 API Usage & Examples

### 1. Create Accounts
```bash
curl -X POST http://localhost:8080/accounts \
  -H "Content-Type: application/json" \
  -d '{"name": "Alice Wallet", "account_type": "asset", "currency": "USD"}'

# Returns: {"id": "uuid-1", ...}

curl -X POST http://localhost:8080/accounts \
  -H "Content-Type: application/json" \
  -d '{"name": "Bob Wallet", "account_type": "asset", "currency": "INR"}'

# Returns: {"id": "uuid-2", ...}
```

### 2. Post a Cross-Currency Transaction
*Notice the `Idempotency-Key` header. This request will automatically fetch the live USD→INR exchange rate and calculate the exact INR credit.*

```bash
curl -X POST http://localhost:8080/transactions \
  -H "Idempotency-Key: pay-12345" \
  -H "Content-Type: application/json" \
  -d '{
    "description": "Payment from Alice to Bob",
    "from_currency": "USD",
    "to_currency": "INR",
    "entries": [
      {"account_id": "<uuid-1>", "amount": -1000}, 
      {"account_id": "<uuid-2>", "amount": 1000}
    ]
  }'
```

### 3. Check Account Balance
```bash
curl http://localhost:8080/accounts/<uuid-2>/balance
```

---

## 🧠 Core Design Decisions

1. **Why integer minor units?**
   IEEE 754 floating point can't represent `0.1` exactly. Across thousands of transactions, rounding errors compound. Storing `$10.50` as `1050` (cents) means all arithmetic is integer arithmetic — no precision loss ever.

2. **Why derive balance instead of storing it?**
   A stored `balance` column is a cache. Caches go stale. Updating it requires `SELECT FOR UPDATE` locks. If 10,000 customers pay Amazon at once, locking the `Amazon` account row will serialize the database and bring throughput to a halt. Deriving via `SUM(entries)` is lock-free for writes.

3. **Why enforce idempotency at the DB layer?**
   App-layer checks (`if exists, return`) suffer from race conditions. Two concurrent requests with the same key can bypass the check before either commits. By relying on a `UNIQUE` constraint, we push the synchronization problem down to the storage engine (Postgres), which guarantees atomicity.

4. **Why split pure tests from integration tests?**
   The core validation logic (`ValidateBalance`, `computeRequestHash`) is pure Go. These tests run instantly without a database. The stateful logic (`PostTransaction`) runs integration tests against a real Postgres container. We never use SQLite for testing because its constraint and locking behaviors differ wildly from Postgres.

---

## 📚 Further Reading
- [Architecture & Data Model Diagram](docs/ARCHITECTURE_AND_DESIGN.md)
- [Data Contracts (Schema Definitions)](docs/data_contracts.md)
- [Technical Execution Workflow (How this was built)](docs/TECH_EXECUTION_WORKFLOW.md)
- [Engineering Notes (Concurrency patterns & Idempotency deep dives)](NOTES.md)
