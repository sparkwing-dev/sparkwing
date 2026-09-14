package client

import (
	"errors"
	"testing"
	"time"
)

func shedError(after time.Duration) error {
	return &UnavailableError{
		RetryAfter: after,
		Err:        errors.New("controller 503: unavailable"),
	}
}

type fixedAdvisor time.Duration

func (a fixedAdvisor) PollAdvice() time.Duration { return time.Duration(a) }

func TestUnavailableBackoff_HonorsTheHeaderBetweenFloorAndCap(t *testing.T) {
	cases := []struct {
		name  string
		after time.Duration
		floor time.Duration
		least time.Duration
		most  time.Duration
	}{
		{"header above the floor", 2 * time.Second, 500 * time.Millisecond, 2 * time.Second, 2500 * time.Millisecond},
		{"header below the floor", 0, 500 * time.Millisecond, 500 * time.Millisecond, 625 * time.Millisecond},
		{"header above the cap", time.Hour, 0, MaxClaimBackoff, MaxClaimBackoff + MaxClaimBackoff/4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, ok := UnavailableBackoff(shedError(tc.after), tc.floor)
			if !ok {
				t.Fatal("UnavailableBackoff did not recognize a shed claim")
			}
			if wait < tc.least || wait > tc.most {
				t.Fatalf("wait = %s, want between %s and %s", wait, tc.least, tc.most)
			}
		})
	}
}

func TestUnavailableBackoff_IgnoresOtherErrors(t *testing.T) {
	for _, err := range []error{errors.New("connection refused"), nil} {
		if _, ok := UnavailableBackoff(err, time.Second); ok {
			t.Fatalf("UnavailableBackoff claimed %v as a shed poll", err)
		}
	}
}

func TestAdvisedPoll_OnlyWidensTheConfiguredCadence(t *testing.T) {
	cases := []struct {
		name       string
		configured time.Duration
		advisor    PollAdvisor
		least      time.Duration
		most       time.Duration
	}{
		{"no advisor", time.Second, nil, time.Second, time.Second},
		{"no advice", time.Second, fixedAdvisor(0), time.Second, time.Second},
		{"advice below the configured cadence", 5 * time.Second, fixedAdvisor(time.Second), 5 * time.Second, 5 * time.Second},
		{"advice above the configured cadence", time.Second, fixedAdvisor(6 * time.Second), 6 * time.Second, 7500 * time.Millisecond},
		{"advice above the cap", time.Second, fixedAdvisor(time.Hour), MaxPollAdvice, MaxPollAdvice + MaxPollAdvice/4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait := AdvisedPoll(tc.configured, tc.advisor)
			if wait < tc.least || wait > tc.most {
				t.Fatalf("wait = %s, want between %s and %s", wait, tc.least, tc.most)
			}
		})
	}
}

func TestShedLog_WarnsAtMostOncePerWindow(t *testing.T) {
	clock := time.Now()
	s := NewShedLog(time.Minute)
	s.now = func() time.Time { return clock }

	if !s.Due() {
		t.Fatal("the first shed poll stayed silent")
	}
	clock = clock.Add(59 * time.Second)
	if s.Due() {
		t.Fatal("a second line inside the window")
	}
	clock = clock.Add(2 * time.Second)
	if !s.Due() {
		t.Fatal("no line after the window closed")
	}
}
