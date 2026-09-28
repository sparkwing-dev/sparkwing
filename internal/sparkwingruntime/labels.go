package sparkwingruntime

import "strings"

// NormalizeLabels trims labels, removes empty values and keeps the first copy
// of each capability in declaration order.
func NormalizeLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
