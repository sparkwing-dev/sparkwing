package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type claimHoldFixture struct {
	e   *engineFixture
	h   http.Handler
	raw string
}

// safety: the run's trigger is held elsewhere and its one job is not ready yet,
// so a queue claim for jobs finds nothing.
func newClaimHoldFixture(t *testing.T, wake func(*store.Store) ClaimWake) claimHoldFixture {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	engineRuntimeClaim(t, e, capability.UnitTrigger)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','job','pending',?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	engineAcceptedFixturePlan(t, e, "a", "run")
	e.VersionPolicy = engineVersionPolicy(&engineVersionFake{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	if wake != nil {
		e.ClaimWake = wake(e.Store)
	}
	raw := engineHTTPRoot(t, e, "a", "root", []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
	return claimHoldFixture{e: e, h: e.Server.WithEngineController(e.EngineController).Handler(), raw: raw}
}

func (f claimHoldFixture) claim(ctx context.Context, path string, wait time.Duration) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequestWithContext(ctx, "POST", path, strings.NewReader(`{"runner_version":"v1","kinds":["node"]}`))
		r.Header.Set("Authorization", "Bearer "+f.raw)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(controllerwire.ClaimWaitHeader, strconv.FormatInt(wait.Milliseconds(), 10))
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		done <- w
	}()
	return done
}

func claimedJob(t *testing.T, w *httptest.ResponseRecorder) *controllerwire.ClaimedUnit {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", w.Code, w.Body.String())
	}
	var response controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Claim
}

type waitingWake struct {
	ClaimWake
	waiting chan struct{}
}

func (w waitingWake) NextGroupWaitDeadline(ctx context.Context, team store.Team, after time.Time) (time.Time, bool, error) {
	w.waiting <- struct{}{}
	return w.ClaimWake.NextGroupWaitDeadline(ctx, team, after)
}

// Runs on the real clock because the award checks leases the fixture took on it.
func TestHeldClaimAwardsWorkTheMomentACommitMakesItClaimable(t *testing.T) {
	wake := waitingWake{waiting: make(chan struct{}, 1)}
	f := newClaimHoldFixture(t, func(s *store.Store) ClaimWake { wake.ClaimWake = s; return wake })
	done := f.claim(t.Context(), "/api/v1/runners/claim", controllerwire.MaxClaimWait)
	select {
	case <-wake.waiting:
	case w := <-done:
		t.Fatalf("an empty claim that asked to wait answered at once: %d %s", w.Code, w.Body.String())
	}
	// safety: the direct write is invisible to the commit signal; the observation below is the commit that wakes the hold.
	engineSQL(t, f.e.Store, `UPDATE nodes SET ready_at=1,queue_started_at=? WHERE run_id='run' AND node_id='job'`, time.Now().UnixNano())
	if err := f.e.Store.AsOperator().RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "root", Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if unit := claimedJob(t, w); unit == nil || unit.JobID != "job" {
		t.Fatalf("held claim after the commit: %s", w.Body.String())
	}
	if w.Header().Get(controllerwire.ClaimHeldHeader) != "" {
		t.Fatal("an award was marked as a held empty answer")
	}
}

func TestHeldClaimAnswersEmptyAtItsBound(t *testing.T) {
	f := newClaimHoldFixture(t, func(s *store.Store) ClaimWake { return s })
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		w := <-f.claim(t.Context(), "/api/v1/runners/claim", time.Hour)
		if unit := claimedJob(t, w); unit != nil {
			t.Fatalf("empty queue awarded %+v", unit)
		}
		if elapsed := time.Since(start); elapsed != controllerwire.MaxClaimWait {
			t.Fatalf("held for %s, want the %s cap", elapsed, controllerwire.MaxClaimWait)
		}
		if w.Header().Get(controllerwire.ClaimHeldHeader) == "" {
			t.Fatal("a held empty answer did not say it waited")
		}
	})
}

type countingWake struct {
	ClaimWake
	searches atomic.Int32
	lapse    time.Duration
	lapseAt  time.Time
	lookup   func()
}

func (c *countingWake) NextCommit() <-chan struct{} {
	c.searches.Add(1)
	return c.ClaimWake.NextCommit()
}

func (c *countingWake) NextGroupWaitDeadline(_ context.Context, _ store.Team, after time.Time) (time.Time, bool, error) {
	if c.lookup != nil {
		c.lookup()
	}
	if c.lapseAt.IsZero() {
		c.lapseAt = time.Now().Add(c.lapse)
	}
	return c.lapseAt, c.lapseAt.After(after), nil
}

// A group wait that runs out is settled by a search, so the hold searches again
// at that moment rather than leaving the job until the bound.
func TestHeldClaimSearchesAgainWhenAGroupWaitRunsOut(t *testing.T) {
	wake := &countingWake{lapse: 2 * time.Second}
	f := newClaimHoldFixture(t, func(s *store.Store) ClaimWake { wake.ClaimWake = s; return wake })
	synctest.Test(t, func(t *testing.T) {
		done := f.claim(t.Context(), "/api/v1/runners/claim", controllerwire.MaxClaimWait)
		time.Sleep(wake.lapse - time.Millisecond)
		synctest.Wait()
		if n := wake.searches.Load(); n != 1 {
			t.Fatalf("searches before the group wait ran out: %d, want 1", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if n := wake.searches.Load(); n != 2 {
			t.Fatalf("searches once the group wait ran out: %d, want 2", n)
		}
		<-done
	})
}

// A group wait can run out between the search and the deadline lookup, so the
// lookup must still wake the hold for it rather than treat it as already settled.
func TestHeldClaimSearchesAgainWhenAGroupWaitRunsOutDuringTheSearch(t *testing.T) {
	wake := &countingWake{}
	wake.lookup = func() {
		if wake.searches.Load() == 1 {
			time.Sleep(2 * time.Second)
		}
	}
	f := newClaimHoldFixture(t, func(s *store.Store) ClaimWake { wake.ClaimWake = s; return wake })
	synctest.Test(t, func(t *testing.T) {
		wake.lapseAt = time.Now().Add(time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := f.claim(ctx, "/api/v1/runners/claim", controllerwire.MaxClaimWait)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		searches := wake.searches.Load()
		cancel()
		<-done
		if searches != 2 {
			t.Fatalf("searches after a group wait lapsed during the lookup: %d, want 2", searches)
		}
	})
}

// A lapse the last search already saw, such as a job this claimant cannot take,
// must not wake the hold again, or it would search in a loop until the bound.
func TestHeldClaimDoesNotSpinOnAGroupWaitItAlreadySaw(t *testing.T) {
	wake := &countingWake{}
	f := newClaimHoldFixture(t, func(s *store.Store) ClaimWake { wake.ClaimWake = s; return wake })
	synctest.Test(t, func(t *testing.T) {
		wake.lapseAt = time.Now().Add(-time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		wake.lookup = func() {
			if wake.searches.Load() > 2 {
				cancel()
			}
		}
		start := time.Now()
		<-f.claim(ctx, "/api/v1/runners/claim", controllerwire.MaxClaimWait)
		if n, elapsed := wake.searches.Load(), time.Since(start); n != 1 || elapsed != controllerwire.MaxClaimWait {
			t.Fatalf("hold with an already-lapsed group wait: %d searches over %s, want 1 over %s", n, elapsed, controllerwire.MaxClaimWait)
		}
	})
}

func TestHeldClaimEndsWhenTheRunnerLeaves(t *testing.T) {
	f := newClaimHoldFixture(t, func(s *store.Store) ClaimWake { return s })
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		done := f.claim(ctx, "/api/v1/runners/claim", controllerwire.MaxClaimWait)
		synctest.Wait()
		cancel()
		<-done
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("hold outlived its claimant by %s", elapsed)
		}
	})
}

// Cloud shares this claim path but has many writers to its store, so it never
// holds a claim, and a named claim never waits anywhere.
func TestClaimsThatMustNotWaitAnswerAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		wake bool
		path string
	}{
		{name: "controller without a wake", path: "/api/v1/runners/claim"},
		{name: "named run claim", wake: true, path: "/api/v1/runs/run/claim"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wake func(*store.Store) ClaimWake
			if tc.wake {
				wake = func(s *store.Store) ClaimWake { return s }
			}
			f := newClaimHoldFixture(t, wake)
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				w := <-f.claim(t.Context(), tc.path, controllerwire.MaxClaimWait)
				if elapsed := time.Since(start); elapsed != 0 {
					t.Fatalf("claim waited %s", elapsed)
				}
				if w.Header().Get(controllerwire.ClaimHeldHeader) != "" {
					t.Fatal("an unheld answer claimed it waited")
				}
			})
		})
	}
}
