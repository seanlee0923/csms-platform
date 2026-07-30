package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/seanlee0923/csms-platform/internal/runtime"
)

func main() {
	config, err := runtime.LoadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: config.LogLevel}))
	server, err := runtime.New(config, logger)
	if err != nil {
		logger.Error("create runtime", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("runtime stopped", "error", err)
		os.Exit(1)
	}
}
