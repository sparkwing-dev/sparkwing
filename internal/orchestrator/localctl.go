package orchestrator

import (
	"log/slog"
	"os"
	"strings"
)

func loopbackLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelWarn}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SPARKWING_LOG_FORMAT")), "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
