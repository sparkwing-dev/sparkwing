package store

import "strings"

func placeholders(n int) string {
	if n < 1 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}
