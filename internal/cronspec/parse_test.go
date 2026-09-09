package cronspec_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
)

func TestParseAccepts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		expr string
		want string
	}{
		{name: "every minute", expr: "* * * * *", want: "* * * * *"},
		{name: "list", expr: "5,35 * * * *", want: "5,35 * * * *"},
		{name: "range", expr: "0 9-17 * * *", want: "0 9-17 * * *"},
		{name: "star step", expr: "*/15 * * * *", want: "*/15 * * * *"},
		{name: "range step", expr: "0 0 1-30/5 * *", want: "0 0 1-30/5 * *"},
		{name: "value step", expr: "10/5 * * * *", want: "10/5 * * * *"},
		{name: "month names", expr: "0 0 * jan-mar *", want: "0 0 * jan-mar *"},
		{name: "day names", expr: "0 0 * * MON,wed", want: "0 0 * * MON,wed"},
		{name: "sunday as seven", expr: "0 0 * * 7", want: "0 0 * * 7"},
		{name: "mixed separators", expr: "\t0   0 *\t* *  ", want: "0 0 * * *"},
		{name: "alias", expr: "@daily", want: "@daily"},
		{name: "alias case", expr: "@MIDNIGHT", want: "@MIDNIGHT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := cronspec.Parse(tc.expr)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", tc.expr, err)
			}
			if got := s.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseAliasesExpand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		alias string
		equiv string
	}{
		{alias: "@yearly", equiv: "0 0 1 1 *"},
		{alias: "@annually", equiv: "0 0 1 1 *"},
		{alias: "@monthly", equiv: "0 0 1 * *"},
		{alias: "@weekly", equiv: "0 0 * * 0"},
		{alias: "@daily", equiv: "0 0 * * *"},
		{alias: "@midnight", equiv: "0 0 * * *"},
		{alias: "@hourly", equiv: "0 * * * *"},
	}
	after := time.Date(2025, time.June, 11, 13, 27, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			t.Parallel()
			alias := mustParse(t, tc.alias)
			equiv := mustParse(t, tc.equiv)
			got := alias.Upcoming(after, time.UTC, 4)
			want := equiv.Upcoming(after, time.UTC, 4)
			if len(got) != len(want) {
				t.Fatalf("Upcoming lengths %d and %d", len(got), len(want))
			}
			for i := range got {
				if !got[i].Equal(want[i]) {
					t.Fatalf("instant %d: %s, want %s", i, got[i], want[i])
				}
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		expr string
		want string
	}{
		{name: "empty", expr: "", want: "empty expression"},
		{name: "blank", expr: "   \t ", want: "empty expression"},
		{name: "four fields", expr: "* * * *", want: "expected five fields"},
		{name: "six fields with seconds", expr: "0 * * * * *", want: "expected five fields"},
		{name: "seven fields", expr: "0 * * * * * *", want: "expected five fields"},
		{name: "minute out of range", expr: "60 * * * *", want: `minute field: value 60 out of range 0-59`},
		{name: "hour out of range", expr: "* 24 * * *", want: `hour field: value 24 out of range 0-23`},
		{name: "day of month too high", expr: "0 0 32 * *", want: `day-of-month field: value 32 out of range 1-31`},
		{name: "day of month zero", expr: "0 0 0 * *", want: `day-of-month field: value 0 out of range 1-31`},
		{name: "month out of range", expr: "* * * 13 *", want: `month field: value 13 out of range 1-12`},
		{name: "day of week out of range", expr: "* * * * 8", want: `day-of-week field: value 8 out of range 0-7`},
		{name: "zero step", expr: "*/0 * * * *", want: `minute field: invalid step in "*/0"`},
		{name: "non numeric step", expr: "*/x * * * *", want: `minute field: invalid step in "*/x"`},
		{name: "negative step", expr: "*/-2 * * * *", want: `minute field: invalid step in "*/-2"`},
		{name: "descending range", expr: "5-1 * * * *", want: `minute field: descending range in "5-1"`},
		{name: "empty list item", expr: "1,,2 * * * *", want: `minute field: invalid token ""`},
		{name: "unnamed field takes no names", expr: "abc * * * *", want: `minute field: invalid token "abc"`},
		{name: "unknown month name", expr: "* * * foo *", want: `month field: unknown name "foo"`},
		{name: "unknown day name", expr: "* * * * funday", want: `day-of-week field: unknown name "funday"`},
		{name: "unknown alias", expr: "@bogus", want: `unknown alias "@bogus"`},
		{name: "alias with fields", expr: "@daily *", want: `alias "@daily" stands alone`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := cronspec.Parse(tc.expr)
			if err == nil {
				t.Fatalf("Parse(%q) = %v, want an error", tc.expr, s)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%q) error = %q, want it to contain %q", tc.expr, err, tc.want)
			}
		})
	}
}

func TestNilScheduleIsInert(t *testing.T) {
	t.Parallel()
	var s *cronspec.Schedule
	at := time.Date(2025, time.June, 11, 13, 27, 0, 0, time.UTC)
	if got := s.String(); got != "" {
		t.Fatalf("String() = %q, want empty", got)
	}
	if got := s.Next(at, time.UTC); !got.IsZero() {
		t.Fatalf("Next() = %s, want the zero time", got)
	}
	if got := s.Upcoming(at, time.UTC, 3); got != nil {
		t.Fatalf("Upcoming() = %v, want nil", got)
	}
}

func mustParse(t *testing.T, expr string) *cronspec.Schedule {
	t.Helper()
	s, err := cronspec.Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", expr, err)
	}
	return s
}
