package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderJSONL_FormatsRecord(t *testing.T) {
	in := []byte("{\"ts\":\"2026-05-04T06:03:16.748Z\",\"level\":\"info\",\"node\":\"docker-version\",\"event\":\"exec_line\",\"msg\":\"Docker version 24.0.7\"}\n")
	var out bytes.Buffer
	renderJSONL(in, &out, formatPlain)
	got := out.String()
	if strings.Contains(got, "\"msg\":") || strings.Contains(got, "\"event\":") {
		t.Fatalf("rendered output still looks like JSONL envelope:\n%s", got)
	}
	if !strings.Contains(got, "docker-version") || !strings.Contains(got, "Docker version 24.0.7") {
		t.Fatalf("rendered output missing expected fields:\n%s", got)
	}
}

func TestRenderJSONL_NodeStartEvent(t *testing.T) {
	in := []byte("{\"ts\":\"2026-05-04T06:03:16.330Z\",\"level\":\"info\",\"node\":\"docker-version\",\"event\":\"node_start\",\"msg\":\"docker --version\"}\n")
	var out bytes.Buffer
	renderJSONL(in, &out, formatPlain)
	if !strings.Contains(out.String(), "▶ docker-version") {
		t.Fatalf("expected ▶ banner, got: %q", out.String())
	}
}

func TestRenderJSONL_NonJSONLinesPassThrough(t *testing.T) {
	in := []byte("plain garbage line\n{\"ts\":\"2026-05-04T06:03:16Z\",\"node\":\"n\",\"msg\":\"ok\"}\n")
	var out bytes.Buffer
	renderJSONL(in, &out, formatPlain)
	got := out.String()
	if !strings.Contains(got, "plain garbage line") {
		t.Fatalf("non-JSON line dropped:\n%s", got)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("structured line not rendered:\n%s", got)
	}
}

func TestRenderJSONL_PlainStripsMsgANSI(t *testing.T) {
	in := []byte("{\"ts\":\"2026-05-04T06:03:16Z\",\"node\":\"n\",\"msg\":\"\x1b[31mred text\x1b[0m\"}\n")
	var out bytes.Buffer
	renderJSONL(in, &out, formatPlain)
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Fatalf("plain mode left ANSI in output: %q", out.String())
	}
	if !strings.Contains(out.String(), "red text") {
		t.Fatalf("plain mode dropped Msg content: %q", out.String())
	}
}

func TestRenderJSONL_PlainStripsNonJSONANSI(t *testing.T) {
	in := []byte("\x1b[31mraw red\x1b[0m\n")
	var out bytes.Buffer
	renderJSONL(in, &out, formatPlain)
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Fatalf("plain mode left ANSI in non-JSON line: %q", out.String())
	}
}

func TestRenderJSONL_ANSIKeepsRendererSGRAndMsg(t *testing.T) {
	in := []byte("{\"ts\":\"2026-05-04T06:03:16Z\",\"level\":\"error\",\"node\":\"n\",\"msg\":\"\x1b[31mred\x1b[0m\"}\n")
	var out bytes.Buffer
	renderJSONL(in, &out, formatANSI)
	if !strings.ContainsRune(out.String(), 0x1b) {
		t.Fatalf("ansi mode produced no escapes: %q", out.String())
	}
	if !strings.Contains(out.String(), "red") {
		t.Fatalf("ansi mode dropped Msg text: %q", out.String())
	}
}

func TestNegotiateLogFormat(t *testing.T) {
	type tc struct {
		name   string
		accept string
		query  string
		want   logFormat
	}
	cases := []tc{
		{"empty defaults to plain", "", "", formatPlain},
		{"plain accept", "text/plain", "", formatPlain},
		{"raw accept", "application/x-ndjson", "", formatRaw},
		{"raw with q", "application/x-ndjson, text/plain;q=0.5", "", formatRaw},
		{"ansi accept", "text/x-ansi", "", formatANSI},
		{"query overrides accept (raw)", "text/x-ansi", "raw", formatRaw},
		{"query overrides accept (ansi)", "application/x-ndjson", "ansi", formatANSI},
		{"unknown query falls to plain", "application/x-ndjson", "weird", formatPlain},
		{"ndjson alias", "", "ndjson", formatRaw},
		{"color alias", "", "color", formatANSI},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := "/x"
			if c.query != "" {
				url += "?format=" + c.query
			}
			r := httptest.NewRequest(http.MethodGet, url, nil)
			if c.accept != "" {
				r.Header.Set("Accept", c.accept)
			}
			if got := negotiateLogFormat(r); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestRenderSSELogLine_StructuredRecord(t *testing.T) {
	payload := []byte("{\"ts\":\"2026-05-04T06:03:16Z\",\"level\":\"info\",\"node\":\"n\",\"msg\":\"hello\"}")
	got := renderSSELogLine(payload, formatPlain)
	if len(got) == 0 {
		t.Fatal("expected at least one line")
	}
	if !strings.Contains(got[0], "hello") {
		t.Fatalf("expected rendered line to contain 'hello', got %q", got)
	}
	for _, line := range got {
		if strings.Contains(line, "\n") {
			t.Fatalf("SSE line contains embedded newline: %q", line)
		}
	}
}

func TestRenderSSELogLine_PlainStripsMsgANSI(t *testing.T) {
	payload := []byte("{\"ts\":\"2026-05-04T06:03:16Z\",\"node\":\"n\",\"msg\":\"\x1b[31mred\x1b[0m\"}")
	got := renderSSELogLine(payload, formatPlain)
	for _, line := range got {
		if strings.ContainsRune(line, 0x1b) {
			t.Fatalf("plain SSE line left ANSI: %q", line)
		}
	}
}

func TestRenderSSELogLine_NonJSON(t *testing.T) {
	got := renderSSELogLine([]byte("not json"), formatPlain)
	if len(got) != 1 || got[0] != "not json" {
		t.Fatalf("expected verbatim passthrough, got %q", got)
	}
}

func TestServeLogStreamKeepsNDJSONEventsRaw(t *testing.T) {
	source := "data: {\"ts\":\"2026-08-28T00:00:00Z\",\"level\":\"info\",\"node\":\"verify\",\"event\":\"step_start\",\"msg\":\"tests\"}\n\n" +
		"data: {\"ts\":\"2026-08-28T00:00:01Z\",\"level\":\"info\",\"node\":\"verify\",\"step\":\"tests\",\"event\":\"exec_line\",\"msg\":\"PASS browser\"}\n\n"
	b := &fakeBackend{streamLog: func(runID, nodeID string) (io.ReadCloser, error) {
		if runID != "run-one" || nodeID != "verify" {
			t.Fatalf("stream target = %s/%s", runID, nodeID)
		}
		return io.NopCloser(strings.NewReader(source)), nil
	}}

	raw := httptest.NewRecorder()
	serveLogStream(b, raw, httptest.NewRequest(http.MethodGet, "/stream?format=ndjson", nil), "run-one", "verify")
	if raw.Body.String() != source {
		t.Fatalf("ndjson stream changed event envelopes:\n%s", raw.Body.String())
	}

	ansi := httptest.NewRecorder()
	serveLogStream(b, ansi, httptest.NewRequest(http.MethodGet, "/stream?format=ansi", nil), "run-one", "verify")
	if strings.Contains(ansi.Body.String(), `"event":"step_start"`) {
		t.Fatalf("ansi stream retained the structured event envelope:\n%s", ansi.Body.String())
	}
	if !strings.Contains(ansi.Body.String(), "tests") || !strings.Contains(ansi.Body.String(), "PASS browser") {
		t.Fatalf("ansi stream lost pretty-rendered content:\n%s", ansi.Body.String())
	}
}
