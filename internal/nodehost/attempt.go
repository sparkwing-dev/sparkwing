package nodehost

import (
	"bytes"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func buildSessionEnvironment(cfg SessionConfig, kind procenv.Kind, runtime procenv.Runtime) ([]string, error) {
	base := cfg.Env
	if base == nil {
		base = engineenv.Minimal()
	}
	return procenv.Build(kind, procenv.Base{Local: true, Captured: base, Masker: cfg.Masker}, runtime)
}

// LineWriter masks what is written to it as one text stream and hands each
// line to a [LogSink] at one level; a line that grows past the session's line
// limit goes in pieces, masked whole across them. Call Flush once writing
// ends. It is unlocked, so one goroutine writes at a time.
type LineWriter struct {
	sink   interface{ Text(string, string) }
	level  string
	stream *secrets.Stream
	buf    []byte
}

// NewLineWriter returns a writer whose lines reach sink at level, masked with masker.
func NewLineWriter(sink *LogSink, level string, masker *secrets.Masker) *LineWriter {
	return &LineWriter{sink: sink, level: level, stream: masker.Stream()}
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.sink.Text(w.level, w.stream.Mask(strings.TrimSuffix(string(w.buf[:i]), "\r"), true))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) >= maxTextLine {
		w.sink.Text(w.level, w.stream.Mask(string(w.buf), false))
		w.buf = nil
	}
	return len(p), nil
}

// Flush hands the unterminated rest, and any tail held for it, to the sink.
func (w *LineWriter) Flush() {
	w.sink.Text(w.level, w.stream.Mask(string(w.buf), true))
	w.buf = nil
}
