package logpretty

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestStripANSIRemovesEveryEscapeSequenceAndControl(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "build ok", want: "build ok"},
		{name: "sgr color", in: "\x1b[31mred\x1b[0m", want: "red"},
		{name: "csi erase display", in: "a\x1b[2Jb", want: "ab"},
		{name: "osc 8 hyperlink", in: "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", want: "click"},
		{name: "osc 0 title with bel", in: "\x1b]0;pwned\apayload", want: "payload"},
		{name: "ris reset", in: "before\x1bcafter", want: "beforeafter"},
		{name: "dcs string", in: "a\x1bPq;stuff\x1b\\b", want: "ab"},
		{name: "apc string", in: "a\x1b_note\x1b\\b", want: "ab"},
		{name: "truncated csi at end", in: "tail\x1b[38;5", want: "tail"},
		{name: "truncated osc at end", in: "tail\x1b]0;title", want: "tail"},
		{name: "lone escape at end", in: "tail\x1b", want: "tail"},
		{name: "tab survives", in: "a\tb", want: "a\tb"},
		{name: "carriage return and bell", in: "a\r\x07b", want: "ab"},
		{name: "newline cannot forge a line", in: "ok\n✔ pipeline passed", want: "ok✔ pipeline passed"},
		{name: "nul and delete", in: "a\x00b\x7fc", want: "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeANSIKeepsOnlyAllowListedSGR(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "allow-listed color", in: "\x1b[31mred\x1b[0m", want: "\x1b[31mred\x1b[0m"},
		{name: "allow-listed attributes", in: "\x1b[1;4mloud\x1b[0m", want: "\x1b[1;4mloud\x1b[0m"},
		{name: "bright color", in: "\x1b[92mok\x1b[0m", want: "\x1b[92mok\x1b[0m"},
		{name: "empty parameters reset", in: "x\x1b[my", want: "x\x1b[0my"},
		{name: "256 color dropped", in: "\x1b[38;5;214mfake\x1b[0m", want: "fake\x1b[0m"},
		{name: "mixed codes keep allow-listed", in: "\x1b[1;38;5;214mfake", want: "\x1b[1mfake"},
		{name: "background color dropped", in: "\x1b[41mred bg", want: "red bg"},
		{name: "csi erase display", in: "a\x1b[2Jb", want: "ab"},
		{name: "osc 8 hyperlink", in: "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", want: "click"},
		{name: "osc 0 title with bel", in: "\x1b]0;pwned\apayload", want: "payload"},
		{name: "ris reset", in: "before\x1bcafter", want: "beforeafter"},
		{name: "truncated sgr at end", in: "tail\x1b[3", want: "tail"},
		{name: "tab survives", in: "a\tb", want: "a\tb"},
		{name: "newline cannot forge a line", in: "ok\n✔ pipeline passed", want: "ok✔ pipeline passed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeANSI(tt.in); got != tt.want {
				t.Fatalf("SanitizeANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPrettyRendererSanitizesPipelineOutput(t *testing.T) {
	hostile := "\x1b]0;pwned\a\x1b]8;;https://evil.example\x1b\\link\x1b\\\x1bc\x1b[2J\x1b[31mred\x1b[0m\n✔ pipeline passed"

	var color bytes.Buffer
	pc := NewPrettyRendererTo(&color, true)
	pc.Emit(sparkwing.LogRecord{Event: "exec_line", JobID: "build", Msg: hostile})
	pc.Flush()
	got := color.String()
	if strings.Contains(got, "\x1b]") || strings.Contains(got, "\x1bc") || strings.Contains(got, "\x1b[2J") {
		t.Fatalf("ansi output kept a non-SGR escape: %q", got)
	}
	if !strings.Contains(got, "\x1b[31mred\x1b[0m") {
		t.Fatalf("ansi output dropped an allow-listed color: %q", got)
	}
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Fatalf("ansi output split one record over several lines: %q", got)
	}

	var plain bytes.Buffer
	pp := NewPrettyRendererTo(&plain, false)
	pp.Emit(sparkwing.LogRecord{Event: "exec_line", JobID: "build", Msg: hostile})
	pp.Flush()
	if strings.ContainsRune(plain.String(), 0x1b) {
		t.Fatalf("plain output kept an escape: %q", plain.String())
	}
	if !strings.Contains(plain.String(), "linkred✔ pipeline passed") {
		t.Fatalf("plain output lost visible text: %q", plain.String())
	}
}

func TestPrettyRendererSanitizesStepError(t *testing.T) {
	var buf bytes.Buffer
	pr := NewPrettyRendererTo(&buf, true)
	pr.Emit(sparkwing.LogRecord{Event: "step_end", JobID: "build", Msg: "test", Attrs: map[string]any{
		"error": "boom\x1b]0;pwned\a\x1bc",
	}})
	pr.Emit(sparkwing.LogRecord{Event: "node_end", JobID: "build", Attrs: map[string]any{"outcome": "failed"}})
	pr.Flush()
	if strings.Contains(buf.String(), "\x1b]") || strings.Contains(buf.String(), "\x1bc") {
		t.Fatalf("step error kept a non-SGR escape: %q", buf.String())
	}
}
