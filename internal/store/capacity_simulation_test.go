package store_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAdmissionDeterministicSimulation(t *testing.T) {
	count := 12
	if raw := os.Getenv("SPARKWING_ADMISSION_SIM_SEEDS"); raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 1 {
			t.Fatalf("SPARKWING_ADMISSION_SIM_SEEDS must be a positive integer: %q", raw)
		}
	}
	for _, dialect := range []struct {
		name string
		open func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			for seed := range count {
				t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) { simulateAdmission(t, uint64(seed), dialect.open(t)) })
			}
		})
	}
}

type admissionSimRun struct {
	id, host                          string
	owner, holder                     runstate.Actor
	released, admitted, started, done bool
	claimLease, ownerLease            time.Time
	capacity                          map[string]admissionSimLease
}

type admissionSimLease struct {
	expires, resetAt time.Time
}

type admissionSimulation struct {
	t                *testing.T
	f                *storeRecoveryFixture
	now              time.Time
	random           *rand.Rand
	machines         [2]string
	runs             []admissionSimRun
	previous         map[string]*store.ConcurrencyState
	peak             map[string]int
	lapseJobRun      int
	jobLapsed        bool
	computeBusyRun   int
	computeWithdrawn bool
}

func simulateAdmission(t *testing.T, seed uint64, target *storetest.Target) {
	group := func(name, scope string, capacity int) nodeproto.ConcurrencySpec {
		return nodeproto.ConcurrencySpec{Group: name, Scope: new(scope), Capacity: new(capacity), Cost: new(1), OnLimit: new("queue")}
	}
	planCapacity := 1 + int(seed%2)
	f := newStoreRecoveryFixtureAt(t, false, target, func(doc *nodeproto.PlanDoc) {
		doc.Concurrency = []nodeproto.ConcurrencySpec{group("shared", "global", planCapacity), group("local", "run", 1)}
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{group("box-shared", "box", 1), group("shared", "global", 1), group("local", "run", 1)}
	})
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s := &admissionSimulation{t: t, f: f, now: now, random: rand.New(rand.NewPCG(seed, seed+17)), previous: map[string]*store.ConcurrencyState{}, peak: map[string]int{}}
	s.lapseJobRun = s.random.IntN(6)
	s.computeBusyRun = 1 + s.random.IntN(5)
	f.s.SetTestClock(t, func() time.Time { return s.now })
	s.runs = append(s.runs, admissionSimRun{id: string(f.ref.RunID), host: "runner", owner: f.owner, holder: f.holder, released: true})
	principal, err := f.s.AsOperator().ResolveAuthority(t.Context(), f.holder.Authority)
	if err != nil || principal.Capability.Parent == nil {
		t.Fatalf("fixture machine: %+v, %v", principal, err)
	}
	s.machines[0] = principal.Capability.Parent.ID
	authority := f.plan.Seal.Request.AuthorityRef
	issued, err := f.s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(f.tenant.Team()), RegistrationID: "runner:planner:simulation", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "simulation"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	s.machines[1] = issued.Capability.Prefix
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
		host := []string{"runner", "simulation"}[s.random.IntN(2)]
		s.runs = append(s.runs, admissionSimRun{id: intake.RunID, host: host, owner: owner})
	}
	for i := range s.runs {
		s.runs[i].capacity = map[string]admissionSimLease{}
		s.observeAuthorityLeases(i)
	}
	// The fixture has already released its first job through plan admission.
	s.reserve(0, "plan")
	// Every seed changes the coupled arrival, handoff and completion order.
	for _, i := range s.random.Perm(len(s.runs)) {
		s.release(i)
	}
	s.start(0)
	if !s.runs[0].started {
		t.Fatal("first released job did not execute")
	}
	// Losing plan capacity gates future releases, while released work still
	// acquires job capacity and reports under its original execution award.
	s.lapse(0, "plan")
	for _, i := range s.random.Perm(len(s.runs)) {
		if i != 0 {
			s.release(i)
		}
	}
	if got := s.admit(0, false); got != runstate.AcquireQueued {
		t.Fatalf("lapsed plan did not queue: %s", got)
	}
	s.handoff(0)
	if got := s.admit(0, true); got != runstate.AcquireGranted {
		t.Fatalf("released job gated by parent plan: %s", got)
	}
	s.settle(0)

	// A fair drain mixes both hosts with real claim loss and owner handoff.
	crashed, handed := false, false
	for round := 0; round < 30; round++ {
		for _, i := range s.random.Perm(len(s.runs)) {
			run := &s.runs[i]
			if run.done {
				continue
			}
			if !run.released {
				s.release(i)
			}
			if !run.released {
				s.check()
				continue
			}
			if !run.started {
				s.start(i)
				if run.holder.Kind != "" && !crashed && (round > 0 || s.random.IntN(3) == 0) {
					s.crashClaim(i)
					crashed = true
				}
			} else if !handed && s.random.IntN(3) == 0 {
				s.handoff(i)
				handed = true
			} else {
				s.settle(i)
			}
			s.check()
		}
	}
	if !crashed || !s.jobLapsed || !s.computeWithdrawn {
		t.Fatalf("simulation missed a crash, lease lapse or compute withdrawal: crashed=%t lapsed=%t compute=%t", crashed, s.jobLapsed, s.computeWithdrawn)
	}
	for _, run := range s.runs {
		if !run.done {
			t.Fatalf("fair scheduler deadlocked: %+v", run)
		}
	}
	s.check()
	for key, state := range s.previous {
		if len(state.Holders)+len(state.Waiters) != 0 {
			t.Fatalf("settled work retained %s: %+v", key, state)
		}
	}
	if s.peak["job:global:shared"] != 1 {
		t.Fatalf("job contention was not exercised: peaks=%v", s.peak)
	}
}

func (s *admissionSimulation) ref(i int) runspec.NodeRef {
	return runspec.NodeRef{RunID: runspec.RunID(s.runs[i].id), NodeID: "job"}
}

func (s *admissionSimulation) admit(i int, job bool) runstate.AcquireKind {
	s.t.Helper()
	s.now = s.now.Add(time.Nanosecond)
	run := &s.runs[i]
	binding := runstate.CapacityBinding{RunID: run.id, Generation: int64(run.owner.Owner.Generation)}
	actor := run.owner
	if job {
		binding.NodeID, binding.Generation, actor = "job", int64(run.holder.Generation), run.holder
	}
	result, err := s.f.tenant.MayStart(s.t.Context(), binding, actor)
	if err != nil || result.Kind != runstate.AcquireGranted && result.Kind != runstate.AcquireQueued {
		s.t.Fatalf("admit run=%d job=%t: %+v, %v", i, job, result, err)
	}
	level := "plan"
	if job {
		level = "job"
		run.admitted = result.Kind == runstate.AcquireGranted
	}
	s.reserve(i, level)
	s.check()
	return result.Kind
}

func (s *admissionSimulation) release(i int) {
	s.t.Helper()
	run := &s.runs[i]
	if run.released {
		return
	}
	result, err := s.f.tenant.TransitionNode(s.t.Context(), store.ReadyNode{Ref: s.ref(i), Actor: run.owner})
	if err != nil {
		s.t.Fatal(err)
	}
	run.released = !result.Waiting
	s.reserve(i, "plan")
	s.check()
}

func (s *admissionSimulation) start(i int) {
	s.t.Helper()
	run := &s.runs[i]
	if run.holder.Kind == "" {
		machine := s.machines[0]
		if run.host == "simulation" {
			machine = s.machines[1]
		}
		claim, err := s.f.s.AsOperator().ClaimUnit(s.t.Context(), store.UnitDemand{CapabilityPrefix: machine, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(run.id), NodeID: "job"}})
		if err != nil {
			s.t.Fatalf("claim run=%d: %v", i, err)
		}
		run.holder = runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(run.id), NodeID: "job", Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
		s.observeAuthorityLeases(i)
	}
	if s.admit(i, true) != runstate.AcquireGranted {
		return
	}
	if !s.jobLapsed && i == s.lapseJobRun {
		s.jobLapsed = true
		s.lapse(i, "job")
		if s.admit(i, true) != runstate.AcquireGranted {
			return
		}
	}
	row, err := s.f.tenant.ReadNode(s.t.Context(), s.ref(i))
	if err != nil {
		s.t.Fatal(err)
	}
	if !s.computeWithdrawn && i == s.computeBusyRun {
		s.computeWithdrawn = true
		stale := run.holder
		if err := s.f.tenant.RelinquishStart(s.t.Context(), runstate.CapacityBinding{RunID: run.id, NodeID: "job", Generation: int64(stale.Generation)}, stale); err != nil {
			s.t.Fatal(err)
		}
		after, err := s.f.tenant.ReadNode(s.t.Context(), s.ref(i))
		if err != nil || after.ClaimedBy != "" || after.ReadyAt == nil || row.ReadyAt == nil || !after.ReadyAt.Equal(*row.ReadyAt) || after.Attempt != row.Attempt {
			s.t.Fatalf("compute contention changed readiness ranking: before=%+v after=%+v error=%v", row, after, err)
		}
		if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.StartNode{Ref: s.ref(i), Actor: stale, SourceAttempt: acceptanceAttempt(row.Attempt)}); err == nil {
			s.t.Fatal("relinquished provisional award retained its permit")
		}
		run.holder, run.admitted = runstate.Actor{}, false
		if run.host == "runner" {
			run.host = "simulation"
		} else {
			run.host = "runner"
		}
		s.invalidate(i, "job")
		run.claimLease = s.now
		s.check()
		return
	}
	if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.StartNode{Ref: s.ref(i), Actor: run.holder, SourceAttempt: acceptanceAttempt(row.Attempt)}); err != nil {
		s.t.Fatal(err)
	}
	run.started = true
	s.check()
}

func (s *admissionSimulation) handoff(i int) {
	s.t.Helper()
	run := &s.runs[i]
	if _, err := s.f.s.DB().ExecContext(s.t.Context(), storetest.Rebind(s.f.s, `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?`), s.now.UnixNano(), s.f.tenant.Team(), run.id); err != nil {
		s.t.Fatal(err)
	}
	run.ownerLease = s.now
	if _, err := s.f.tenant.RenewNodeClaim(s.t.Context(), s.ref(i), run.holder); err != nil {
		s.t.Fatal(err)
	}
	s.observeAuthorityLeases(i)
	s.check()
	units, err := s.f.s.AsOperator().ReapUnits(s.t.Context(), s.f.lease, 10)
	if err != nil || len(units) != 1 || !units[0].Requeued {
		s.t.Fatalf("owner crash run=%d: %+v, %v", i, units, err)
	}
	owner, err := s.f.claimOwner(s.t.Context(), run.id, fmt.Sprintf("recovered-%d-%d", i, run.owner.Owner.Generation))
	if err != nil {
		s.t.Fatal(err)
	}
	if owner.Owner.Generation <= run.owner.Owner.Generation {
		s.t.Fatal("handoff retained owner fence")
	}
	run.owner = owner
	s.observeAuthorityLeases(i)
	s.check()
}

func (s *admissionSimulation) crashClaim(i int) {
	s.t.Helper()
	run := &s.runs[i]
	stale := run.holder
	// Claim expiry alone must preserve the owner-backed reservation and FIFO.
	if _, err := s.f.s.DB().ExecContext(s.t.Context(), storetest.Rebind(s.f.s, `UPDATE nodes SET lease_expires_at=? WHERE team=? AND run_id=? AND node_id='job'`), s.now.UnixNano(), s.f.tenant.Team(), run.id); err != nil {
		s.t.Fatal(err)
	}
	run.claimLease = s.now
	s.check()
	if _, err := s.f.tenant.MayStart(s.t.Context(), runstate.CapacityBinding{RunID: run.id, NodeID: "job", Generation: int64(stale.Generation)}, stale); err == nil {
		s.t.Fatal("expired claim obtained execution permit")
	}
	units, err := s.f.s.AsOperator().ReapUnits(s.t.Context(), s.f.lease, 10)
	if err != nil || len(units) != 1 || !units[0].Requeued || units[0].NodeID != "job" {
		s.t.Fatalf("claim crash: %+v, %v", units, err)
	}
	run.holder, run.admitted, run.started = runstate.Actor{}, false, false
	if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.ReadyNode{Ref: s.ref(i), Actor: run.owner}); err != nil {
		s.t.Fatal(err)
	}
	s.check()
}

func (s *admissionSimulation) lapse(i int, level string) {
	s.t.Helper()
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		if _, err := s.f.s.DB().ExecContext(s.t.Context(), storetest.Rebind(s.f.s, `UPDATE `+table+` SET lease_expires_at=? WHERE team=? AND run_id=? AND key LIKE ?`), s.now.UnixNano(), s.f.tenant.Team(), s.runs[i].id, level+":%"); err != nil {
			s.t.Fatal(err)
		}
	}
	s.invalidate(i, level)
}

func (s *admissionSimulation) settle(i int) {
	s.t.Helper()
	run := &s.runs[i]
	row, err := s.f.tenant.ReadNode(s.t.Context(), s.ref(i))
	if err != nil {
		s.t.Fatal(err)
	}
	if !run.admitted || !run.started {
		s.t.Fatalf("predicate/body without permit: %+v", run)
	}
	if _, err := s.f.tenant.TransitionNode(s.t.Context(), store.ReportNode{Ref: s.ref(i), Actor: run.holder, Outcome: runstate.NodeSuccess, SourceAttempt: acceptanceAttempt(row.Attempt)}); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.f.tenant.FinishRunAndUnit(s.t.Context(), runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: runspec.RunID(run.id), Actor: run.owner, Cause: runstate.RunFinish}, Unit: run.owner.Owner}); err != nil {
		s.t.Fatal(err)
	}
	run.done = true
	clear(run.capacity)
	s.check()
}

func (s *admissionSimulation) observeAuthorityLeases(i int) {
	s.t.Helper()
	run := &s.runs[i]
	var owner int64
	if err := s.f.s.DB().QueryRowContext(s.t.Context(), storetest.Rebind(s.f.s, `SELECT lease_expires_at FROM triggers WHERE team=? AND id=?`), s.f.tenant.Team(), run.id).Scan(&owner); err != nil {
		s.t.Fatal(err)
	}
	run.ownerLease = time.Unix(0, owner)
	if run.holder.Kind != "" {
		row, err := s.f.tenant.ReadNode(s.t.Context(), s.ref(i))
		if err != nil {
			s.t.Fatal(err)
		}
		if row.LeaseExpiresAt == nil {
			s.t.Fatal("claimed work has no claim lease")
		}
		run.claimLease = *row.LeaseExpiresAt
	}
}

func (s *admissionSimulation) invalidate(i int, level string) {
	run := &s.runs[i]
	lease := admissionSimLease{expires: s.now}
	// Arrival sequences can lead the wall clock when several sets arrive together.
	for key, state := range s.previous {
		keyLevel, _, _, _, _ := runspec.ParseConcurrencyKey(key)
		if keyLevel != level {
			continue
		}
		for _, holder := range state.Holders {
			if holder.RunID == run.id && holder.QueueArrivedAt.After(lease.resetAt) {
				lease.resetAt = holder.QueueArrivedAt
			}
		}
		for _, waiter := range state.Waiters {
			if waiter.RunID == run.id && waiter.ArrivedAt.After(lease.resetAt) {
				lease.resetAt = waiter.ArrivedAt
			}
		}
	}
	run.capacity[level] = lease
}

func (s *admissionSimulation) reserve(i int, level string) {
	run := &s.runs[i]
	horizon := run.ownerLease
	if run.claimLease.After(horizon) {
		horizon = run.claimLease
	}
	expires := s.now.Add(store.DefaultLeaseDuration)
	if horizon.Before(expires) {
		expires = horizon
	}
	lease := run.capacity[level]
	lease.expires = expires
	run.capacity[level] = lease
}

func (s *admissionSimulation) live(id, level string, arrival time.Time) bool {
	for _, run := range s.runs {
		if run.id == id {
			lease := run.capacity[level]
			return !run.done && lease.expires.After(s.now) && (arrival.IsZero() || arrival.After(lease.resetAt)) && (run.claimLease.After(s.now) || run.ownerLease.After(s.now))
		}
	}
	return false
}

func (s *admissionSimulation) check() {
	s.t.Helper()
	rows, err := s.f.s.DB().QueryContext(s.t.Context(), storetest.Rebind(s.f.s, `SELECT key FROM concurrency_entries WHERE team=? ORDER BY key`), s.f.tenant.Team())
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
	sets := map[string]map[string]int{}
	arrivals := map[string]time.Time{}
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
			s.t.Fatalf("invalid key %s", key)
		}
		checkBinding := func(runID, nodeID, kind string) {
			for _, run := range s.runs {
				if run.id != runID {
					continue
				}
				if run.done || (level == "plan") != (nodeID == "") || scope == "run" && identity != run.id || scope == "box" && identity != run.host {
					s.t.Fatalf("wrong work/host: key=%s run=%+v node=%s", key, run, nodeID)
				}
				set := runID + ":" + level
				if sets[set] == nil {
					sets[set] = map[string]int{}
				}
				sets[set][kind]++
				return
			}
			s.t.Fatalf("unknown owner %s in %s", runID, key)
		}
		checkArrival := func(runID string, arrival time.Time) {
			set := runID + ":" + level
			if previous, ok := arrivals[set]; ok && !previous.Equal(arrival) {
				s.t.Fatalf("group set has multiple arrival sequences: %s %s/%s", set, previous, arrival)
			}
			arrivals[set] = arrival
		}
		for _, holder := range state.Holders {
			checkBinding(holder.RunID, holder.NodeID, "holder")
			checkArrival(holder.RunID, holder.QueueArrivedAt)
		}
		for _, waiter := range state.Waiters {
			checkBinding(waiter.RunID, waiter.NodeID, "waiter")
			checkArrival(waiter.RunID, waiter.ArrivedAt)
		}
		if previous := s.previous[key]; previous != nil {
			checkRetained := func(runID, nodeID string, arrival time.Time) {
				if !s.live(runID, level, arrival) {
					return
				}
				retained := false
				check := func(currentRun, currentNode string, currentArrival time.Time) {
					if runID == currentRun && nodeID == currentNode {
						retained = true
						if !arrival.Equal(currentArrival) {
							s.t.Fatalf("live reservation lost arrival: %s %s/%s %s -> %s", key, runID, nodeID, arrival, currentArrival)
						}
					}
				}
				for _, waiter := range state.Waiters {
					check(waiter.RunID, waiter.NodeID, waiter.ArrivedAt)
				}
				for _, holder := range state.Holders {
					check(holder.RunID, holder.NodeID, holder.QueueArrivedAt)
				}
				if !retained {
					s.t.Fatalf("live reservation disappeared: %s %s/%s", key, runID, nodeID)
				}
			}
			for _, old := range previous.Waiters {
				checkRetained(old.RunID, old.NodeID, old.ArrivedAt)
			}
			for _, old := range previous.Holders {
				checkRetained(old.RunID, old.NodeID, old.QueueArrivedAt)
			}
			for _, holder := range state.Holders {
				alreadyHeld := false
				for _, old := range previous.Holders {
					alreadyHeld = alreadyHeld || old.HolderID == holder.HolderID && old.ClaimedAt.Equal(holder.ClaimedAt) && old.LeaseExpiresAt.After(s.now)
				}
				if alreadyHeld {
					continue
				}
				for _, waiter := range state.Waiters {
					if s.live(waiter.RunID, level, waiter.ArrivedAt) && holder.LeaseExpiresAt.After(s.now) && (waiter.ArrivedAt.Before(holder.QueueArrivedAt) || holder.QueueArrivedAt.IsZero()) {
						s.t.Fatalf("FIFO bypass: %s granted %s before %s", key, holder.RunID, waiter.RunID)
					}
				}
			}
		}
		s.previous[key] = state
	}
	for _, run := range s.runs {
		for _, level := range []string{"plan", "job"} {
			set := run.id + ":" + level
			kinds := sets[set]
			if kinds["holder"] != 0 && kinds["waiter"] != 0 {
				s.t.Fatalf("partial group grant: %s %v", set, kinds)
			}
			want := 3
			if level == "plan" {
				want = 2
			}
			// Expired rows can remain until reconciliation; live work cannot vanish.
			if s.live(run.id, level, time.Time{}) && kinds["holder"]+kinds["waiter"] != want {
				s.t.Fatalf("incomplete group set: %s %v want=%d", set, kinds, want)
			}
		}
	}
	for _, run := range s.runs {
		if !run.started || run.done {
			continue
		}
		var permitted int
		if err := s.f.s.DB().QueryRowContext(s.t.Context(), storetest.Rebind(s.f.s, `SELECT COUNT(*) FROM admission_work WHERE team=? AND run_id=? AND node_id='job' AND granted=1 AND generation=? AND holder=?`), s.f.tenant.Team(), run.id, run.holder.Generation, run.holder.Holder).Scan(&permitted); err != nil {
			s.t.Fatal(err)
		}
		if permitted != 1 {
			s.t.Fatalf("running work escaped award permit: %+v", run)
		}
	}
}
