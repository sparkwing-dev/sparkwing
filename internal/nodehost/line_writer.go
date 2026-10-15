package nodehost

import (
	"bytes"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// LineWriter masks a complete text stream before emitting its lines.
type LineWriter struct {
	sink   interface{ Text(string, string) }
	level  string
	stream *secrets.Stream
	buf    []byte
}

// Write emits complete lines and retains their masked tail.
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

// Flush emits the final unterminated line.
func (w *LineWriter) Flush() {
	w.sink.Text(w.level, w.stream.Mask(string(w.buf), true))
	w.buf = nil
}
