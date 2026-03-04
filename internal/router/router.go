package router

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/yuno/ai-challenge/internal/handler"
	"github.com/yuno/ai-challenge/internal/middleware"
)

// New creates and configures the chi router.
func New(h *handler.Handler, logger *slog.Logger) *chi.Mux {
	r := chi.NewRouter()

	r.Use(chiMiddleware.Recoverer)
	r.Use(chiMiddleware.RequestID)
	r.Use(middleware.RequestLogger(logger))

	r.Post("/transactions", h.CreateTransaction)
	r.Get("/transactions", h.ListTransactions)
	r.Get("/transactions/{id}", h.GetTransaction)
	r.Get("/processors/health", h.GetProcessorHealth)

	return r
}
