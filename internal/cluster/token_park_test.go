package cluster

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/tokenpark"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type recordingClaimer struct {
	mu    sync.Mutex
	calls []time.Time
	fail  func(call int) error
}

func (c *recordingClaimer) ClaimNodeWithCapacity(context.Context, string, []string, time.Duration,
	*client.Headroom, *client.ClaimCapacity,
) (*store.Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, time.Now())
	return nil, c.fail(len(c.calls) - 1)
}

func (c *recordingClaimer) gaps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	gaps := make([]time.Duration, 0, len(c.calls))
	for i := 1; i < len(c.calls); i++ {
		gaps = append(gaps, c.calls[i].Sub(c.calls[i-1]))
	}
	return gaps
}

const testRunnerToken = "swr_abcdefgh_secretsecretsecret"

func revokedErr() error {
	return &client.TokenDeadError{State: "revoked", Message: "token is revoked"}
}

func tokenParkPoolConfig(changed func() bool) PoolLoopConfig {
	return normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL:     "http://stub",
		Token:             testRunnerToken,
		HolderPrefix:      "test",
		MaxConcurrent:     1,
		SourceName:        "test runner",
		CredentialChanged: changed,
	})
}

func TestRunPoolLoop_RevokedTokenParksForAnHour(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		claimer := &recordingClaimer{fail: func(int) error { return revokedErr() }}
		var logs strings.Builder
		logger := slog.New(slog.NewTextHandler(&logs, nil))

		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Minute)
		defer cancel()
		unchanged := func() bool { return false }
		if err := runPoolLoop(ctx, tokenParkPoolConfig(unchanged), claimer, nil, nil, logger); err != nil {
			t.Fatalf("runPoolLoop: %v", err)
		}

		gaps := claimer.gaps()
		if len(gaps) < 1 || len(gaps) > 2 {
			t.Fatalf("a revoked token made %d claims in 2.5h, want 2 or 3 (one an hour): gaps %v", len(gaps)+1, gaps)
		}
		for i, gap := range gaps {
			if gap < time.Hour || gap > time.Hour+15*time.Minute {
				t.Errorf("claim %d came %s after the last; a parked runner asks about once an hour", i+1, gap)
			}
		}
		out := logs.String()
		if got := strings.Count(out, "will never accept it again"); got != 1 {
			t.Fatalf("logged the dead-token error %d times, want once:\n%s", got, out)
		}
		if !strings.Contains(out, "swr_abcdefgh") || strings.Contains(out, "secretsecret") {
			t.Fatalf("the error must name the token prefix and never the secret:\n%s", out)
		}
		if !strings.Contains(out, "sparkwing cluster runners") {
			t.Fatalf("the error must say how to re-enroll:\n%s", out)
		}
	})
}

func TestRunPoolLoop_ConfigChangeEndsTheParkAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		claimer := &recordingClaimer{fail: func(int) error { return revokedErr() }}
		var rewritten atomic.Bool
		// safety: the rewrite lands between watch ticks, so the tick after it is the one that must see it.
		time.AfterFunc(32*time.Second, func() { rewritten.Store(true) })

		start := time.Now()
		err := runPoolLoop(t.Context(), tokenParkPoolConfig(rewritten.Load), claimer, nil, nil, discardLogger())
		if !errors.Is(err, ErrCredentialChanged) {
			t.Fatalf("runPoolLoop = %v, want ErrCredentialChanged so the agent reloads its config", err)
		}
		if waited := time.Since(start); waited != 7*tokenpark.WatchInterval {
			t.Fatalf("the park ended %s after start; a rewrite 32s in must end it at the next watch tick", waited)
		}
		if got := len(claimer.gaps()) + 1; got != 1 {
			t.Fatalf("made %d claims before reloading, want 1", got)
		}
	})
}

func TestRunPoolLoop_ConfigChangeDuringOrdinaryBackoffKeepsTheLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		claimer := &recordingClaimer{fail: func(int) error { return errors.New("controller 500: boom") }}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		err := runPoolLoop(ctx, tokenParkPoolConfig(func() bool { return true }), claimer, nil, nil, discardLogger())
		if err != nil {
			t.Fatalf("runPoolLoop = %v; a rewrite may end only a parked loop", err)
		}
	})
}

func TestRunPoolLoop_ServerErrorsBackOffExponentiallyToTheCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		claimer := &recordingClaimer{fail: func(int) error { return errors.New("controller 500: boom") }}
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		if err := runPoolLoop(ctx, tokenParkPoolConfig(nil), claimer, nil, nil, discardLogger()); err != nil {
			t.Fatalf("runPoolLoop: %v", err)
		}
		gaps := claimer.gaps()
		if len(gaps) < 12 || len(gaps) > 25 {
			t.Fatalf("made %d claims in an hour of 500s, want roughly 9 to reach the cap then one per 5m: %v", len(gaps)+1, gaps)
		}
		for i := 1; i < 8; i++ {
			if gaps[i] <= gaps[i-1] {
				t.Fatalf("gap %d (%s) did not grow from %s: %v", i, gaps[i], gaps[i-1], gaps)
			}
		}
		for i, gap := range gaps {
			if gap > client.FailureBackoffCap {
				t.Fatalf("gap %d is %s, past the %s cap", i, gap, client.FailureBackoffCap)
			}
		}
		if last := gaps[len(gaps)-1]; last < client.FailureBackoffCap*3/4 {
			t.Fatalf("the last gap is %s; sustained failures settle near the %s cap", last, client.FailureBackoffCap)
		}
	})
}

func TestRunPoolLoop_SuccessResetsTheBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		claimer := &recordingClaimer{fail: func(call int) error {
			if call == 6 {
				return nil
			}
			return errors.New("controller 500: boom")
		}}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		if err := runPoolLoop(ctx, tokenParkPoolConfig(nil), claimer, nil, nil, discardLogger()); err != nil {
			t.Fatalf("runPoolLoop: %v", err)
		}
		gaps := claimer.gaps()
		if len(gaps) < 8 {
			t.Fatalf("made %d claims, want at least 9: %v", len(gaps)+1, gaps)
		}
		if after := gaps[7]; after > 2*time.Second {
			t.Fatalf("the first failure after an answered claim waited %s; success must reset the backoff: %v", after, gaps)
		}
	})
}
