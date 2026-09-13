package cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
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
		{"advice above the configured cadence", time.Second, fixedAdvisor(6 * time.Second), 6 * time.Second, 7500 * time.Millisecond},
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

type shedHeartbeatClient struct {
	mu    sync.Mutex
	calls int
	shed  int
}

func (c *shedHeartbeatClient) HeartbeatExecutor(context.Context, string, client.Headroom) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls <= c.shed {
		return &client.RateLimitedError{
			RetryAfter: 10 * time.Millisecond,
			Err:        errors.New("controller 429: too many heartbeat requests from this runner"),
		}
	}
	return nil
}

func (c *shedHeartbeatClient) attempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (*shedHeartbeatClient) PrepareExecutorClaim(context.Context, string) (*store.ExecutorClaimPreparation, error) {
	return nil, nil
}

func (*shedHeartbeatClient) OfferExecutorClaim(context.Context, client.ExecutorClaim, string, string) (client.ExecutorClaimOfferResult, error) {
	return client.ExecutorClaimOfferResult{}, nil
}

func staticHeadroom() headroomProvider {
	return func(context.Context) capacityReport {
		return capacityReport{headroom: &client.Headroom{Cores: 1}}
	}
}

func TestRunExecutorLiveness_SurvivesAShedHeartbeat(t *testing.T) {
	restoreBackoff := minShedBackoff
	minShedBackoff = 10 * time.Millisecond
	t.Cleanup(func() { minShedBackoff = restoreBackoff })

	ctrl := &shedHeartbeatClient{shed: 3}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := runExecutorLiveness(ctx, "agent-1", 10*time.Millisecond,
		staticHeadroom(), ctrl, discardLogger())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("liveness ended with %v; a shed heartbeat must not end it before its context does", err)
	}
	if got := ctrl.attempts(); got <= ctrl.shed {
		t.Errorf("liveness stopped after %d attempts; it must keep beating past a shed one", got)
	}
}

func TestRunExecutorLiveness_FailsOnceSilencePassesTheLeaseWindow(t *testing.T) {
	ctrl := &shedHeartbeatClient{shed: 1 << 30}
	restoreBackoff := minShedBackoff
	minShedBackoff = 5 * time.Millisecond
	t.Cleanup(func() { minShedBackoff = restoreBackoff })
	restore := maxExecutorHeartbeatSilence
	maxExecutorHeartbeatSilence = 50 * time.Millisecond
	t.Cleanup(func() { maxExecutorHeartbeatSilence = restore })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runExecutorLiveness(ctx, "agent-1", 10*time.Millisecond,
		staticHeadroom(), ctrl, discardLogger())
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("liveness ended with %v; past its silence window it must report the heartbeat failure", err)
	}
}

type rateLimitedClaimer struct {
	retryAfter time.Duration

	mu    sync.Mutex
	calls []time.Time
	shed  int
}

func (c *rateLimitedClaimer) ClaimNodeWithCapacity(ctx context.Context, holderID string, labels []string,
	lease time.Duration, headroom *client.Headroom, capacity *client.ClaimCapacity,
) (*store.Node, error) {
	c.mu.Lock()
	c.calls = append(c.calls, time.Now())
	shed := c.shed
	c.shed--
	c.mu.Unlock()
	if shed > 0 {
		return nil, &client.RateLimitedError{
			RetryAfter: c.retryAfter,
			Err:        errors.New("controller 429: too many claim requests from this runner"),
		}
	}
	return nil, nil
}

func (c *rateLimitedClaimer) PollAdvice() time.Duration { return 0 }

func (c *rateLimitedClaimer) gaps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	gaps := make([]time.Duration, 0, len(c.calls))
	for i := 1; i < len(c.calls); i++ {
		gaps = append(gaps, c.calls[i].Sub(c.calls[i-1]))
	}
	return gaps
}

func TestRunPoolLoop_BacksOffOnARateLimitedClaim(t *testing.T) {
	const retryAfter = 60 * time.Millisecond
	claimer := &rateLimitedClaimer{retryAfter: retryAfter, shed: 3}

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub",
		HolderPrefix:  "test",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		SourceName:    "test runner",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	exec := func(ctx context.Context, n *store.Node, holderID string) {}
	if err := runPoolLoop(ctx, cfg, claimer, exec, nil, discardLogger()); err != nil {
		t.Fatalf("a shed claim stopped the loop: %v", err)
	}

	gaps := claimer.gaps()
	if len(gaps) < 2 {
		t.Fatalf("loop polled %d times; it cannot show a backoff", len(gaps)+1)
	}
	for i, gap := range gaps[:2] {
		if gap < retryAfter {
			t.Errorf("poll %d came %s after a 429 naming %s; the loop ignored the backpressure",
				i+1, gap, retryAfter)
		}
	}
}

func TestRunPoolLoop_AStableIdentityLetsTheBudgetBite(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var shed atomic.Int64
	ctrl := controller.New(st, nil).
		WithRequestBudget(controller.RequestBudget{ClaimsPerMinute: 2})
	handler := ctrl.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		handler.ServeHTTP(recorder, r)
		if recorder.status == http.StatusTooManyRequests {
			shed.Add(1)
		}
	}))
	t.Cleanup(ts.Close)

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: ts.URL,
		HolderPrefix:  "runner:test-host",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		SourceName:    "test runner",
	})
	cli := client.NewWithToken(cfg.ControllerURL, nil, "").WithRunnerIdentity(cfg.HolderPrefix)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	exec := func(ctx context.Context, n *store.Node, holderID string) {}
	if err := runPoolLoop(ctx, cfg, cli, exec, nil, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	if shed.Load() == 0 {
		t.Fatal("the controller shed nothing; each poll bought itself a fresh budget")
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
