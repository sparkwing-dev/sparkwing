package cronspec_test

import (
	"testing"
	"time"

	// safety: the DST rules this suite pins are the reason it exists, so it must
	// not skip itself on a host with no zone database.
	_ "time/tzdata"
)

func loadZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone database unavailable: %v", err)
	}
	return loc
}

func TestNextSkipsNonexistentWallClockMinute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		zone  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "denver spring forward",
			zone:  "America/Denver",
			expr:  "30 2 * * *",
			after: time.Date(2025, time.March, 8, 12, 0, 0, 0, time.UTC),
			want:  time.Date(2025, time.March, 10, 8, 30, 0, 0, time.UTC),
		},
		{
			name:  "london spring forward",
			zone:  "Europe/London",
			expr:  "30 1 * * *",
			after: time.Date(2025, time.March, 29, 12, 0, 0, 0, time.UTC),
			want:  time.Date(2025, time.March, 31, 0, 30, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc := loadZone(t, tc.zone)
			got := mustParse(t, tc.expr).Next(tc.after, loc)
			if !got.Equal(tc.want) {
				t.Fatalf("Next() = %s, want %s", got.In(loc), tc.want.In(loc))
			}
		})
	}
}

func TestNextFiresOnceOnRepeatedWallClockMinute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		zone   string
		expr   string
		after  time.Time
		first  time.Time
		second time.Time
	}{
		{
			name:   "denver fall back",
			zone:   "America/Denver",
			expr:   "30 1 * * *",
			after:  time.Date(2025, time.November, 1, 12, 0, 0, 0, time.UTC),
			first:  time.Date(2025, time.November, 2, 7, 30, 0, 0, time.UTC),
			second: time.Date(2025, time.November, 3, 8, 30, 0, 0, time.UTC),
		},
		{
			name:   "london fall back",
			zone:   "Europe/London",
			expr:   "30 1 * * *",
			after:  time.Date(2025, time.October, 25, 12, 0, 0, 0, time.UTC),
			first:  time.Date(2025, time.October, 26, 0, 30, 0, 0, time.UTC),
			second: time.Date(2025, time.October, 27, 1, 30, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc := loadZone(t, tc.zone)
			s := mustParse(t, tc.expr)
			got := s.Next(tc.after, loc)
			if !got.Equal(tc.first) {
				t.Fatalf("first fire = %s, want %s", got.In(loc), tc.first.In(loc))
			}
			next := s.Next(got, loc)
			if !next.Equal(tc.second) {
				t.Fatalf("second fire = %s, want %s (the repeated hour must not fire twice)", next.In(loc), tc.second.In(loc))
			}
		})
	}
}

func TestNextCountsAcrossTransitions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		zone string
		day  time.Time
		want int
	}{
		{name: "denver spring forward", zone: "America/Denver", day: time.Date(2025, time.March, 9, 0, 0, 0, 0, time.UTC), want: 92},
		{name: "denver fall back", zone: "America/Denver", day: time.Date(2025, time.November, 2, 0, 0, 0, 0, time.UTC), want: 96},
		{name: "london spring forward", zone: "Europe/London", day: time.Date(2025, time.March, 30, 0, 0, 0, 0, time.UTC), want: 92},
		{name: "london fall back", zone: "Europe/London", day: time.Date(2025, time.October, 26, 0, 0, 0, 0, time.UTC), want: 96},
		{name: "denver ordinary day", zone: "America/Denver", day: time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC), want: 96},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc := loadZone(t, tc.zone)
			y, mo, d := tc.day.Date()
			start := time.Date(y, mo, d, 0, 0, 0, 0, loc)
			end := start.AddDate(0, 0, 1)
			s := mustParse(t, "*/15 * * * *")
			count := 0
			for at := start.Add(-time.Second); ; count++ {
				next := s.Next(at, loc)
				if next.IsZero() || !next.Before(end) {
					break
				}
				at = next
			}
			if count != tc.want {
				t.Fatalf("fires between %s and %s = %d, want %d", start, end, count, tc.want)
			}
		})
	}
}
