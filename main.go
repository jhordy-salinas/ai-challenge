package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/yuno/ai-challenge/internal/handler"
	"github.com/yuno/ai-challenge/internal/health"
	"github.com/yuno/ai-challenge/internal/processor"
	"github.com/yuno/ai-challenge/internal/retry"
	"github.com/yuno/ai-challenge/internal/router"
	"github.com/yuno/ai-challenge/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	s := store.New()
	sim := processor.NewRealisticSimulator()
	tracker := health.NewTracker(5 * time.Minute)
	engine := retry.NewEngine(s, sim, tracker, nil, logger)
	h := handler.New(engine, s, tracker, []string{"StripeLatam", "PayUSouth", "EbanxBR"})
	r := router.New(h, logger)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	logger.Info("server starting", "port", port)
	fmt.Fprintf(os.Stderr, "CloudMarket Retry Orchestrator listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
