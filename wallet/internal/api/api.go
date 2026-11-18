// Package api is the HTTP surface: five operations, spelled exactly as the
// brief spells them — no version prefix, no base path.
//
// The webhook endpoint is mounted here but implemented in internal/ingest,
// because what happens to a provider event is one subject and serving HTTP is
// another.
package api

import (
	"log/slog"
	"net/http"

	"wallet/internal/store"
)

// Server holds what the handlers need.
type Server struct {
	store  *store.Store
	logger *slog.Logger
}

// New returns the service's handler. webhook is the provider endpoint —
// ingest.NewHandler — and may be nil in tests that do not exercise it.
func New(st *store.Store, webhook http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{store: st, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /wallets/{user_id}", s.getWallet)
	mux.HandleFunc("POST /wallets/{user_id}/spend", s.spend)
	mux.HandleFunc("GET /wallets/{user_id}/entries", s.getEntries)
	if webhook != nil {
		mux.Handle("POST /webhooks/payments", webhook)
	}

	return withRequestID(withLogging(mux, logger))
}

// health is cheap and dependency-free: it answers while the database is down,
// which is what makes it usable as the warm-up request a concurrency scenario
// fires before a race. It carries no configuration and no secret.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// storageUnavailable is the answer when the database could not be reached. It
// is deliberately the same answer for every such failure: this service does not
// know whether the work happened, and saying so is what keeps a payment alive.
func (s *Server) storageUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("storage unavailable",
		"path", r.URL.Path,
		"request_id", RequestID(r.Context()),
		"err", err.Error(),
	)
	writeError(w, http.StatusServiceUnavailable, codeStorageUnavailable)
}
