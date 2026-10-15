package clisurface

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ParseLookback accepts Go durations plus day and week suffixes for history filters.
func ParseLookback(v string) (time.Duration, error) {
	v = strings.ToLower(v)
	if v == "" {
		return 0, errors.New("empty duration")
	}
	last := v[len(v)-1]
	switch last {
	case 'd':
		days, err := time.ParseDuration(v[:len(v)-1] + "h")
		if err != nil {
			return 0, err
		}
		return scaleDuration(days, 24, v)
	case 'w':
		weeks, err := time.ParseDuration(v[:len(v)-1] + "h")
		if err != nil {
			return 0, err
		}
		return scaleDuration(weeks, 24*7, v)
	default:
		return time.ParseDuration(v)
	}
}

func scaleDuration(value time.Duration, factor int64, raw string) (time.Duration, error) {
	const (
		maxDuration = time.Duration(1<<63 - 1)
		minDuration = time.Duration(-1 << 63)
	)
	if value > maxDuration/time.Duration(factor) || value < minDuration/time.Duration(factor) {
		return 0, fmt.Errorf("duration %q overflows", raw)
	}
	return value * time.Duration(factor), nil
}
