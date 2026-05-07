package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

const (
	mediaTypeNDJSON = "application/x-ndjson"
	mediaTypeANSI   = "text/x-ansi"
	mediaTypePlain  = "text/plain"
)

type logFormat int

const (
	formatPlain logFormat = iota // pretty text, no SGR, Msg ANSI stripped
	formatANSI                   // pretty text + renderer SGR + Msg ANSI passthrough
	formatRaw                    // upstream JSONL bytes, untouched
)

// negotiateLogFormat picks a render mode from `?format=` (when present)
// or the `Accept` header; query takes precedence. Unknown values fall
// back to plain.
func negotiateLogFormat(r *http.Request) logFormat {
	if q := r.URL.Query().Get("format"); q != "" {
		switch q {
		case "raw", "ndjson":
			return formatRaw
		case "ansi", "color":
			return formatANSI
		default:
			return formatPlain
		}
	}
	accept := r.Header.Get("Accept")
	switch {
	case strings.Contains(accept, mediaTypeNDJSON):
		return formatRaw
	case strings.Contains(accept, mediaTypeANSI):
		return formatANSI
	default:
		return formatPlain
	}
}

// contentTypeFor returns the Content-Type header value for a format.
func contentTypeFor(f logFormat) string {
	switch f {
	case formatRaw:
		return mediaTypeNDJSON + "; charset=utf-8"
	case formatANSI:
		return mediaTypeANSI + "; charset=utf-8"
	default:
		return mediaTypePlain + "; charset=utf-8"
	}
}

// renderJSONL pretty-prints a JSONL LogRecord stream into w. Lines that
// don't parse as a LogRecord are written through verbatim (stripped of
// ANSI in plain mode) so a misconfigured pipeline still surfaces.
func renderJSONL(src []byte, w io.Writer, f logFormat) {
	useColor := f == formatANSI
	pr := orchestrator.NewPrettyRendererTo(w, useColor)
	scanner := bufio.NewScanner(bytes.NewReader(src))
	// 1 MiB matches the largest single-line payload observed in CI.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			fmt.Fprintln(w)
			continue
		}
		var rec sparkwing.LogRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if f == formatPlain {
				w.Write([]byte(orchestrator.StripANSI(string(line))))
			} else {
				w.Write(line)
			}
			fmt.Fprintln(w)
			continue
		}
		if f == formatPlain {
			rec.Msg = orchestrator.StripANSI(rec.Msg)
		}
		pr.Emit(rec)
	}
}

// streamPrettySSE re-emits upstream SSE log frames in the chosen render
// format. Each upstream record can produce multiple output lines, so we
// emit one SSE event per output line to keep `data:` values newline-free.
func streamPrettySSE(body io.Reader, w io.Writer, flusher http.Flusher, f logFormat) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		switch {
		case len(raw) == 0:
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		case raw[0] == ':':
			if _, err := fmt.Fprintf(w, "%s\n", raw); err != nil {
				return
			}
			flusher.Flush()
		case bytes.HasPrefix(raw, []byte("data: ")):
			payload := raw[len("data: "):]
			for _, line := range renderSSELogLine(payload, f) {
				if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
					return
				}
			}
		default:
			if _, err := fmt.Fprintf(w, "%s\n", raw); err != nil {
				return
			}
		}
	}
}

// renderSSELogLine converts one JSONL LogRecord into pretty-rendered
// text lines. Returns the input verbatim (stripped in plain mode) when
// the line isn't valid JSON.
func renderSSELogLine(payload []byte, f logFormat) []string {
	var rec sparkwing.LogRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		if f == formatPlain {
			return []string{orchestrator.StripANSI(string(payload))}
		}
		return []string{string(payload)}
	}
	if f == formatPlain {
		rec.Msg = orchestrator.StripANSI(rec.Msg)
	}
	var buf bytes.Buffer
	orchestrator.NewPrettyRendererTo(&buf, f == formatANSI).Emit(rec)
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}
