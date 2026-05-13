package orchestrator

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

// renderMarkdownSummary pretty-prints a markdown blob for terminal
// readers. Headings, emphasis, checklists, and tables get light
// styling; everything else passes through as-is. Each output line is
// indented by prefix so the block visually nests under its node/step
// header.
//
// Color emission auto-disables when stdout isn't a TTY (pkg/color),
// so agents and pipes get plain text -- the styling never bleeds
// into logs.
func renderMarkdownSummary(out io.Writer, prefix, md string) {
	body := strings.TrimRight(md, "\n")
	lines := strings.Split(body, "\n")

	// Group consecutive `|...|` rows so we can re-render them as a
	// tabwriter-aligned block (dropping the `|---|---|` separator).
	var table []string
	flushTable := func() {
		if len(table) == 0 {
			return
		}
		writeMarkdownTable(out, prefix, table)
		table = nil
	}

	for _, raw := range lines {
		line := raw
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "|") && strings.HasSuffix(trim, "|") {
			if !isTableSeparator(trim) {
				table = append(table, trim)
			}
			continue
		}
		flushTable()
		writeMarkdownLine(out, prefix, line)
	}
	flushTable()
}

func isTableSeparator(line string) bool {
	// `|---|---|` shape: every interior char is `-`, `:`, `|`, or space.
	inner := strings.Trim(line, "|")
	if inner == "" {
		return false
	}
	hasDash := false
	for _, r := range inner {
		switch r {
		case '-':
			hasDash = true
		case ':', '|', ' ':
		default:
			return false
		}
	}
	return hasDash
}

func writeMarkdownTable(out io.Writer, prefix string, rows []string) {
	if len(rows) == 0 {
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, row := range rows {
		cells := splitTableRow(row)
		styled := make([]string, len(cells))
		for j, c := range cells {
			s := renderInlineMarkdown(c)
			if i == 0 {
				s = color.Bold(s)
			}
			styled[j] = s
		}
		fmt.Fprintf(tw, "%s%s\n", prefix, strings.Join(styled, "\t"))
	}
	_ = tw.Flush()
}

// splitTableRow splits `| a | b | c |` into ["a", "b", "c"].
func splitTableRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	parts := strings.Split(row, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

var (
	reH3        = regexp.MustCompile(`^(\s*)###\s+(.*)$`)
	reH2        = regexp.MustCompile(`^(\s*)##\s+(.*)$`)
	reH1        = regexp.MustCompile(`^(\s*)#\s+(.*)$`)
	reChecked   = regexp.MustCompile(`^(\s*)-\s+\[[xX]\]\s+(.*)$`)
	reUnchecked = regexp.MustCompile(`^(\s*)-\s+\[\s\]\s+(.*)$`)
	reBullet    = regexp.MustCompile(`^(\s*)-\s+(.*)$`)

	reBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reCode = regexp.MustCompile("`([^`]+)`")
)

func writeMarkdownLine(out io.Writer, prefix, line string) {
	switch {
	case strings.TrimSpace(line) == "":
		fmt.Fprintln(out)
		return
	}
	if m := reH3.FindStringSubmatch(line); m != nil {
		fmt.Fprintf(out, "%s%s%s\n", prefix, m[1], color.Bold(renderInlineMarkdown(m[2])))
		return
	}
	if m := reH2.FindStringSubmatch(line); m != nil {
		fmt.Fprintf(out, "%s%s%s\n", prefix, m[1], color.Bold(renderInlineMarkdown(m[2])))
		return
	}
	if m := reH1.FindStringSubmatch(line); m != nil {
		fmt.Fprintf(out, "%s%s%s\n", prefix, m[1], color.Bold(renderInlineMarkdown(m[2])))
		return
	}
	if m := reChecked.FindStringSubmatch(line); m != nil {
		glyph := color.Green("✓")
		fmt.Fprintf(out, "%s%s%s %s\n", prefix, m[1], glyph, renderInlineMarkdown(m[2]))
		return
	}
	if m := reUnchecked.FindStringSubmatch(line); m != nil {
		glyph := color.Dim("☐")
		fmt.Fprintf(out, "%s%s%s %s\n", prefix, m[1], glyph, renderInlineMarkdown(m[2]))
		return
	}
	if m := reBullet.FindStringSubmatch(line); m != nil {
		fmt.Fprintf(out, "%s%s• %s\n", prefix, m[1], renderInlineMarkdown(m[2]))
		return
	}
	fmt.Fprintf(out, "%s%s\n", prefix, renderInlineMarkdown(line))
}

// renderInlineMarkdown styles `code` and **bold** spans. Order
// matters: code spans are extracted first so their backticked
// contents can't be mistaken for bold delimiters.
func renderInlineMarkdown(s string) string {
	var buf bytes.Buffer
	i := 0
	for i < len(s) {
		switch s[i] {
		case '`':
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				buf.WriteByte(s[i])
				i++
				continue
			}
			buf.WriteString(color.Cyan(s[i+1 : i+1+end]))
			i += end + 2
		case '*':
			if i+1 < len(s) && s[i+1] == '*' {
				end := strings.Index(s[i+2:], "**")
				if end < 0 {
					buf.WriteByte(s[i])
					i++
					continue
				}
				buf.WriteString(color.Bold(s[i+2 : i+2+end]))
				i += end + 4
				continue
			}
			buf.WriteByte(s[i])
			i++
		default:
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}

// colorStatus returns the run-status word with an outcome-tinted
// color. Mirrors the renderer's status palette: success=green,
// failed/cancelled=red, running/pending=cyan, anything else dim.
func colorStatus(status string) string {
	switch status {
	case "success":
		return color.Green(status)
	case "failed", "cancelled":
		return color.Red(status)
	case "running", "pending":
		return color.Cyan(status)
	default:
		return color.Dim(status)
	}
}

// colorOutcome returns a node outcome with the matching tint.
func colorOutcome(outcome string) string {
	switch outcome {
	case "success":
		return color.Green(outcome)
	case "failed":
		return color.Red(outcome)
	case "skipped":
		return color.Dim(outcome)
	case "":
		return "-"
	default:
		return outcome
	}
}

// colorStepGlyph wraps stepGlyph's unicode marker with a
// status-matching color.
func colorStepGlyph(status string) string {
	g := stepGlyph(status)
	switch status {
	case "passed":
		return color.Green(g)
	case "failed":
		return color.Red(g)
	case "skipped":
		return color.Dim(g)
	case "running":
		return color.Cyan(g)
	default:
		return g
	}
}
