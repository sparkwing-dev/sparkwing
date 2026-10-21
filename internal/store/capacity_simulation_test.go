package store_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Recovery starts with released claims: plan reacquisition must queue even for
// an original skip/fail policy, while their terminal reports remain admissible.
func TestAdmissionDeterministicSimulation(t *testing.T) {
	count := 12
	if raw := os.Getenv("SPARKWING_ADMISSION_SIM_SEEDS"); raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 1 {
			t.Fatalf("SPARKWING_ADMISSION_SIM_SEEDS must be a positive integer: %q", raw)
		}
	}
	for seed := range count {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) { simulateAdmission(t, uint64(seed)) })
	}
}

type admissionSimRun struct {
	id, host       string
	owner, holder  store.Actor
	admitted, done bool
}

type admissionSimulation struct {
	t        *testing.T
	f        *storeRecoveryFixture
	now      time.Time
	random   *rand.Rand
	runs     []admissionSimRun
	previous map[string]*store.ConcurrencyState
	lapsed   map[string]time.Time
	peak     map[string]int
}

func simulateAdmission(t *testing.T, seed uint64) {
	planCapacity := 1 + int(seed%2)
	jobCapacity := 1 + int(seed/2%2)
	policy := []string{"queue", "skip", "fail"}[seed%3]
	group := func(name, scope string, capacity int, policy string) nodeproto.ConcurrencySpec {
		return nodeproto.ConcurrencySpec{Group: name, Scope: new(scope), Capacity: new(capacity), Cost: new(1), OnLimit: new(policy), QueueTimeoutMS: new(int64(1))}
	}
	f := newStoreRecoveryFixtureAt(t, true, storetest.NewSQLite(t), func(doc *nodeproto.PlanDoc) {
		doc.Concurrency = []nodeproto.ConcurrencySpec{group("shared", "global", planCapacity, policy), group("local", "run", 1, "queue")}
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{group("shared", "box", jobCapacity, "queue"), group("local", "run", 1, "queue")}
		for i := range doc.Jobs[0].Concurrency {
			doc.Jobs[0].Concurrency[i].QueueTimeoutMS = nil
		}
	})
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sim := &admissionSimulation{t: t, f: f, now: now, random: rand.New(rand.NewPCG(seed, seed+17)), previous: map[string]*store.ConcurrencyState{}, lapsed: map[string]time.Time{}, peak: map[string]int{}}
	f.s.SetTestClock(t, func() time.Time { return sim.now })
	sim.runs = append(sim.runs, admissionSimRun{id: f.ref.RunID, host: "runner", owner: f.owner, holder: f.holder})
	principal, err := f.s.AsOperator().ResolveAuthority(t.Context(), f.holder.Authority)
	if err != nil || principal.Capability.Parent == nil {
		t.Fatalf("fixture execution machine: %+v, %v", principal, err)
	}
	machines := []string{principal.Capability.Parent.ID, ""}
	authority := f.plan.Seal.Request.AuthorityRef
	issued, err := f.s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(f.tenant.Team()), RegistrationID: "runner:planner:simulation", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "simulation"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	machines[1] = issued.Capability.Prefix
	for i := 1; i < 6; i++ {
		intake, err := f.tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: f.plan.Seal.Request.AuthorityRef, Request: f.plan.Seal.Request.Request})
		if err != nil {
			t.Fatal(err)
		}
		owner, err := f.claimOwner(t.Context(), intake.RunID, fmt.Sprintf("sim-owner-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		plan := f.plan
		plan.RunID, plan.Actor, plan.Seal.Request = intake.RunID, owner, intake.Request
		if _, err := f.tenant.AcceptPlan(t.Context(), plan); err != nil {
			t.Fatal(err)
		}
		ref := controllerwire.NodeRef{RunID: intake.RunID, NodeID: "job"}
		if _, err := f.tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: owner, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
			t.Fatal(err)
		}
		claim, err := f.s.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: machines[i/3], Target: &store.UnitRef{Kind: capability.UnitNode, RunID: intake.RunID, NodeID: "job"}, Available: match.Resources{Cores: 8}})
		if err != nil {
			t.Fatal(err)
		}
		holder := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
		if _, err := f.tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: acceptanceAttempt(0)}); err != nil {
			t.Fatal(err)
		}
		host := []string{"runner", "simulation"}[i/3]
		sim.runs = append(sim.runs, admissionSimRun{id: intake.RunID, host: host, owner: owner, holder: holder})
	}
	// The oldest waiter loses its owner while its independent claim keeps its
	// queue lease live. A younger request races recovery before adoption.
	for i := range planCapacity + 1 {
		result := sim.admit(i, false)
		want := store.AcquireQueued
		if i < planCapacity {
			want = store.AcquireGranted
		}
		if result != want {
			t.Fatalf("initial plan run=%d: %s, want %s", i, result, want)
		}
	}
	for i := range planCapacity {
		want := store.AcquireGranted
		if i >= jobCapacity {
			want = store.AcquireQueued
		}
		if got := sim.admit(i, true); got != want {
			t.Fatalf("shared box run=%d: %s, want %s", i, got, want)
		}
	}
	oldest := planCapacity
	sim.handoff(oldest)
	sim.settle(0)
	if got := sim.admit(len(sim.runs)-1, true); got != store.AcquireQueued {
		t.Fatalf("new arrival bypassed recovered waiter: %s", got)
	}
	sim.admit(oldest, true)
	found := false
	for _, holder := range sim.previous["plan:global:shared"].Holders {
		found = found || holder.RunID == sim.runs[oldest].id
	}
	if !found {
		t.Fatal("oldest continuous plan waiter did not progress")
	}
	sim.lapse(oldest)
	sim.admit(len(sim.runs)-1, true)
	if got := sim.admit(oldest, false); got != store.AcquireQueued {
		t.Fatalf("elapsed plan lease bypassed continuous waiters: %s", got)
	}
	sim.admit(oldest, true)
	if sim.runs[oldest].admitted {
		t.Fatal("elapsed lease revived ahead of continuous waiters")
	}
	// A released claim may finish while its parent queues again.
	sim.settle(oldest)
	sim.check()

	sim.recoverHost(len(sim.runs)-1, machines[1])

	// A bounded fair scheduler mixes retries, owner crashes and lease lapse.
	// Every round visits every unfinished run, so failure to drain is a cycle.
	for round := 0; round < 12; round++ {
		for _, i := range sim.random.Perm(len(sim.runs)) {
			run := &sim.runs[i]
			if run.done {
				continue
			}
			if round == 0 && i == oldest+1 {
				sim.handoff(i)
			}
			if !run.admitted {
				sim.admit(i, true)
			} else {
				sim.settle(i)
			}
			sim.check()
		}
	}
	for _, run := range sim.runs {
		if !run.done {
			t.Fatalf("fair drain stalled run %s", run.id)
		}
	}
	sim.check()
	if sim.peak["plan:global:shared"] != planCapacity || sim.peak[runspec.ConcurrencyKey("job", "box", "runner", "shared")] != min(planCapacity, jobCapacity) {
		t.Fatalf("simulation missed capacity saturation: peaks=%v", sim.peak)
	}
	for key, state := range sim.previous {
		if len(state.Holders)+len(state.Waiters) != 0 {
			t.Fatalf("released work retained %s: %+v", key, state)
		}
	}
}

func (s *admissionSimulation) admit(i int, job bool) store.AcquireKind {
	s.t.Helper()
	s.now = s.now.Add(time.Nanosecond)
	run := &s.runs[i]
	binding := store.CapacityBinding{RunID: run.id, Generation: run.owner.Owner.Generation}
	actor := run.owner
	if job {
		binding.NodeID, binding.Generation, actor = "job", run.holder.Generation, run.holder
	}
	result, err := s.f.tenant.AdmitCapacity(s.t.Context(), binding, actor)
	if err != nil || result.Kind != store.AcquireGranted && result.Kind != store.AcquireQueued {
		s.t.Fatalf("admit run=%d job=%t: %+v, %v", i, job, result, err)
	}
	if job {
		run.admitted = result.Kind == store.AcquireGranted
	}
	s.check()
	return result.Kind
}

func (s *admissionSimulation) handoff(i int) {
	s.t.Helper()
	run := &s.runs[i]
	if _, err := s.f.s.DB().ExecContext(s.t.Context(), `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?`, s.now.UnixNano(), s.f.tenant.Team(), run.id); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.f.tenant.RenewNodeClaim(s.t.Context(), controllerwire.NodeRef{RunID: run.id, NodeID: "job"}, run.holder); err != nil {
		s.t.Fatal(err)
	}
	units, err := s.f.s.AsOperator().ReapUnits(s.t.Context(), s.f.lease, 10)
	if err != nil || len(units) != 1 || !units[0].Requeued {
		s.t.Fatalf("crash/recovery run=%d: %+v, %v", i, units, err)
	}
	owner, err := s.f.claimOwner(s.t.Context(), run.id, fmt.Sprintf("recovered-%d-%d", i, run.owner.Owner.Generation))
	if err != nil {
		s.t.Fatal(err)
	}
	if owner.Owner.Generation <= run.owner.Owner.Generation {
		s.t.Fatalf("owner handoff retained generation %d", owner.Owner.Generation)
	}
	run.owner = owner
	s.check()
}

func (s *admissionSimulation) recoverHost(i int, machine string) {
	s.t.Helper()
	run := &s.runs[i]
	ref := controllerwire.NodeRef{RunID: run.id, NodeID: "job"}
	// A crashed host stops renewing both its claim and its job capacity.
	for _, table := range []string{"nodes", "concurrency_holders", "concurrency_waiters"} {
		if _, err := s.f.s.DB().ExecContext(s.t.Context(), `UPDATE `+table+` SET lease_expires_at=? WHERE team=? AND run_id=? AND node_id='job'`, s.now.UnixNano(), s.f.tenant.Team(), run.id); err != nil {
			s.t.Fatal(err)
		}
	}
	units, err := s.f.s.AsOperator().ReapUnits(s.t.Context(), s.f.lease, 10)
	if err != nil || len(units) != 1 || !units[0].Requeued || units[0].NodeID != "job" {
		s.t.Fatalf("host crash run=%d: %+v, %v", i, units, err)
	}
	if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.NodeTransition{Ref: ref, Actor: run.holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1)}); err == nil {
		s.t.Fatal("crashed host reported after expiry")
	}
	s.check()
	if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.NodeTransition{Ref: ref, Actor: run.owner, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		s.t.Fatalf("rerelease recovered job: %v", err)
	}
	claim, err := s.f.s.AsOperator().ClaimUnit(s.t.Context(), store.UnitDemand{CapabilityPrefix: machine, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: run.id, NodeID: "job"}, Available: match.Resources{Cores: 8}})
	if err != nil {
		s.t.Fatalf("reaward recovered job: %v", err)
	}
	if claim.Award.Generation <= run.holder.Generation {
		s.t.Fatal("host recovery retained the crashed award generation")
	}
	run.holder = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	row, err := s.f.tenant.ReadNode(s.t.Context(), ref)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.NodeTransition{Ref: ref, Actor: run.holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: acceptanceAttempt(row.Attempt)}); err != nil {
		s.t.Fatal(err)
	}
	run.admitted = false
	s.check()
}

func (s *admissionSimulation) lapse(i int) {
	s.t.Helper()
	// Shorten a live capacity lease, then cross its deadline using store time.
	// Renewal must retain the elapsed row's deadline, forcing fresh admission.
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		if _, err := s.f.s.DB().ExecContext(s.t.Context(), `UPDATE `+table+` SET lease_expires_at=? WHERE team=? AND run_id=?`, s.now.Add(time.Millisecond).UnixNano(), s.f.tenant.Team(), s.runs[i].id); err != nil {
			s.t.Fatal(err)
		}
	}
	s.now = s.now.Add(2 * time.Millisecond)
	if _, err := s.f.tenant.RenewNodeClaim(s.t.Context(), controllerwire.NodeRef{RunID: s.runs[i].id, NodeID: "job"}, s.runs[i].holder); err != nil {
		s.t.Fatal(err)
	}
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		var revived int
		if err := s.f.s.DB().QueryRowContext(s.t.Context(), `SELECT COUNT(*) FROM `+table+` WHERE team=? AND run_id=? AND lease_expires_at>?`, s.f.tenant.Team(), s.runs[i].id, s.now.UnixNano()).Scan(&revived); err != nil {
			s.t.Fatal(err)
		}
		if revived != 0 {
			s.t.Fatalf("renewal revived elapsed %s lease", table)
		}
	}
	s.runs[i].admitted = false
	s.lapsed[s.runs[i].id] = s.now
}

func (s *admissionSimulation) settle(i int) {
	s.t.Helper()
	run := &s.runs[i]
	ref := controllerwire.NodeRef{RunID: run.id, NodeID: "job"}
	row, err := s.f.tenant.ReadNode(s.t.Context(), ref)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.NodeTransition{Ref: ref, Actor: run.holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(row.Attempt)}); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.f.tenant.FinishRunAndUnit(s.t.Context(), store.FinishRunRequest{Transition: store.RunTransition{RunID: run.id, Actor: run.owner, To: store.RunSuccess, Cause: store.RunFinish}, Unit: run.owner.Owner}); err != nil {
		s.t.Fatal(err)
	}
	run.done = true
	s.check()
}

func (s *admissionSimulation) check() {
	s.t.Helper()
	rows, err := s.f.s.DB().QueryContext(s.t.Context(), `SELECT key FROM concurrency_entries WHERE team=? ORDER BY key`, s.f.tenant.Team())
	if err != nil {
		s.t.Fatal(err)
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			s.t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		s.t.Fatal(err)
	}
	for _, key := range keys {
		state, err := s.f.tenant.GetConcurrencyState(s.t.Context(), key)
		if err != nil {
			s.t.Fatal(err)
		}
		s.peak[key] = max(s.peak[key], state.UsedCost)
		if state.UsedCost > state.EffectiveCapacity {
			s.t.Fatalf("capacity exceeded %s: %+v", key, state)
		}
		level, scope, identity, _, ok := runspec.ParseConcurrencyKey(key)
		if !ok {
			s.t.Fatalf("invalid capacity key %s", key)
		}
		checkBinding := func(runID, nodeID string) {
			for _, run := range s.runs {
				if run.id == runID {
					if run.done || (level == "plan") != (nodeID == "") || scope == "run" && identity != run.id || scope == "box" && identity != run.host {
						s.t.Fatalf("capacity bound to wrong work/host: key=%s run=%+v node=%s", key, run, nodeID)
					}
					return
				}
			}
			s.t.Fatalf("unknown capacity owner %s in %s", runID, key)
		}
		for _, holder := range state.Holders {
			checkBinding(holder.RunID, holder.NodeID)
		}
		for _, waiter := range state.Waiters {
			checkBinding(waiter.RunID, waiter.NodeID)
		}
		previous := s.previous[key]
		if previous != nil {
			for _, old := range previous.Waiters {
				if !s.lapsed[old.RunID].Before(old.ArrivedAt) {
					continue
				}
				retained := false
				for _, waiter := range state.Waiters {
					if old.RunID == waiter.RunID && old.NodeID == waiter.NodeID {
						retained = true
						if !old.ArrivedAt.Equal(waiter.ArrivedAt) {
							s.t.Fatalf("continuous waiter lost arrival in %s: %+v -> %+v", key, old, waiter)
						}
					}
				}
				for _, holder := range state.Holders {
					if old.RunID == holder.RunID && old.NodeID == holder.NodeID {
						retained = true
					}
				}
				for _, run := range s.runs {
					if run.id == old.RunID && run.done {
						retained = true
					}
				}
				if !retained {
					s.t.Fatalf("continuous waiter disappeared from %s: %+v", key, old)
				}
			}
			for _, holder := range state.Holders {
				alreadyHeld := false
				for _, old := range previous.Holders {
					if old.HolderID == holder.HolderID && old.ClaimedAt.Equal(holder.ClaimedAt) && old.LeaseExpiresAt.After(s.now) {
						alreadyHeld = true
					}
				}
				if alreadyHeld {
					continue
				}
				for _, waiter := range state.Waiters {
					if !s.lapsed[waiter.RunID].Before(waiter.ArrivedAt) || !holder.LeaseExpiresAt.After(s.now) {
						continue
					}
					if waiter.ArrivedAt.Before(holder.QueueArrivedAt) || holder.QueueArrivedAt.IsZero() {
						s.t.Fatalf("FIFO bypass in %s: granted %s before live waiter %s", key, holder.RunID, waiter.RunID)
					}
				}
			}
		}
		s.previous[key] = state
	}
}
