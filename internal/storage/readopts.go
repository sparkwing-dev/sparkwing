package storage

import (
	"bytes"
	"fmt"
	"strings"
)

// Apply returns the lines of a newline-separated log that o selects:
// the Lines range first, then Grep, then Tail, then Head. An empty
// ReadOpts returns data unchanged. Every [LogStore] that filters after
// reading the whole log applies its options through here, so the
// filters mean the same thing on every backend.
func (o ReadOpts) Apply(data []byte) ([]byte, error) {
	if (o == ReadOpts{}) {
		return data, nil
	}
	lines := splitLines(data)

	if o.Lines != "" {
		from, to, err := parseLineRange(o.Lines)
		if err != nil {
			return nil, err
		}
		from = max(from, 1)
		to = min(to, len(lines))
		if from > len(lines) || from > to {
			lines = nil
		} else {
			lines = lines[from-1 : to]
		}
	}

	if o.Grep != "" {
		filtered := lines[:0]
		for _, l := range lines {
			if strings.Contains(l, o.Grep) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	if o.Tail > 0 && len(lines) > o.Tail {
		lines = lines[len(lines)-o.Tail:]
	}
	if o.Head > 0 && len(lines) > o.Head {
		lines = lines[:o.Head]
	}

	var out bytes.Buffer
	for _, l := range lines {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	if s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

func parseLineRange(spec string) (from, to int, err error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid lines range %q", spec)
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &from); err != nil {
		return 0, 0, fmt.Errorf("invalid lines.from %q: %w", parts[0], err)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &to); err != nil {
		return 0, 0, fmt.Errorf("invalid lines.to %q: %w", parts[1], err)
	}
	return from, to, nil
}
