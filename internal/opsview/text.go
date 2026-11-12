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
