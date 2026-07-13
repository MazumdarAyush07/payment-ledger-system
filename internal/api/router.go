package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

/*
NewRouter wires all routes and middleware to a chi router and returns it.
This is the single place where URL structure, middleware stack, and handler
wiring are defined. main.go calls this and passes the result to http.ListenAndServe.
*/
func NewRouter(accounts *AccountHandler, transactions *TransactionHandler) http.Handler {
	r := chi.NewRouter()

	/* Global middleware stack */
	r.Use(middleware.RequestID) // injects X-Request-Id header for tracing
	r.Use(requestLogger)        // structured slog: method, path, status, duration
	r.Use(middleware.Recoverer) // catches panics, returns 500 instead of crashing

	/* Health check — unauthenticated, used by load balancers */
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	/* Account routes */
	r.Post("/accounts", accounts.CreateAccount)
	r.Get("/accounts/{id}/balance", accounts.GetBalance)
	r.Get("/accounts/{id}/statement", accounts.GetStatement)

	/* Transaction routes */
	r.Post("/transactions", transactions.PostTransaction)
	r.Get("/transactions/{id}", transactions.GetTransaction)

	return r
}

/*
requestLogger is a minimal structured request logger middleware using the
standard library's log/slog package. Logs method, path, status, and duration.
*/
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}
