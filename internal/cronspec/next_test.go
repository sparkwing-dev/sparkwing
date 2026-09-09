package cronspec_test

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	t.Parallel()
	utc := func(y int, mo time.Month, d, h, mi int) time.Time {
		return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	}
	cases := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "minute step",
			expr:  "*/15 * * * *",
			after: time.Date(2025, time.January, 1, 0, 7, 30, 0, time.UTC),
			want:  utc(2025, time.January, 1, 0, 15),
		},
		{
			name:  "strictly after a match",
			expr:  "*/15 * * * *",
			after: utc(2025, time.January, 1, 0, 15),
			want:  utc(2025, time.January, 1, 0, 30),
		},
		{
			name:  "list",
			expr:  "5,35 * * * *",
			after: utc(2025, time.January, 1, 0, 10),
			want:  utc(2025, time.January, 1, 0, 35),
		},
		{
			name:  "hour rolls the day",
			expr:  "0 9 * * *",
			after: utc(2025, time.January, 1, 9, 0),
			want:  utc(2025, time.January, 2, 9, 0),
		},
		{
			name:  "working hours skip the weekend",
			expr:  "0 9-17 * * 1-5",
			after: utc(2025, time.January, 3, 17, 30),
			want:  utc(2025, time.January, 6, 9, 0),
		},
		{
			name:  "day of month",
			expr:  "0 0 15 * *",
			after: utc(2025, time.January, 20, 0, 0),
			want:  utc(2025, time.February, 15, 0, 0),
		},
		{
			name:  "day of month step",
			expr:  "0 0 1-30/5 * *",
			after: utc(2025, time.January, 2, 0, 0),
			want:  utc(2025, time.January, 6, 0, 0),
		},
		{
			name:  "month wraps the year",
			expr:  "0 0 1 3 *",
			after: utc(2025, time.April, 1, 0, 0),
			want:  utc(2026, time.March, 1, 0, 0),
		},
		{
			name:  "month name",
			expr:  "0 0 * jul *",
			after: utc(2025, time.August, 1, 0, 0),
			want:  utc(2026, time.July, 1, 0, 0),
		},
		{
			name:  "day of week",
			expr:  "0 0 * * 1",
			after: utc(2025, time.January, 1, 0, 0),
			want:  utc(2025, time.January, 6, 0, 0),
		},
		{
			name:  "seven is sunday",
			expr:  "0 0 * * 7",
			after: utc(2025, time.January, 1, 0, 0),
			want:  utc(2025, time.January, 5, 0, 0),
		},
		{
			name:  "zero is sunday",
			expr:  "0 0 * * 0",
			after: utc(2025, time.January, 1, 0, 0),
			want:  utc(2025, time.January, 5, 0, 0),
		},
		{
			name:  "day of month alone",
			expr:  "0 0 13 * *",
			after: utc(2025, time.May, 1, 0, 0),
			want:  utc(2025, time.May, 13, 0, 0),
		},
		{
			name:  "day of week alone",
			expr:  "0 0 * * 5",
			after: utc(2025, time.May, 1, 0, 0),
			want:  utc(2025, time.May, 2, 0, 0),
		},
		{
			name:  "both day fields union on the weekday",
			expr:  "0 0 13 * 5",
			after: utc(2025, time.May, 1, 0, 0),
			want:  utc(2025, time.May, 2, 0, 0),
		},
		{
			name:  "both day fields union on the date",
			expr:  "0 0 13 * 5",
			after: utc(2025, time.May, 10, 0, 0),
			want:  utc(2025, time.May, 13, 0, 0),
		},
		{
			name:  "a day-of-month step is not a restriction",
			expr:  "0 3 */2 * 1",
			after: utc(2025, time.May, 1, 0, 0),
			want:  utc(2025, time.May, 5, 3, 0),
		},
		{
			name:  "a day-of-week step is not a restriction",
			expr:  "0 3 13 * */2",
			after: utc(2025, time.May, 1, 0, 0),
			want:  utc(2025, time.May, 13, 3, 0),
		},
		{
			name:  "leap day in a leap year",
			expr:  "0 0 29 2 *",
			after: utc(2023, time.March, 1, 0, 0),
			want:  utc(2024, time.February, 29, 0, 0),
		},
		{
			name:  "leap day skips three years",
			expr:  "0 0 29 2 *",
			after: utc(2024, time.March, 1, 0, 0),
			want:  utc(2028, time.February, 29, 0, 0),
		},
		{
			name:  "last day skips short months",
			expr:  "0 0 31 * *",
			after: utc(2025, time.January, 31, 0, 0),
			want:  utc(2025, time.March, 31, 0, 0),
		},
		{
			name:  "february thirtieth never matches",
			expr:  "0 0 30 2 *",
			after: utc(2025, time.January, 1, 0, 0),
			want:  time.Time{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mustParse(t, tc.expr).Next(tc.after, time.UTC)
			if !got.Equal(tc.want) {
				t.Fatalf("Next(%s) = %s, want %s", tc.after, got, tc.want)
			}
		})
	}
}

func TestNextDefaultsToUTC(t *testing.T) {
	t.Parallel()
	after := time.Date(2025, time.January, 1, 10, 30, 0, 0, time.UTC)
	got := mustParse(t, "0 * * * *").Next(after, nil)
	want := time.Date(2025, time.January, 1, 11, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next() = %s, want %s", got, want)
	}
}

func TestUpcoming(t *testing.T) {
	t.Parallel()
	after := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	got := mustParse(t, "0 0 * * *").Upcoming(after, time.UTC, 3)
	want := []time.Time{
		time.Date(2025, time.January, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2025, time.January, 4, 0, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("Upcoming() returned %d instants, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("instant %d = %s, want %s", i, got[i], want[i])
		}
	}
	if got := mustParse(t, "0 0 * * *").Upcoming(after, time.UTC, 0); got != nil {
		t.Fatalf("Upcoming(n=0) = %v, want nil", got)
	}
	if got := mustParse(t, "0 0 30 2 *").Upcoming(after, time.UTC, 3); len(got) != 0 {
		t.Fatalf("Upcoming() on an impossible schedule = %v, want none", got)
	}
}
