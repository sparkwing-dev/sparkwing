// Package cronspec parses and evaluates standard five-field cron expressions.
//
// The package is pure and clock-injected: every entry point takes the instants
// and the location it works with, so the same code drives a per-minute tick, a
// controller replaying history, and a test.
package cronspec

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression. Obtain one from Parse; the zero value
// matches nothing. A Schedule is immutable and safe for concurrent use.
type Schedule struct {
	minute uint64
	hour   uint64
	dom    uint64
	month  uint64
	dow    uint64

	src string

	// Standard cron unions the two day fields only when both were narrowed.
	domRestricted bool
	dowRestricted bool
}

type fieldSpec struct {
	name  string
	min   int
	max   int
	names map[string]int
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

var fieldSpecs = [5]fieldSpec{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day-of-month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: monthNames},
	{name: "day-of-week", min: 0, max: 7, names: dayNames},
}

var aliases = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// Parse reads a five-field cron expression: minute hour day-of-month month
// day-of-week, separated by any run of spaces or tabs. Each field accepts `*`,
// a value, a list `1,2`, a range `1-5`, and a step on any of those: `*/15`,
// `1-30/5`, `10/5`. The month field accepts jan..dec and the day-of-week field
// sun..sat, case-insensitive; day-of-week takes both 0 and 7 for Sunday.
//
// Parse also accepts the aliases @yearly, @annually, @monthly, @weekly, @daily,
// @midnight and @hourly, each standing alone.
//
// Errors name the offending field and token.
func Parse(expr string) (*Schedule, error) {
	tokens := strings.Fields(expr)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("cronspec: empty expression")
	}
	src := strings.Join(tokens, " ")
	if strings.HasPrefix(tokens[0], "@") {
		expanded, ok := aliases[strings.ToLower(tokens[0])]
		if !ok {
			return nil, fmt.Errorf("cronspec: unknown alias %q", tokens[0])
		}
		if len(tokens) != 1 {
			return nil, fmt.Errorf("cronspec: alias %q stands alone, got %q", tokens[0], src)
		}
		tokens = strings.Fields(expanded)
	}
	if len(tokens) != 5 {
		return nil, fmt.Errorf("cronspec: expected five fields (minute hour day-of-month month day-of-week), got %d in %q", len(tokens), src)
	}

	var masks [5]uint64
	for i, spec := range fieldSpecs {
		mask, err := parseField(tokens[i], spec)
		if err != nil {
			return nil, err
		}
		masks[i] = mask
	}
	dow := masks[4]
	if dow&(1<<7) != 0 {
		dow = dow&^(1<<7) | 1
	}
	return &Schedule{
		minute:        masks[0],
		hour:          masks[1],
		dom:           masks[2],
		month:         masks[3],
		dow:           dow,
		src:           src,
		domRestricted: dayRestricted(tokens[2]),
		dowRestricted: dayRestricted(tokens[4]),
	}, nil
}

// safety: Vixie cron reads the day-field star flag off the first character
// alone, so `*/2` is as unrestricted as `*`: the two day fields OR together
// only when neither begins with one.
func dayRestricted(field string) bool {
	return !strings.HasPrefix(field, "*")
}

// String returns the expression as given, trimmed and single-spaced. An alias
// reads back as the alias.
func (s *Schedule) String() string {
	if s == nil {
		return ""
	}
	return s.src
}

func parseField(field string, spec fieldSpec) (uint64, error) {
	var mask uint64
	for _, token := range strings.Split(field, ",") {
		m, err := parseToken(token, spec)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	return mask, nil
}

func parseToken(token string, spec fieldSpec) (uint64, error) {
	body, step := token, 1
	if i := strings.Index(token, "/"); i >= 0 {
		body = token[:i]
		n, err := strconv.Atoi(token[i+1:])
		if err != nil || n < 1 {
			return 0, fmt.Errorf("cronspec: %s field: invalid step in %q", spec.name, token)
		}
		step = n
	}
	var lo, hi int
	switch {
	case body == "*":
		lo, hi = spec.min, spec.max
	case strings.Contains(body, "-"):
		i := strings.Index(body, "-")
		var err error
		if lo, err = parseValue(body[:i], spec, token); err != nil {
			return 0, err
		}
		if hi, err = parseValue(body[i+1:], spec, token); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("cronspec: %s field: descending range in %q", spec.name, token)
		}
	default:
		v, err := parseValue(body, spec, token)
		if err != nil {
			return 0, err
		}
		lo, hi = v, v
		if step > 1 {
			hi = spec.max
		}
	}
	var mask uint64
	for v := lo; v <= hi; v += step {
		mask |= 1 << uint(v)
	}
	return mask, nil
}

func parseValue(text string, spec fieldSpec, token string) (int, error) {
	if text == "" {
		return 0, fmt.Errorf("cronspec: %s field: invalid token %q", spec.name, token)
	}
	if v, err := strconv.Atoi(text); err == nil {
		if v < spec.min || v > spec.max {
			return 0, fmt.Errorf("cronspec: %s field: value %d out of range %d-%d in %q", spec.name, v, spec.min, spec.max, token)
		}
		return v, nil
	}
	if spec.names != nil {
		if v, ok := spec.names[strings.ToLower(text)]; ok {
			return v, nil
		}
		return 0, fmt.Errorf("cronspec: %s field: unknown name %q in %q", spec.name, text, token)
	}
	return 0, fmt.Errorf("cronspec: %s field: invalid token %q", spec.name, token)
}

func bitSet(mask uint64, v int) bool {
	return v >= 0 && v < 64 && mask&(1<<uint(v)) != 0
}

func zone(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}
