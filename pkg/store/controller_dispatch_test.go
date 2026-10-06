package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

type dispatchRun struct {
	s        *store.Store
	run      string
	claimant store.ClaimIdentity
}

func newDispatchRun(t *testing.T, runID string) dispatchRun {
	t.Helper()
	return newDispatchRunOn(t, storetest.Open(t), runID)
}

func newDispatchRunOn(t *testing.T, s *store.Store, runID string) dispatchRun {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.CreatePlanNode(ctx, store.DefaultTeam, runID, now); err != nil {
		t.Fatalf("plan node: %v", err)
	}
	return dispatchRun{s: s, run: runID, claimant: store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"}}
}

func (f dispatchRun) claim(t *testing.T, nodeID string, kind store.ClaimTokenKind) store.ClaimToken {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	raw := f.claimRaw(t, nodeID, kind)
	tok, err := f.s.AuthorizeClaimToken(ctx, raw, store.ClaimResult, now)
	if err != nil {
		t.Fatalf("authorize %s: %v", nodeID, err)
	}
	return tok
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (f dispatchRun) accept(tok store.ClaimToken, body string) (bool, error) {
	return f.s.AcceptPlan(context.Background(), store.NewClaimResultCommit(tok, digestOf([]byte(body))), []byte(body), time.Now())
}

func (f dispatchRun) report(tok store.ClaimToken, rep store.AttemptReport) (bool, error) {
	body, _ := json.Marshal(rep)
	return f.s.ReportAttempt(context.Background(), store.NewClaimResultCommit(tok, digestOf(body)), rep, time.Now())
}

func (f dispatchRun) mustAccept(t *testing.T, body string) {
	t.Helper()
	if _, err := f.accept(f.claim(t, store.PlanNodeID, store.ClaimTokenPlan), body); err != nil {
		t.Fatalf("accept: %v", err)
	}
}

func (f dispatchRun) mustReport(t *testing.T, nodeID, outcome string) {
	t.Helper()
	if _, err := f.report(f.claim(t, nodeID, store.ClaimTokenWork), store.AttemptReport{Outcome: outcome}); err != nil {
		t.Fatalf("report %s %s: %v", nodeID, outcome, err)
	}
}

func (f dispatchRun) node(t *testing.T, nodeID string) *store.Node {
	t.Helper()
	n, err := f.s.GetNode(context.Background(), f.run, nodeID)
	if err != nil {
		t.Fatalf("get %s: %v", nodeID, err)
	}
	return n
}

func (f dispatchRun) wantReleased(t *testing.T, nodeID string, want bool) {
	t.Helper()
	n := f.node(t, nodeID)
	if got := n.ReadyAt != nil && n.Status == "pending"; got != want {
		t.Fatalf("%s released = %v (status %s, ready %v), want %v", nodeID, got, n.Status, n.ReadyAt, want)
	}
}

func (f dispatchRun) wantOutcome(t *testing.T, nodeID, outcome string) {
	t.Helper()
	if n := f.node(t, nodeID); n.Status != "done" || n.Outcome != outcome {
		t.Fatalf("%s = %s/%s, want done/%s", nodeID, n.Status, n.Outcome, outcome)
	}
}

func (f dispatchRun) wantRun(t *testing.T, status string) {
	t.Helper()
	r, err := f.s.GetRun(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != status {
		t.Fatalf("run %s = %s (%s), want %s", f.run, r.Status, r.Error, status)
	}
}

const hashA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func planOf(nodes ...string) string {
	var parts []string
	for _, spec := range nodes {
		spec, mods, _ := strings.Cut(spec, "|")
		id, deps, _ := strings.Cut(spec, ":")
		depList := []string{}
		if deps != "" {
			depList = strings.Split(deps, ",")
		}
		d, _ := json.Marshal(depList)
		node := fmt.Sprintf(`{"id":%q,"deps":%s,"spec_hash":%q`, id, d, hashA)
		if mods != "" {
			node += "," + mods
		}
		parts = append(parts, node+"}")
	}
	return `{"pipeline":"demo","nodes":[` + strings.Join(parts, ",") + `]}`
}

func TestAcceptPlan_InsertsNodesFinishesPlanAndReleasesRoots(t *testing.T) {
	f := newDispatchRun(t, "run-accept")
	f.wantReleased(t, store.PlanNodeID, true)
	f.mustAccept(t, planOf("a", "b:a", `c:a,b|"optional_deps":["ghost","a"]`))
	f.wantOutcome(t, store.PlanNodeID, "success")
	f.wantRun(t, "running")
	f.wantReleased(t, "a", true)
	f.wantReleased(t, "b", false)
	if deps := f.node(t, "c").Deps; strings.Join(deps, ",") != "a,b" {
		t.Fatalf("c deps = %v; an optional dependency the plan holds is a hard one, a missing one is dropped", deps)
	}
	f.mustReport(t, "a", "success")
	f.wantReleased(t, "b", true)
	f.wantReleased(t, "c", false)
	f.mustReport(t, "b", "success")
	f.mustReport(t, "c", "success")
	f.wantRun(t, "success")
}

// A plan is the planning claim's one result: the same body replays after the
// claim ends, any other body, or any body from an earlier generation, is a
// conflict, and none of them writes.
func TestAcceptPlan_OnePlanPerRun(t *testing.T) {
	f := newDispatchRun(t, "run-once")
	ctx := context.Background()
	stale := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), f.run, store.PlanNodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReapExpiredNodeClaims(ctx); err != nil {
		t.Fatal(err)
	}
	live := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	if live.Generation <= stale.Generation {
		t.Fatalf("requeued plan claim at generation %d, want above %d", live.Generation, stale.Generation)
	}
	if _, err := f.accept(stale, planOf("x")); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("plan from a lost generation: err = %v, want conflict", err)
	}
	body := planOf("a")
	if replayed, err := f.accept(live, body); err != nil || replayed {
		t.Fatalf("accept: replayed %v err %v", replayed, err)
	}
	if replayed, err := f.accept(live, body); err != nil || !replayed {
		t.Fatalf("identical resubmission: replayed %v err %v", replayed, err)
	}
	if _, err := f.accept(live, planOf("a", "b")); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("differing resubmission: err = %v, want conflict", err)
	}
	if n := f.node(t, store.PlanNodeID); n.ClaimedBy != "" || n.Status != "done" {
		t.Fatalf("the plan claim did not end at acceptance: %s claimed by %q", n.Status, n.ClaimedBy)
	}
	if err := f.s.ReplayClaimResult(ctx, live, digestOf([]byte(body))); err != nil {
		t.Fatalf("replay after the claim ended: %v", err)
	}
	if err := f.s.ReplayClaimResult(ctx, live, digestOf([]byte(planOf("a", "b")))); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("differing replay after the claim ended: err = %v", err)
	}
	if _, err := f.s.GetNode(ctx, f.run, "b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused plan wrote node b: %v", err)
	}
	if _, err := f.s.GetNode(ctx, f.run, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stale plan wrote node x: %v", err)
	}
}

func TestAcceptPlan_RefusesInvalidPlansAndWritesNothing(t *testing.T) {
	huge := make([]string, store.MaxPlanNodes+1)
	for i := range huge {
		huge[i] = fmt.Sprintf("n%d", i)
	}
	secrets := make([]string, store.MaxSecretsPerTeam+1)
	for i := range secrets {
		secrets[i] = fmt.Sprintf(`{"name":"S%d"}`, i)
	}
	cases := map[string]string{
		"secrets not a list":        `{"secrets":{"name":"S"},"nodes":[]}`,
		"empty secret name":         `{"secrets":[{"name":""}],"nodes":[]}`,
		"traversing secret name":    `{"secrets":[{"name":"a/../b"}],"nodes":[]}`,
		"secret name with a space":  `{"secrets":[{"name":"DEPLOY TOKEN"}],"nodes":[]}`,
		"too many secrets":          `{"secrets":[` + strings.Join(secrets, ",") + `],"nodes":[]}`,
		"empty pipeline ref":        `{"nodes":[{"id":"a","deps":[],"spec_hash":"` + hashA + `","pipeline_refs":[{"pipeline":"","node":"b"}]}]}`,
		"too many pipeline refs":    `{"nodes":[{"id":"a","deps":[],"spec_hash":"` + hashA + `","pipeline_refs":[` + strings.Repeat(`{"pipeline":"p","node":"n"},`, 32) + `{"pipeline":"p","node":"n"}]}]}`,
		"not json":                  `{"nodes":[`,
		"two documents":             planOf("a") + planOf("b"),
		"cycle":                     planOf("a:c", "b:a", "c:b"),
		"recovery cycle":            planOf(`a|"on_failure_of":"b"`, "b:a"),
		"unknown dependency":        planOf("a:ghost"),
		"self dependency":           planOf("a:a"),
		"duplicate id":              planOf("a", "a"),
		"reserved id":               planOf(store.PlanNodeID),
		"empty segment":             planOf("a//b"),
		"relative segment":          planOf("a/../b"),
		"control character":         planOf("a\tb"),
		"long id":                   planOf(strings.Repeat("x", store.MaxNodeIDBytes+1)),
		"unknown recovery":          planOf(`a|"on_failure_of":"ghost"`),
		"missing spec hash":         `{"nodes":[{"id":"a","deps":[]}]}`,
		"dynamic fan-out":           planOf(`a|"dynamic":true`),
		"approval skip_if":          planOf(`a|"approval":{"message":"ok?"},"modifiers":{"has_skip_if":true}`),
		"approval on_timeout":       planOf(`a|"approval":{"on_timeout":"retry"}`),
		"approval unknown field":    planOf(`a|"approval":{"approvers":["korey"]}`),
		"plan concurrency":          `{"plan_concurrency":{"key":"k"},"nodes":[]}`,
		"plan groups":               `{"plan_concurrency_groups":[{"key":"k"}],"nodes":[]}`,
		"when runner":               planOf(`a|"modifiers":{"when_runner":["gpu"]}`),
		"unknown modifier":          planOf(`a|"modifiers":{"retry_forever":true}`),
		"box concurrency":           planOf(`a|"modifiers":{"conc_group":"g","conc_scope":"box"}`),
		"unknown on_limit":          planOf(`a|"modifiers":{"conc_group":"g","conc_on_limit":"wait"}`),
		"groupless limit":           planOf(`a|"modifiers":{"conc_capacity":2}`),
		"unknown recovery modifier": planOf(`a|"modifiers":{"on_failure":"ghost"}`),
		"too many nodes":            planOf(huge...),
		"oversize":                  `{"nodes":[],"pad":"` + strings.Repeat("x", store.MaxPlanBytes) + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDispatchRun(t, "run-invalid")
			tok := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
			if _, err := f.accept(tok, body); !errors.Is(err, store.ErrPlanInvalid) {
				t.Fatalf("err = %v, want ErrPlanInvalid", err)
			}
			nodes, err := f.s.ListNodes(context.Background(), f.run)
			if err != nil || len(nodes) != 1 {
				t.Fatalf("a refused plan left %d nodes (%v)", len(nodes), err)
			}
			if n := f.node(t, store.PlanNodeID); n.Status != "pending" || n.ClaimedBy == "" {
				t.Fatalf("a refused plan moved the plan node to %s, claimed by %q", n.Status, n.ClaimedBy)
			}
			if _, err := f.accept(tok, planOf("a")); err != nil {
				t.Fatalf("the claim could not submit a valid plan after a refused one: %v", err)
			}
		})
	}
}

// The pipeline's requirements bind every node, node concurrency runs in the
// pod, and the snapshot upload can no longer replace the accepted plan.
func TestAcceptPlan_MergesRequiresAndKeepsTheAcceptedPlan(t *testing.T) {
	f := newDispatchRun(t, "run-requires")
	body := strings.Replace(planOf(
		`a|"modifiers":{"runs_on":["linux","gpu"],"conc_group":"deploy","conc_scope":"global","conc_on_limit":"queue","conc_capacity":1}`,
		"b:a",
	), `{"pipeline":"demo",`, `{"pipeline":"demo","requires":["gpu","arm64"],`, 1)
	f.mustAccept(t, body)
	if got := strings.Join(f.node(t, "a").NeedsLabels, ","); got != "linux,gpu,arm64" {
		t.Fatalf("a labels = %s", got)
	}
	if got := strings.Join(f.node(t, "b").NeedsLabels, ","); got != "gpu,arm64" {
		t.Fatalf("b labels = %s", got)
	}
	ctx := context.Background()
	if err := f.s.UpdatePlanSnapshot(ctx, f.run, []byte(`{"nodes":[]}`)); !errors.Is(err, store.ErrPlanAccepted) {
		t.Fatalf("snapshot upload over an accepted plan: err = %v", err)
	}
	if r, err := f.s.GetRun(ctx, f.run); err != nil || string(r.PlanSnapshot) != body {
		t.Fatalf("plan_json changed: %v", err)
	}
}

func TestAcceptPlan_ClampsResourcesAndRetries(t *testing.T) {
	f := newDispatchRun(t, "run-clamp")
	f.mustAccept(t, planOf(
		`big|"modifiers":{"retry":50,"retry_auto":true,"retry_backoff_ms":3600000,"res_cores":1000,"res_memory_bytes":-5}`,
		`inpod|"modifiers":{"retry":3,"res_cores":2}`,
	))
	var budget, backoff int64
	var cores float64
	var memory int64
	row := func(id string) {
		t.Helper()
		if err := f.s.DB().QueryRowContext(context.Background(), storetest.Rebind(f.s,
			`SELECT retry_budget, retry_backoff_ms, requested_cores, requested_memory_bytes
			   FROM nodes WHERE run_id = ? AND node_id = ?`), f.run, id).Scan(&budget, &backoff, &cores, &memory); err != nil {
			t.Fatal(err)
		}
	}
	row("big")
	if budget != store.MaxNodeRetries || backoff != store.MaxRetryBackoff.Milliseconds() || cores != 8 || memory != 0 {
		t.Fatalf("big: budget %d backoff %d cores %g memory %d", budget, backoff, cores, memory)
	}
	row("inpod")
	if budget != 0 || cores != 2 {
		t.Fatalf("a retry the pod runs itself got a controller budget %d (cores %g)", budget, cores)
	}
}

// The retry decision and the attempt's outcome commit together, so a failed
// attempt the node will retry never reads as a failure its dependents act on.
func TestReportAttempt_ARetryingFailureNeverReleasesDependents(t *testing.T) {
	f := newDispatchRun(t, "run-retry")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":2,"retry_auto":true,"retry_backoff_ms":1000}`, "b:a"))
	for attempt := 1; attempt <= 2; attempt++ {
		before := time.Now()
		f.mustReport(t, "a", "failed")
		a := f.node(t, "a")
		if a.Status != "pending" || a.Outcome != "" || a.AttemptsConsumed != attempt || a.ClaimedBy != "" {
			t.Fatalf("attempt %d: a = %s/%s consumed %d claimed %q", attempt, a.Status, a.Outcome, a.AttemptsConsumed, a.ClaimedBy)
		}
		if a.ReadyAt == nil || a.ReadyAt.Before(before.Add(time.Duration(attempt)*time.Second)) {
			t.Fatalf("attempt %d: requeued at %v, want the backoff after %v", attempt, a.ReadyAt, before)
		}
		f.wantReleased(t, "b", false)
		if b := f.node(t, "b"); b.Status != "pending" {
			t.Fatalf("attempt %d: b = %s/%s while a still has retries", attempt, b.Status, b.Outcome)
		}
		f.wantRun(t, "running")
	}
	f.mustReport(t, "a", "failed")
	f.wantOutcome(t, "a", "failed")
	f.wantOutcome(t, "b", "cancelled")
	f.wantRun(t, "failed")
	attempts, err := f.s.ListNodeExecutionAttempts(context.Background(), f.run, "a")
	if err != nil || len(attempts) != 3 {
		t.Fatalf("attempt rows = %d (%v), want one per attempt", len(attempts), err)
	}
}

func TestReportAttempt_ASuccessfulRetryReleasesDependents(t *testing.T) {
	f := newDispatchRun(t, "run-retry-ok")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":1,"retry_auto":true}`, "b:a"))
	f.mustReport(t, "a", "failed")
	f.wantReleased(t, "b", false)
	f.mustReport(t, "a", "success")
	f.wantReleased(t, "b", true)
}

func TestReportAttempt_EdgeKindsDecideDependents(t *testing.T) {
	f := newDispatchRun(t, "run-edges")
	f.mustAccept(t, planOf(
		`soft|"modifiers":{"continue_on_error":true}`,
		"after-soft:soft",
		`rescue|"on_failure_of":"soft"`,
		"ok",
		`unneeded|"on_failure_of":"ok"`,
		"hard",
		"after-hard:hard",
		"after-after:after-hard",
		`maybe|"modifiers":{"optional":true}`,
	))
	f.wantReleased(t, "rescue", false)
	f.mustReport(t, "soft", "failed")
	f.wantReleased(t, "after-soft", true)
	f.wantReleased(t, "rescue", true)
	f.mustReport(t, "ok", "success")
	f.wantOutcome(t, "unneeded", "skipped")
	f.mustReport(t, "hard", "failed")
	f.wantOutcome(t, "after-hard", "cancelled")
	f.wantOutcome(t, "after-after", "cancelled")
	f.mustReport(t, "maybe", "failed")
	f.mustReport(t, "after-soft", "success")
	f.mustReport(t, "rescue", "success")
	r, err := f.s.GetRun(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "failed" || strings.Contains(r.Error, "maybe") || !strings.Contains(r.Error, "hard") {
		t.Fatalf("run = %s %q; an optional failure must not count and a hard one must", r.Status, r.Error)
	}
}

// Recovery nodes react to the run's verdict and do not set it, so a failed
// recovery of an optional parent leaves the run successful.
func TestReportAttempt_ARecoveryNodeDoesNotDecideTheRun(t *testing.T) {
	f := newDispatchRun(t, "run-recovery-verdict")
	f.mustAccept(t, planOf(`maybe|"modifiers":{"optional":true,"on_failure":"rescue"}`, `rescue|"on_failure_of":"maybe"`, "ok"))
	f.mustReport(t, "maybe", "failed")
	f.mustReport(t, "rescue", "failed")
	f.mustReport(t, "ok", "success")
	f.wantRun(t, "success")
}

func TestReportAttempt_OptionalFailureLeavesTheRunSuccessful(t *testing.T) {
	f := newDispatchRun(t, "run-optional")
	f.mustAccept(t, planOf(`maybe|"modifiers":{"optional":true}`, "after:maybe"))
	f.mustReport(t, "maybe", "failed")
	f.mustReport(t, "after", "success")
	f.wantRun(t, "success")
}

func TestReportAttempt_APlanClaimReportsOnlyItsFailure(t *testing.T) {
	f := newDispatchRun(t, "run-plan-fail")
	tok := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	if _, err := f.report(tok, store.AttemptReport{Outcome: "success"}); !errors.Is(err, store.ErrAttemptInvalid) {
		t.Fatalf("plan success through the attempt route: err = %v", err)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed", Error: "plan: boom"}); err != nil {
		t.Fatal(err)
	}
	f.wantOutcome(t, store.PlanNodeID, "failed")
	f.wantRun(t, "failed")
}

// A fetch that may pass on a later attempt is retried within the budget, a
// planning node's included; a refused source credential is never retried.
func TestReportAttempt_SourceFailuresRetryByWhetherALaterAttemptCanPass(t *testing.T) {
	f := newDispatchRun(t, "run-plan-fetch")
	fetch := store.AttemptReport{Outcome: "failed", FailureReason: store.FailureSourceFetch}
	if _, err := f.report(f.claim(t, store.PlanNodeID, store.ClaimTokenPlan), fetch); err != nil {
		t.Fatal(err)
	}
	f.wantReleased(t, store.PlanNodeID, true)
	f.wantRun(t, "pending")

	g := newDispatchRunOn(t, f.s, "run-work-refused")
	g.mustAccept(t, planOf(`a|"modifiers":{"retry":2,"retry_auto":true}`))
	refused := store.AttemptReport{Outcome: "failed", FailureReason: store.FailureSourceUnavailable}
	if _, err := g.report(g.claim(t, "a", store.ClaimTokenWork), refused); err != nil {
		t.Fatal(err)
	}
	g.wantOutcome(t, "a", "failed")
	g.wantRun(t, "failed")
}

func TestReportAttempt_AnEndedClaimReplaysOnlyItsReport(t *testing.T) {
	f := newDispatchRun(t, "run-replay-attempt")
	f.mustAccept(t, planOf("a", "b:a"))
	tok := f.claim(t, "a", store.ClaimTokenWork)
	ref := commitOutput(t.Context(), t, f.s, store.DefaultTeam, f.run, "a", []byte(`{"v":1}`))
	rep := store.AttemptReport{Outcome: "success", Output: &ref}
	if _, err := f.report(tok, rep); err != nil {
		t.Fatal(err)
	}
	if replayed, err := f.report(tok, rep); err != nil || !replayed {
		t.Fatalf("identical report: replayed %v err %v", replayed, err)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed"}); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("differing report after the claim ended: err = %v", err)
	}
	f.wantOutcome(t, "a", "success")
	if a := f.node(t, "a"); a.OutputRef == nil || *a.OutputRef != ref {
		t.Fatalf("output = %+v", a.OutputRef)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "bogus"}); !errors.Is(err, store.ErrAttemptInvalid) {
		t.Fatalf("unknown outcome: err = %v", err)
	}
}

func TestSettle_IsIdempotent(t *testing.T) {
	f := newDispatchRun(t, "run-settle")
	f.mustAccept(t, planOf("a", "b:a", "c:b", "d"))
	f.mustReport(t, "a", "failed")
	f.wantReleased(t, "d", true)
	before := map[string]*store.Node{}
	for _, id := range []string{"a", "b", "c", "d"} {
		before[id] = f.node(t, id)
	}
	run, _ := f.s.GetRun(context.Background(), f.run)
	for range 2 {
		if err := store.SettleForTest(context.Background(), f.s, store.DefaultTeam, f.run, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for id, was := range before {
		now := f.node(t, id)
		if now.Status != was.Status || now.Outcome != was.Outcome || !sameTime(now.FinishedAt, was.FinishedAt) || !sameTime(now.ReadyAt, was.ReadyAt) {
			t.Fatalf("%s changed on a settle with nothing due: %+v -> %+v", id, was, now)
		}
	}
	again, _ := f.s.GetRun(context.Background(), f.run)
	if again.Status != "running" || !sameTime(again.FinishedAt, run.FinishedAt) {
		t.Fatalf("run changed on a settle with nothing due: %s %v -> %s %v", run.Status, run.FinishedAt, again.Status, again.FinishedAt)
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// Every sibling's report locks the run row and settles; the join must be
// released exactly once, only after the last sibling, whatever the order.
func TestReportAttempt_ManySiblingsFinishingTogetherReleaseTheJoinOnce(t *testing.T) {
	const siblings = 60
	f := newDispatchRun(t, "run-fanin")
	specs := make([]string, 0, siblings+1)
	ids := make([]string, 0, siblings)
	for i := range siblings {
		id := fmt.Sprintf("s%02d", i)
		ids = append(ids, id)
		specs = append(specs, id)
	}
	specs = append(specs, "join:"+strings.Join(ids, ","))
	f.mustAccept(t, planOf(specs...))
	toks := make([]store.ClaimToken, siblings)
	for i, id := range ids {
		toks[i] = f.claim(t, id, store.ClaimTokenWork)
	}
	var wg sync.WaitGroup
	errs := make(chan error, siblings)
	for _, tok := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.report(tok, store.AttemptReport{Outcome: "success"}); err != nil {
				errs <- fmt.Errorf("%s: %w", tok.NodeID, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	f.wantReleased(t, "join", true)
	f.mustReport(t, "join", "success")
	f.wantRun(t, "success")
}

// A plan listed deepest-first, where every node depends on every earlier one,
// is the worst order for a cascade: one failed root must still decide each
// dependent once, in the same report, with the verdict its upstreams imply.
func TestReportAttempt_AFailedRootCascadesThroughADenseReversedPlan(t *testing.T) {
	const depth = 100
	f := newDispatchRun(t, "run-dense-cascade")
	specs := []string{`rescue|"on_failure_of":"n99"`, "after-rescue:rescue"}
	for i := depth - 1; i >= 0; i-- {
		var deps []string
		for j := range i {
			deps = append(deps, fmt.Sprintf("n%d", j))
		}
		specs = append(specs, fmt.Sprintf("n%d:%s", i, strings.Join(deps, ",")))
	}
	f.mustAccept(t, planOf(specs...))
	f.wantReleased(t, "n0", true)
	f.mustReport(t, "n0", "failed")
	for i := 1; i < depth; i++ {
		id := fmt.Sprintf("n%d", i)
		f.wantOutcome(t, id, "cancelled")
		if n := f.node(t, id); n.Error != "upstream-failed" {
			t.Fatalf("%s error = %q, want upstream-failed", id, n.Error)
		}
	}
	f.wantOutcome(t, "rescue", "skipped")
	f.wantReleased(t, "after-rescue", true)
	f.wantRun(t, "running")
	f.mustReport(t, "after-rescue", "success")
	f.wantRun(t, "failed")
}
