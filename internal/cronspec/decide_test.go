package cronspec_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	at := func(h, mi, sec int) time.Time {
		return time.Date(2025, time.January, 2, h, mi, sec, 0, time.UTC)
	}
	cases := []struct {
		name    string
		expr    string
		cursor  time.Time
		now     time.Time
		catchUp time.Duration
		want    cronspec.Decision
	}{
		{
			name:    "nothing due since the cursor",
			expr:    "0 * * * *",
			cursor:  at(10, 0, 0),
			now:     at(10, 0, 5),
			catchUp: time.Hour,
			want:    cronspec.Decision{},
		},
		{
			name:    "nothing due yet today",
			expr:    "0 0 * * *",
			cursor:  time.Date(2025, time.January, 2, 0, 0, 0, 0, time.UTC),
			now:     at(12, 0, 0),
			catchUp: time.Hour,
			want:    cronspec.Decision{},
		},
		{
			name:    "one due inside the window",
			expr:    "*/5 * * * *",
			cursor:  at(10, 0, 0),
			now:     at(10, 5, 10),
			catchUp: time.Hour,
			want:    cronspec.Decision{Due: at(10, 5, 0), Fire: true},
		},
		{
			name:    "one due outside the window",
			expr:    "0 * * * *",
			cursor:  at(9, 0, 0),
			now:     at(10, 5, 0),
			catchUp: 2 * time.Minute,
			want:    cronspec.Decision{Due: at(10, 0, 0), Missed: 1},
		},
		{
			name:    "many due with the latest inside the window",
			expr:    "*/5 * * * *",
			cursor:  at(9, 0, 0),
			now:     at(10, 2, 0),
			catchUp: time.Hour,
			want:    cronspec.Decision{Due: at(10, 0, 0), Fire: true, Missed: 11},
		},
		{
			name:    "many due with the latest outside the window",
			expr:    "*/5 * * * *",
			cursor:  at(9, 0, 0),
			now:     at(10, 4, 0),
			catchUp: 2 * time.Minute,
			want:    cronspec.Decision{Due: at(10, 0, 0), Missed: 12},
		},
		{
			name:    "catch-up floors at two minutes",
			expr:    "* * * * *",
			cursor:  at(10, 0, 0),
			now:     at(10, 1, 30),
			catchUp: 0,
			want:    cronspec.Decision{Due: at(10, 1, 0), Fire: true},
		},
		{
			name:    "a late tick still fires",
			expr:    "0 * * * *",
			cursor:  at(9, 0, 0),
			now:     at(10, 1, 45),
			catchUp: time.Second,
			want:    cronspec.Decision{Due: at(10, 0, 0), Fire: true},
		},
		{
			name:    "a cursor ahead of now decides nothing",
			expr:    "* * * * *",
			cursor:  at(11, 0, 0),
			now:     at(10, 0, 30),
			catchUp: time.Hour,
			want:    cronspec.Decision{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cronspec.Decide(mustParse(t, tc.expr), time.UTC, tc.cursor, tc.now, tc.catchUp)
			if !got.Due.Equal(tc.want.Due) || got.Fire != tc.want.Fire || got.Missed != tc.want.Missed {
				t.Fatalf("Decide() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecideCursorAdvancesPastTheFire(t *testing.T) {
	t.Parallel()
	s := mustParse(t, "*/5 * * * *")
	cursor := time.Date(2025, time.January, 2, 9, 0, 0, 0, time.UTC)
	now := time.Date(2025, time.January, 2, 10, 2, 0, 0, time.UTC)
	first := cronspec.Decide(s, time.UTC, cursor, now, time.Hour)
	if !first.Fire {
		t.Fatalf("first Decide() = %+v, want a fire", first)
	}
	again := cronspec.Decide(s, time.UTC, first.Due, now, time.Hour)
	if again != (cronspec.Decision{}) {
		t.Fatalf("second Decide() = %+v, want nothing due", again)
	}
}

func TestDecideCapsTheBacklog(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, time.February, 1, 0, 0, 30, 0, time.UTC)
	got := cronspec.Decide(mustParse(t, "* * * * *"), time.UTC, now.AddDate(0, 0, -30), now, time.Hour)
	want := cronspec.Decision{Due: now.Truncate(time.Minute), Fire: true, Missed: cronspec.MaxMissed}
	if !got.Due.Equal(want.Due) || got.Fire != want.Fire || got.Missed != want.Missed {
		t.Fatalf("Decide() = %+v, want %+v", got, want)
	}
}

func TestDecideHonorsTheLocation(t *testing.T) {
	t.Parallel()
	loc := loadZone(t, "America/Denver")
	s := mustParse(t, "0 3 * * *")
	now := time.Date(2025, time.June, 10, 9, 0, 30, 0, time.UTC)
	got := cronspec.Decide(s, loc, now.Add(-24*time.Hour), now, time.Hour)
	want := time.Date(2025, time.June, 10, 3, 0, 0, 0, loc)
	if !got.Due.Equal(want) || !got.Fire {
		t.Fatalf("Decide() = %+v, want a fire at %s", got, want)
	}
}

func TestDecideDoesNotRefireTheRepeatedHour(t *testing.T) {
	t.Parallel()
	loc := loadZone(t, "America/Denver")
	s := mustParse(t, "30 1 * * *")
	fired := time.Date(2025, time.November, 2, 7, 30, 0, 0, time.UTC)
	secondOccurrence := time.Date(2025, time.November, 2, 8, 30, 30, 0, time.UTC)
	if got := cronspec.Decide(s, loc, fired, secondOccurrence, time.Hour); got != (cronspec.Decision{}) {
		t.Fatalf("Decide() = %+v, want nothing due on the repeated wall-clock minute", got)
	}
}

func TestDecideOnNilSchedule(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, time.January, 2, 10, 0, 0, 0, time.UTC)
	if got := cronspec.Decide(nil, time.UTC, now.Add(-time.Hour), now, time.Hour); got != (cronspec.Decision{}) {
		t.Fatalf("Decide(nil) = %+v, want the zero Decision", got)
	}
}
