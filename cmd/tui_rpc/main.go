package main

import (
	"log/slog"
	"os"

	"github.com/nousresearch/hermes-go/pkg/tui_rpc"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	server := tui_rpc.NewRPCServer(logger)

	// Wire up signal handling for graceful shutdown
	shutdownCh := make(chan os.Signal, 1)
	// signal.Notify(shutdownCh, syscall.SIGINT, syscall.SIGTERM) // omit for stdio-only mode

	go func() {
		<-shutdownCh
		server.Shutdown()
	}()

	if err := server.Serve(); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}
