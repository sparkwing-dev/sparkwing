package opsview

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DisplayText quotes terminal controls and bidi formatting in untrusted human-output fields.
// An optional positive byte limit adds an ellipsis without splitting a UTF-8 rune.
// For example, DisplayText("ok\rforged") returns `"ok\rforged"`.
func DisplayText(value string, maxBytes ...int) string {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029'
	}) {
		value = strconv.Quote(value)
	}
	if len(maxBytes) == 0 || maxBytes[0] <= 0 || len(value) <= maxBytes[0] {
		return value
	}
	suffix := strings.Repeat(".", min(3, maxBytes[0]))
	end := maxBytes[0] - len(suffix)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix
}

// DisplayLines splits untrusted multi-line text into lines for a terminal, so a
// newline prints as a line break while every other control stays quoted by
// DisplayText. Callers indent continuation lines so no line can pose as the
// CLI's own output. A trailing newline adds no empty line, and an optional
// positive byte limit applies to each line.
// For example, DisplayLines("exit 1\nFAIL") returns []string{"exit 1", "FAIL"}.
func DisplayLines(value string, maxBytes ...int) []string {
	value = strings.TrimRight(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = DisplayText(line, maxBytes...)
	}
	return lines
}

// DisplayBlock renders DisplayLines with the first line as given and later lines
// prefixed by indent, ready to follow a label such as "sparkwing: run: ".
// For example, DisplayBlock("a\nb", "  ") returns "a\n  b".
func DisplayBlock(value, indent string, maxBytes ...int) string {
	return strings.Join(DisplayLines(value, maxBytes...), "\n"+indent)
}
