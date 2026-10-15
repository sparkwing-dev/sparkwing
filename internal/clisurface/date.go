package clisurface

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ParseLooseDate accepts calendar dates and relative lookbacks in local time.
func ParseLooseDate(raw string) (time.Time, error) {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return time.Time{}, errors.New("empty date")
	}
	now := time.Now()
	switch v {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		y := now.AddDate(0, 0, -1)
		return time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, y.Location()), nil
	}
	if d, err := ParseLookback(v); err == nil {
		return now.Add(-d), nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date %q (try today, yesterday, 24h, 7d, or 2026-05-10)", raw)
}
