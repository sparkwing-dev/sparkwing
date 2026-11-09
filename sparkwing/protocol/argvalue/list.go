package argvalue

import "strings"

// List applies the same comma and Unicode whitespace rules as []string arguments.
func List(raw string) []string {
	if raw == "" {
		return nil
	}
	var values []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
