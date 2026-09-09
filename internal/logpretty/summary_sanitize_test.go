package logpretty

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSummariesSanitizeTerminalControls(t *testing.T) {
	for _, color := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "color"}[color], func(t *testing.T) {
			p := NewPrettyRendererTo(&bytes.Buffer{}, color)
			payload := "\x1b]0;injected\x07\x1b[2J"
			nodes := []any{map[string]any{"id": payload + "build\nforged", "summary": payload + "**summary**\nsecond line", "step_summaries": []any{map[string]any{"step_id": payload + "test\nforged", "summary": payload + "step summary"}}}}
			var out bytes.Buffer
			p.writeRunBlockSummaries(&out, nodes)
			got := out.String()
			if strings.Contains(got, "injected") || strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\nforged") {
				t.Fatalf("unsafe summary: %q", got)
			}
			if !strings.Contains(got, "summary") || !strings.Contains(got, "second line") || !strings.Contains(got, "step summary") {
				t.Fatalf("lost content: %q", got)
			}
		})
	}
}

func TestMarkdownSummarySanitizesTerminalControls(t *testing.T) {
	var out bytes.Buffer
	RenderMarkdownSummary(&out, "  ", "\x1b]0;injected\x07\x1b[2Jsummary")
	if got := out.String(); strings.Contains(got, "injected") || strings.Contains(got, "\x1b[2J") {
		t.Fatalf("unsafe summary: %q", got)
	}
}
