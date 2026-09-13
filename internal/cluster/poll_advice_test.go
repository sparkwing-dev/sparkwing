package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type fixedAdvisor time.Duration

func (a fixedAdvisor) PollAdvice() time.Duration { return time.Duration(a) }

func TestAdvisedPoll_OnlyWidensTheConfiguredCadence(t *testing.T) {
	cases := []struct {
		name       string
		configured time.Duration
		advisor    pollAdvisor
		least      time.Duration
		most       time.Duration
	}{
		{"no advisor", time.Second, nil, time.Second, time.Second},
		{"no advice", time.Second, fixedAdvisor(0), time.Second, time.Second},
		{"advice below the configured cadence", 5 * time.Second, fixedAdvisor(time.Second), 5 * time.Second, 5 * time.Second},
		{"advice above the configured cadence", time.Second, fixedAdvisor(10 * time.Second), 10 * time.Second, 12500 * time.Millisecond},
		{"advice above the cap", time.Second, fixedAdvisor(time.Hour), maxAdvisedPoll, maxAdvisedPoll + maxAdvisedPoll/4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait := advisedPoll(tc.configured, tc.advisor)
			if wait < tc.least || wait > tc.most {
				t.Fatalf("wait = %s, want between %s and %s", wait, tc.least, tc.most)
			}
		})
	}
}

type advisingClaimer struct {
	advice time.Duration

	mu    sync.Mutex
	calls []time.Time
}

func (a *advisingClaimer) ClaimNodeWithCapacity(ctx context.Context, holderID string, labels []string,
	lease time.Duration, headroom *client.Headroom, capacity *client.ClaimCapacity,
) (*store.Node, error) {
	a.mu.Lock()
	a.calls = append(a.calls, time.Now())
	a.mu.Unlock()
	return nil, nil
}

func (a *advisingClaimer) PollAdvice() time.Duration { return a.advice }

func (a *advisingClaimer) gaps() []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	gaps := make([]time.Duration, 0, len(a.calls))
	for i := 1; i < len(a.calls); i++ {
		gaps = append(gaps, a.calls[i].Sub(a.calls[i-1]))
	}
	return gaps
}

func TestRunPoolLoop_HonoursTheSuggestedPollInterval(t *testing.T) {
	const advice = 80 * time.Millisecond
	claimer := &advisingClaimer{advice: advice}

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub",
		HolderPrefix:  "test",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		SourceName:    "test runner",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	exec := func(ctx context.Context, n *store.Node, holderID string) {}
	if err := runPoolLoop(ctx, cfg, claimer, exec, nil, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	gaps := claimer.gaps()
	if len(gaps) == 0 {
		t.Fatal("the loop polled once or not at all; it cannot show a cadence")
	}
	for i, gap := range gaps {
		if gap < advice {
			t.Errorf("poll %d came %s after the last one; the controller suggested %s", i+1, gap, advice)
		}
	}
	// safety: a one-millisecond cadence left unwidened would have polled hundreds of times in the same window.
	if len(gaps) > 6 {
		t.Errorf("loop polled %d times in 400ms; the suggestion did not widen its cadence", len(gaps)+1)
	}
}
