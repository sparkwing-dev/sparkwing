package cluster

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func shedError(after time.Duration) error {
	return &client.UnavailableError{
		RetryAfter: after,
		Err:        errors.New("controller 503: unavailable"),
	}
}

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
		{"header above the cap", time.Hour, 0, maxClaimBackoff, maxClaimBackoff + maxClaimBackoff/4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, ok := unavailableBackoff(shedError(tc.after), tc.floor)
			if !ok {
				t.Fatalf("unavailableBackoff did not recognize a shed claim")
			}
			if wait < tc.least || wait > tc.most {
				t.Fatalf("wait = %s, want between %s and %s", wait, tc.least, tc.most)
			}
		})
	}
}

func TestUnavailableBackoff_IgnoresOtherErrors(t *testing.T) {
	for _, err := range []error{errors.New("connection refused"), store.ErrInsufficientCredits, nil} {
		if _, ok := unavailableBackoff(err, time.Second); ok {
			t.Fatalf("unavailableBackoff claimed %v as a shed poll", err)
		}
	}
}

func TestShedLog_WarnsAtMostOncePerWindow(t *testing.T) {
	clock := time.Now()
	s := newShedLog(time.Minute)
	s.now = func() time.Time { return clock }

	if !s.due() {
		t.Fatal("the first shed poll stayed silent")
	}
	clock = clock.Add(59 * time.Second)
	if s.due() {
		t.Fatal("a second line inside the window")
	}
	clock = clock.Add(2 * time.Second)
	if !s.due() {
		t.Fatal("no line after the window closed")
	}
}

func TestRunPoolLoop_ShedClaimBacksOffWithoutAnErrorLine(t *testing.T) {
	stub := &stubClaimer{responses: []claimResp{
		{err: shedError(0)},
		{err: shedError(0)},
		{err: shedError(0)},
		{node: fakeNode("a")},
	}}

	var executed atomic.Int64
	exec := func(context.Context, *store.Node, string) { executed.Add(1) }

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	const outcome = `sparkwing_runner_claims_total{outcome="unavailable"}`
	before := metricSampleValue(t, gatherMetrics(t), outcome)

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub",
		HolderPrefix:  "test",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		MaxClaims:     1,
		SourceName:    "test runner",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runPoolLoop(ctx, cfg, stub, exec, nil, logger); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	out := logs.String()
	if got := executed.Load(); got != 1 {
		t.Fatalf("exec calls = %d, want 1: a shed claim must not stop the loop", got)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Fatalf("a shed claim logged an error line:\n%s", out)
	}
	if got := strings.Count(out, "level=WARN"); got != 1 {
		t.Fatalf("shed claims logged %d warn lines, want 1 per window:\n%s", got, out)
	}
	if got := strings.Count(out, "claim shed by the controller"); got != 3 {
		t.Fatalf("shed claims logged %d debug lines, want 3:\n%s", got, out)
	}
	if got := metricSampleValue(t, gatherMetrics(t), outcome); got != before+3 {
		t.Fatalf("shed claims counted %v, want %v", got, before+3)
	}
}
