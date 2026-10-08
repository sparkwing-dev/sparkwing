package logutil

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Flag names every service registers for its own log.
const (
	FormatFlag = "log-format"
	LevelFlag  = "log-level"
)

// Options picks how a service writes its own log: Format is text or json,
// Level is debug, info, warn or error.
type Options struct {
	Format string
	Level  string
}

type stringFlags interface {
	String(name, value, usage string) *string
}

// Bind registers --log-format and --log-level on fs, which may be a
// standard library or a pflag flag set, and returns the reader of the parsed
// values.
func Bind(fs stringFlags) func() Options {
	format := fs.String(FormatFlag, "text", "format of this service's own log: text or json")
	level := fs.String(LevelFlag, "info", "lowest level this service logs: debug, info, warn or error")
	return func() Options { return Options{Format: *format, Level: *level} }
}

// Handler builds the slog handler o names, writing to w. It refuses a format
// or level it does not know rather than logging in one nobody asked for.
func (o Options) Handler(w io.Writer) (slog.Handler, error) {
	var level slog.Level
	switch o.Level {
	case "debug":
		level = slog.LevelDebug
	case "", "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("--%s %q: want debug, info, warn or error", LevelFlag, o.Level)
	}
	opts := &slog.HandlerOptions{Level: level}
	switch o.Format {
	case "", "text":
		return slog.NewTextHandler(w, opts), nil
	case "json":
		return slog.NewJSONHandler(w, opts), nil
	default:
		return nil, fmt.Errorf("--%s %q: want text or json", FormatFlag, o.Format)
	}
}

// Bridge returns the writer the standard library log package writes through,
// so a log.Printf line lands in logger at the level its prefix names.
func Bridge(logger *slog.Logger) io.Writer {
	return &bridgeWriter{logger: logger}
}

type bridgeWriter struct {
	logger *slog.Logger
}

func (w *bridgeWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")
	lower := strings.ToLower(msg)

	switch {
	case strings.HasPrefix(lower, "critical:"),
		strings.HasPrefix(lower, "error:"),
		strings.HasPrefix(lower, "fatal:"):
		w.logger.Error(msg)

	case strings.HasPrefix(lower, "warning:"),
		strings.HasPrefix(lower, "warn:"),
		strings.HasPrefix(lower, "rejected:"):
		w.logger.Warn(msg)

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
