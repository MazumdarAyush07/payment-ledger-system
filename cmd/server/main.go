package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/ayushmazumdar/payment-ledger/internal/api"
	"github.com/ayushmazumdar/payment-ledger/internal/currency"
	"github.com/ayushmazumdar/payment-ledger/internal/db"
	"github.com/ayushmazumdar/payment-ledger/internal/ledger"
	"github.com/joho/godotenv"
)

func main() {
	/* Load .env for local development. In production, env vars are injected directly. */
	if err := godotenv.Load(); err != nil {
		slog.Warn("no .env file found, reading environment variables directly")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	/* Step 1: Connect to Postgres */
	ctx := context.Background()
	pool, err := db.ConnectPool(ctx, dbURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("connected to database")

	/* Step 2: Wire the ledger engine */
	engine := ledger.NewService(pool)

	/* Step 3: Wire the currency rate service */
	rateService := currency.NewRateService(nil) // nil → production http.Client with 3s timeout
	slog.Info("currency rate service initialised")

	/* Step 4: Wire the HTTP handlers and router */
	accounts := api.NewAccountHandler(engine)
	transactions := api.NewTransactionHandler(engine, rateService)
	router := api.NewRouter(accounts, transactions)

	/* Step 5: Start the HTTP server */
	addr := ":" + port
	slog.Info("server starting", "addr", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
