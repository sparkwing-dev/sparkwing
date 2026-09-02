package logpretty

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	c1DCSs = "\u0090"
	c1SOSs = "\u0098"
	c1CSIs = "\u009b"
	c1STs  = "\u009c"
	c1OSCs = "\u009d"
	c1PMs  = "\u009e"
	c1APCs = "\u009f"
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
		{name: "newline survives", in: "ok\n✔ pipeline passed", want: "ok\n✔ pipeline passed"},
		{name: "nul and delete", in: "a\x00b\x7fc", want: "abc"},
		{name: "c1 csi as utf8", in: "a" + c1CSIs + "2Jb", want: "ab"},
		{name: "c1 csi as raw byte", in: "a\x9b2Jb", want: "ab"},
		{name: "c1 csi cursor forge", in: "safe" + c1CSIs + "2J" + c1CSIs + "1;1HFORGED", want: "safeFORGED"},
		{name: "c1 osc with c1 terminator", in: "a" + c1OSCs + "0;pwned" + c1STs + "b", want: "ab"},
		{name: "c1 osc with bel", in: "a" + c1OSCs + "0;pwned\ab", want: "ab"},
		{name: "c1 osc as raw bytes", in: "a\x9d0;pwned\x9cb", want: "ab"},
		{name: "c1 dcs", in: "a" + c1DCSs + "0;q" + c1STs + "b", want: "ab"},
		{name: "c1 sos", in: "a" + c1SOSs + "note" + c1STs + "b", want: "ab"},
		{name: "c1 pm", in: "a" + c1PMs + "note" + c1STs + "b", want: "ab"},
		{name: "c1 apc", in: "a" + c1APCs + "note" + c1STs + "b", want: "ab"},
		{name: "c1 without parameters", in: "line1\u0085line2", want: "line1line2"},
		{name: "esc plus multibyte rune", in: "a\x1béb", want: "ab"},
		{name: "esc plus c1 rune", in: "a\x1b" + c1CSIs + "[2Jb", want: "a[2Jb"},
		{name: "truncated osc keeps the next line", in: "\x1b]0;titleHIDDEN\nreal error", want: "\nreal error"},
		{name: "truncated c1 osc keeps the next line", in: c1OSCs + "0;titleHIDDEN\nreal error", want: "\nreal error"},
		{name: "printable unicode survives", in: "a\x07café ✔", want: "acafé ✔"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.in)
			if got != tt.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("StripANSI(%q) = %q, which is not valid UTF-8", tt.in, got)
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
		{name: "compound with an unknown code is dropped whole", in: "\x1b[1;38;5;214mfake", want: "fake"},
		{name: "background color dropped", in: "\x1b[41mred bg", want: "red bg"},
		{name: "csi erase display", in: "a\x1b[2Jb", want: "ab"},
		{name: "osc 8 hyperlink", in: "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", want: "click"},
		{name: "osc 0 title with bel", in: "\x1b]0;pwned\apayload", want: "payload"},
		{name: "ris reset", in: "before\x1bcafter", want: "beforeafter"},
		{name: "truncated sgr at end", in: "tail\x1b[3", want: "tail"},
		{name: "tab survives", in: "a\tb", want: "a\tb"},
		{name: "newline survives", in: "ok\n✔ pipeline passed", want: "ok\n✔ pipeline passed"},
		{name: "zero padded parameters normalize", in: "\x1b[01;34mdir\x1b[00m", want: "\x1b[1;34mdir\x1b[0m"},
		{name: "zero padded color normalizes", in: "\x1b[031mX\x1b[00m", want: "\x1b[31mX\x1b[0m"},
		{name: "256 color whose index is allow-listed is dropped whole", in: "\x1b[38;5;31mX\x1b[0m", want: "X\x1b[0m"},
		{name: "truecolor is dropped whole", in: "\x1b[38;2;1;2;4mX\x1b[0m", want: "X\x1b[0m"},
		{name: "background truecolor is dropped whole", in: "\x1b[48;2;1;2;4mX", want: "X"},
		{name: "truncated extended color is dropped", in: "\x1b[38;5mX", want: "X"},
		{name: "subparameter truecolor is dropped", in: "\x1b[38:2:255:0:0mX", want: "X"},
		{name: "c1 csi sgr is dropped", in: c1CSIs + "38;2;255;0;0mRED" + c1CSIs + "0m", want: "RED"},
		{name: "c1 csi erase display", in: "a" + c1CSIs + "2Jb", want: "ab"},
		{name: "c1 osc title", in: "a" + c1OSCs + "0;pwned" + c1STs + "b", want: "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeANSI(tt.in)
			if got != tt.want {
				t.Fatalf("SanitizeANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("SanitizeANSI(%q) = %q, which is not valid UTF-8", tt.in, got)
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
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n")[1:] {
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("ansi continuation line is not indented: %q", got)
		}
	}

	var plain bytes.Buffer
	pp := NewPrettyRendererTo(&plain, false)
	pp.Emit(sparkwing.LogRecord{Event: "exec_line", JobID: "build", Msg: hostile})
	pp.Flush()
	if strings.ContainsRune(plain.String(), 0x1b) {
		t.Fatalf("plain output kept an escape: %q", plain.String())
	}
	if !strings.Contains(plain.String(), "linkred") || !strings.Contains(plain.String(), "\n    ✔ pipeline passed") {
		t.Fatalf("plain output lost visible text: %q", plain.String())
	}
}

func TestPrettyRendererKeepsMultiLineCommandEcho(t *testing.T) {
	var buf bytes.Buffer
	pr := NewPrettyRendererTo(&buf, false)
	pr.Emit(sparkwing.LogRecord{Event: "exec_start", JobID: "build", Msg: "$ set -e\necho hello\nmake build"})
	pr.Flush()
	got := buf.String()
	for _, want := range []string{"$ set -e\n", "\n    echo hello\n", "\n    make build\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("command echo lost %q: %q", want, got)
		}
	}
}

func TestPrettyRendererSanitizesBreadcrumbFields(t *testing.T) {
	var buf bytes.Buffer
	pr := NewPrettyRendererTo(&buf, true)
	pr.Emit(sparkwing.LogRecord{
		Event: "exec_line",
		JobID: "node\x1b]0;JOBID-PWNED\a",
		Step:  "step\x1bc" + c1CSIs + "2J",
		Msg:   "ok",
	})
	pr.Emit(sparkwing.LogRecord{Event: "step_skipped", JobID: "node", Msg: "s", Attrs: map[string]any{
		"reason": "cached\x1b]0;REASON-PWNED\a",
	}})
	pr.Emit(sparkwing.LogRecord{Event: "approval_resolved", JobID: "node", Msg: "gate", Attrs: map[string]any{
		"resolution": "approved",
		"via":        "cli\x1bc",
	}})
	pr.Flush()
	got := buf.String()
	if strings.Contains(got, "\x1b]") || strings.Contains(got, "\x1bc") {
		t.Fatalf("renderer kept a non-SGR escape from an attribute: %q", got)
	}
	for _, r := range got {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("renderer kept a C1 control: %q", got)
		}
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
