package orchestrator

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestFormatPlain_IncludesStepWhenSet(t *testing.T) {
	ts := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	rec := sparkwing.LogRecord{
		TS:    ts,
		Level: "info",
		JobID: "deploy",
		Step:  "canary",
		Msg:   "rolling 5%",
	}
	out := formatPlain(rec)
	if !strings.Contains(out, "deploy/canary") {
		t.Errorf("missing node/step prefix in %q", out)
	}
}

func TestFormatPlain_NodeOnlyWhenNoStep(t *testing.T) {
	ts := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	rec := sparkwing.LogRecord{
		TS:    ts,
		Level: "info",
		JobID: "build",
		Msg:   "starting",
	}
	out := formatPlain(rec)
	if strings.Contains(out, "build/") {
		t.Errorf("step prefix should not appear when Step is empty: %q", out)
	}
	if !strings.Contains(out, " build ") {
		t.Errorf("expected ' build ' in %q", out)
	}
}

func TestRenderJSONLStreamDropsC1AndKeepsCommandEcho(t *testing.T) {
	hostile := "ok\u009b2J\u009b1;1H\u009d0;PWNED\u009c\x1b[2J\x1bc"
	var file bytes.Buffer
	enc := json.NewEncoder(&file)
	for _, rec := range []sparkwing.LogRecord{
		{Level: "info", JobID: "build", Event: "exec_line", Msg: hostile},
		{Level: "info", JobID: "build", Event: "exec_start", Msg: "$ set -e\necho hello"},
	} {
		if err := enc.Encode(&rec); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name string
		opts LogsOpts
	}{
		{name: "pretty", opts: LogsOpts{}},
		{name: "plain", opts: LogsOpts{Format: "plain"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := renderJSONLStream(bytes.NewReader(file.Bytes()), tc.opts, &out); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			for _, r := range got {
				if r >= 0x80 && r <= 0x9f {
					t.Fatalf("C1 control reached the terminal: %q", got)
				}
			}
			if strings.Contains(got, "\x1b]") || strings.Contains(got, "\x1bc") || strings.Contains(got, "\x1b[2J") {
				t.Fatalf("non-SGR escape reached the terminal: %q", got)
			}
			if !strings.Contains(got, "$ set -e") || !strings.Contains(got, "\n    echo hello") {
				t.Fatalf("multi-line command echo was mangled: %q", got)
			}
		})
	}
}
