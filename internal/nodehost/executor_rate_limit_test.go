package nodehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type rateLimitedStore struct {
	*executorStore
	mu       sync.Mutex
	refusals int
	after    time.Duration
	calls    []time.Time
}

func (s *rateLimitedStore) TransitionNode(ctx context.Context, command store.NodeCommand) (runstate.Applied, error) {
	s.mu.Lock()
	s.calls = append(s.calls, time.Now())
	refuse := s.refusals != 0
	if refuse {
		s.refusals--
	}
	s.mu.Unlock()
	if refuse {
		return runstate.Applied{}, &apierr.Error{Code: hosterr.RateLimited, Message: "team request budget exhausted", Details: map[string]any{"retry_after_ms": s.after.Milliseconds()}}
	}
	return s.executorStore.TransitionNode(ctx, command)
}

func (s *rateLimitedStore) attempts() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

func rateLimitedReporter(t *testing.T, refusals int, after time.Duration) (*nodeExecutor, NodeInput, *rateLimitedStore) {
	t.Helper()
	input, state, deps, _ := executorFixture(t)
	limited := &rateLimitedStore{executorStore: state, refusals: refusals, after: after}
	deps.Store = limited
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	return host.(*nodeExecutor), input, limited
}

func TestReportOutlastsRateLimitedTransitions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, input, limited := rateLimitedReporter(t, 5, 2*time.Second)
		if err := h.report(t.Context(), input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil); err != nil {
			t.Fatalf("report after five rate-limited refusals: %v", err)
		}
		changes := limited.changes
		if got := len(limited.attempts()); got != 6 || len(changes) != 1 || changes[0].Cause != runstate.CauseReport || changes[0].Extra.Result == nil || changes[0].Extra.Result.Outcome != runstate.NodeSuccess {
			t.Fatalf("attempts=%d recorded=%+v, want the result recorded on the sixth", got, changes)
		}
	})
}

func TestReportStopsRetryingWhenTheLeaseEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, input, limited := rateLimitedReporter(t, -1, time.Second)
		const leaseLength = 30 * time.Second
		keeper := lease.Keeper{Lease: leaseLength, Interval: 10 * time.Second, Renew: func(context.Context) (lease.Beat, error) {
			return lease.Beat{}, errors.New("controller unreachable")
		}}
		unit, stop := keeper.Run(t.Context())
		defer stop()
		start := time.Now()
		err := h.report(unit, input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil)
		var refused *executorTransitionError
		if !errors.Is(err, lease.ErrSilent) || errors.As(err, &refused) {
			t.Fatalf("report = %v, want the lease's silent cause", err)
		}
		if elapsed := time.Since(start); elapsed > leaseLength+5*time.Second || len(limited.attempts()) < 2 {
			t.Fatalf("report retried %d times for %v after a %v lease", len(limited.attempts()), elapsed, leaseLength)
		}
	})
}

func TestReportUnderAPlainContextStopsWithinOneLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, input, limited := rateLimitedReporter(t, -1, 7*time.Second)
		start := time.Now()
		err := h.report(context.Background(), input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil)
		var refused *executorTransitionError
		var failure *apierr.Error
		if !errors.As(err, &refused) || !errors.As(err, &failure) || failure.Code != hosterr.RateLimited {
			t.Fatalf("report = %v, want the last rate-limited refusal", err)
		}
		attempts := limited.attempts()
		if elapsed := time.Since(start); elapsed > limits.DefaultLease || len(attempts) < 2 {
			t.Fatalf("report retried %d times for %v, want retries ending within %v", len(attempts), elapsed, limits.DefaultLease)
		}
	})
}

func TestRateLimitedTransitionsSpreadTheirRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const advised = 5 * time.Second
		var wg sync.WaitGroup
		stores := make([]*rateLimitedStore, 2)
		start := time.Now()
		for i := range stores {
			h, input, limited := rateLimitedReporter(t, 1, advised)
			stores[i] = limited
			wg.Go(func() {
				if err := h.report(t.Context(), input, runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil); err != nil {
					t.Errorf("report %d: %v", i, err)
				}
			})
		}
		wg.Wait()
		first, second := stores[0].attempts(), stores[1].attempts()
		if len(first) != 2 || len(second) != 2 {
			t.Fatalf("attempts = %d and %d, want one retry each", len(first), len(second))
		}
		if first[1].Equal(second[1]) || first[1].Sub(start) <= advised || second[1].Sub(start) <= advised {
			t.Fatalf("retries at %v and %v after a shared %v Retry-After", first[1].Sub(start), second[1].Sub(start), advised)
		}
	})
}
