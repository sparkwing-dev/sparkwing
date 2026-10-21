package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// LineWriter masks a complete text stream before emitting its lines.
type LineWriter struct {
	sink   interface{ Text(string, string) }
	level  string
	stream *secrets.Stream
	lines  *secrets.TextStream
}

// Write emits complete lines and retains their masked tail.
func (w *LineWriter) Write(p []byte) (int, error) {
	if w.lines == nil {
		w.lines = w.stream.Lines()
	}
	for _, line := range w.lines.Push(string(p), false) {
		w.sink.Text(w.level, line)
	}
	return len(p), nil
}

func (w *LineWriter) Flush() {
	if w.lines == nil {
		w.lines = w.stream.Lines()
	}
	for _, line := range w.lines.Push("", true) {
		w.sink.Text(w.level, line)
	}
}
