package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/logpretty"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	mediaTypeNDJSON = "application/x-ndjson"
	mediaTypeANSI   = "text/x-ansi"
	mediaTypePlain  = "text/plain"
)

type logFormat int

const (
	formatPlain logFormat = iota
	formatANSI
	formatRaw
)

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

func renderJSONL(src []byte, w io.Writer, f logFormat) {
	useColor := f == formatANSI
	pr := logpretty.NewPrettyRendererTo(w, useColor)
	scanner := bufio.NewScanner(bytes.NewReader(src))
	// perf: 1 MiB cap matches the largest single-line payload seen in CI; default 64 KiB drops lines silently.
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
				_, _ = w.Write([]byte(logpretty.StripANSI(string(line))))
			} else {
				_, _ = w.Write(line)
			}
			fmt.Fprintln(w)
			continue
		}
		if f == formatPlain {
			rec.Msg = logpretty.StripANSI(rec.Msg)
		}
		pr.Emit(rec)
	}
	pr.Flush()
}

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

func renderSSELogLine(payload []byte, f logFormat) []string {
	var rec sparkwing.LogRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		if f == formatPlain {
			return []string{logpretty.StripANSI(string(payload))}
		}
		return []string{string(payload)}
	}
	if f == formatPlain {
		rec.Msg = logpretty.StripANSI(rec.Msg)
	}
	var buf bytes.Buffer
	pr := logpretty.NewPrettyRendererTo(&buf, f == formatANSI)
	pr.Emit(rec)
	pr.Flush()
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}
