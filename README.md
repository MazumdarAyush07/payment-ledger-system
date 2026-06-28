# Payment Ledger System

A **production-grade, double-entry payment ledger backend** built in Go — designed to demonstrate the financial systems engineering patterns used at companies like Stripe, Square, and Robinhood.

> This is a portfolio project focused on **correctness**, **idempotency**, and **concurrency safety** — not a hosted product.

---

## What Is This?

A payment ledger is the core financial primitive behind every payments product. This system implements:

- **Double-Entry Accounting** — every transaction creates balanced debits and credits. Money is never "mutated"; it is immutably recorded. The system enforces: `∑ entries = 0` for every transaction.
- **Idempotency** — the same payment request sent twice (due to network retries) produces exactly one transaction, never two. Enforced at the database constraint level, not just application logic.
- **Exact Integer Arithmetic** — all monetary amounts stored as `int64` minor units (cents/paise). No floats. No rounding errors.
- **Derived Balances** — account balances are computed dynamically from entry history (`SUM(amount)`), not from a mutable balance column. Eliminates lock contention and sync bugs.

---

## Tech Stack

| Layer | Technology |
|---|---|
| Language | Go 1.22+ |
| Database | PostgreSQL 16 |
| DB Driver | `jackc/pgx/v5` (pgxpool) |
| HTTP Router | `go-chi/chi/v5` |
| Migrations | `pressly/goose/v3` |
| Config | `joho/godotenv` |

---

## How to Run Locally

### Prerequisites
- [Go 1.22+](https://go.dev/dl/)
- [Docker](https://www.docker.com/products/docker-desktop/)

### 1. Clone the repo
```bash
git clone https://github.com/ayushmazumdar/payment-ledger.git
cd payment-ledger-system
```

### 2. Set up environment
```bash
cp .env.example .env
# Edit .env if you need to change ports or credentials
```

### 3. Start PostgreSQL
```bash
docker-compose up -d
```

### 4. Run the server
```bash
go run cmd/server/main.go
```

The server starts on `http://localhost:8080`.

---

## API Overview

> Full documentation coming in Phase 7.

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/accounts` | Create a new ledger account |
| `GET` | `/accounts/{id}/balance` | Get current account balance |
| `GET` | `/accounts/{id}/statement` | Get paginated transaction history |
| `POST` | `/transactions` | Post a double-entry transaction |
| `GET` | `/transactions/{id}` | Fetch a transaction and its entries |

All `POST /transactions` requests require an `Idempotency-Key` header.

---

## Architecture

See [`docs/ARCHITECTURE_AND_DESIGN.md`](docs/ARCHITECTURE_AND_DESIGN.md) for full architecture details, trade-off decisions, and data model diagrams.

---

## Why This Project?

> This section will be fleshed out in Phase 7 with design decisions and lessons learned.

Short answer: most backend portfolio projects are CRUD apps over a single table. A payment ledger requires you to reason about **atomicity**, **race conditions**, **financial invariants**, and **API contract design** — the exact skills that matter for backend roles at fintech companies.
