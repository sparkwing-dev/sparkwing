// Package logutil provides structured logging initialization for all sparkwing services.
package logutil

import (
	"log"
	"log/slog"
	"os"
	"strings"
)

// Init configures structured logging for the calling service.
// Safe to call multiple times (idempotent).
func Init() {
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level()}

	if os.Getenv("SPARKWING_LOG_FORMAT") == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)

	// Bridge standard log.Printf to slog with level detection.
	log.SetFlags(0)
	log.SetOutput(&bridgeWriter{logger: logger})
}

func level() slog.Level {
	switch os.Getenv("SPARKWING_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// bridgeWriter routes log.Printf calls through slog with level detection.
// Messages prefixed with known keywords get routed to the appropriate level.
type bridgeWriter struct {
	logger *slog.Logger
}

func (w *bridgeWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")
	lower := strings.ToLower(msg)

	switch {
	// Error-level: critical failures, data loss risks
	case strings.HasPrefix(lower, "critical:"),
		strings.HasPrefix(lower, "error:"),
		strings.HasPrefix(lower, "fatal:"):
		w.logger.Error(msg)

	// Warn-level: degraded state, recoverable issues
	case strings.HasPrefix(lower, "warning:"),
		strings.HasPrefix(lower, "warn:"),
		strings.HasPrefix(lower, "rejected:"):
		w.logger.Warn(msg)

	// Debug-level: noisy per-request/per-tick messages
	case strings.HasPrefix(lower, "heartbeat:"),
		strings.HasPrefix(lower, "cleanup:"),
		strings.HasPrefix(lower, "background fetch:"),
		strings.HasPrefix(lower, "retention:"),
		strings.HasPrefix(lower, "cache hit:"),
		strings.HasPrefix(lower, "poll "),
		strings.HasPrefix(lower, "describe:"),
		strings.HasPrefix(lower, "tags:"),
		strings.HasPrefix(lower, "sync negotiate:"),
		strings.HasPrefix(lower, "seed:"),
		strings.HasPrefix(lower, "git register:"),
		strings.HasPrefix(lower, "auto-register:"),
		strings.HasPrefix(lower, "proxy:"):
		w.logger.Debug(msg)

	default:
		w.logger.Info(msg)
	}
	return len(p), nil
}
