package store_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type metricTransactions interface {
	WriteMetric(context.Context, capability.Authorized, store.MetricSample) error
	WriteUsage(context.Context, capability.Authorized, store.NodeUsage) error
}

func metricAuthorization(t *testing.T, f *storeRecoveryFixture, kind string) capability.Authorized {
	t.Helper()
	route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/"+kind)
	if !ok {
		t.Fatal("missing metric route")
	}
	target := capability.Target{Team: string(f.tenant.Team()), RunID: f.ref.RunID, NodeID: f.ref.NodeID}
	auth, err := capability.NewAuthenticator(f.s).Authenticate(t.Context(), capability.Presentation{Raw: f.holderBearer, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target}}, route)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(t.Context(), f.s, route, auth, target)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMetricTransactionsRecheckReportingAuthorityProperty(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	writer, ok := any(f.tenant).(metricTransactions)
	if !ok {
		t.Fatal("tenant does not implement metric transactions")
	}
	metric, usage := metricAuthorization(t, f, "metrics"), metricAuthorization(t, f, "usage")
	random := rand.New(rand.NewPCG(657, 944))
	for i := range 96 {
		a := metric
		switch i % 8 {
		case 0:
			a.Target.Team = fmt.Sprintf("foreign-%x", random.Uint64())
		case 1:
			a.Target.RunID = fmt.Sprintf("foreign-%x", random.Uint64())
		case 2:
			a.Target.NodeID = fmt.Sprintf("foreign-%x", random.Uint64())
		case 3:
			a.Binding.Generation += 1 + random.Int64N(10000)
		case 4:
			a.Binding.Holder = fmt.Sprintf("foreign-%x", random.Uint64())
		case 5:
			a.Binding.Attempt += 1 + random.Int64N(10000)
		case 6:
			a.Principal.Authority.ID = fmt.Sprintf("foreign-%x", random.Uint64())
		case 7:
			a.Access.Grants = []capability.Grant{capability.RunsRead}
		}
		if err := writer.WriteMetric(t.Context(), a, store.MetricSample{TS: time.Unix(1800000000, int64(i)), MemoryBytes: random.Int64N(1 << 40)}); err == nil {
			t.Fatalf("forged metric %d committed", i)
		}
		if err := writer.WriteUsage(t.Context(), a, store.NodeUsage{CPUTime: 1, Wall: 1, MaxRSSBytes: 1}); err == nil {
			t.Fatalf("forged usage %d committed", i)
		}
	}
	samples, err := f.tenant.ListNodeMetrics(t.Context(), f.ref.RunID, f.ref.NodeID)
	if err != nil || len(samples) != 0 {
		t.Fatalf("refused metrics = %v, %v", samples, err)
	}
	if err := writer.WriteMetric(t.Context(), metric, store.MetricSample{TS: time.Unix(1800000000, 0), MemoryBytes: 100}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteUsage(t.Context(), usage, store.NodeUsage{CPUTime: 3, Wall: 4, MaxRSSBytes: 5}); err != nil {
		t.Fatal(err)
	}
	row := metricUsage(t, f)
	if row.CPUTime != 3 || row.Wall != 4 || row.MaxRSSBytes != 5 {
		t.Fatalf("usage = %+v", row)
	}

	principal, err := f.s.ResolveAuthority(t.Context(), f.holder.Authority)
	if err != nil || principal.Capability.Parent == nil {
		t.Fatalf("claim ancestry = %+v, %v", principal.Capability.Parent, err)
	}
	if err := f.tenant.RevokeAuthority(t.Context(), store.Actor{Kind: store.ActorOperator, Authority: f.plan.Seal.Request.AuthorityRef}, *principal.Capability.Parent); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteMetric(t.Context(), metric, store.MetricSample{TS: time.Unix(1800000001, 0)}); err == nil {
		t.Fatal("revoked ancestor allowed a metric")
	}
	if err := writer.WriteUsage(t.Context(), usage, store.NodeUsage{CPUTime: 1}); err == nil {
		t.Fatal("revoked ancestor allowed usage")
	}
}

func TestMetricReportsFollowAttemptAndCancellation(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	before := metricAuthorization(t, f, "metrics")
	if _, err := f.tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: f.ref, Actor: f.holder, To: store.NodeRunning, Cause: store.CauseRetry, SourceAttempt: acceptanceAttempt(1), Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeFailed}}}); err != nil {
		t.Fatal(err)
	}
	sample := store.MetricSample{TS: time.Unix(1800000000, 0), MemoryBytes: 100}
	if err := f.tenant.WriteMetric(t.Context(), before, sample); err == nil {
		t.Fatal("previous attempt wrote a sample")
	}
	if _, err := f.tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: f.ref, Actor: f.owner, To: store.NodeCancelled, Cause: store.CauseCancel, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: "stop"}}}); err != nil {
		t.Fatal(err)
	}
	current := metricAuthorization(t, f, "metrics")
	if err := f.tenant.WriteMetric(t.Context(), current, sample); err != nil {
		t.Fatalf("cancelling holder cannot report: %v", err)
	}
	if err := f.tenant.WriteUsage(t.Context(), metricAuthorization(t, f, "usage"), store.NodeUsage{CPUTime: 1, Wall: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: f.ref, Actor: f.holder, To: store.NodeCancelled, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(2), Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.tenant.WriteMetric(t.Context(), current, sample); err == nil {
		t.Fatal("terminal holder wrote a sample")
	}
	if err := f.tenant.WriteUsage(t.Context(), current, store.NodeUsage{CPUTime: 1}); err == nil {
		t.Fatal("terminal holder wrote usage")
	}
}

func TestMetricRunHostReportsFinishedJobsUnderItsOwnFence(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	if _, err := f.tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: f.ref, Actor: f.holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1), Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeSuccess}}}); err != nil {
		t.Fatal(err)
	}
	principal, err := f.s.ResolveAuthority(t.Context(), f.owner.Authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"metrics", "usage"} {
		route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/"+kind)
		if !ok {
			t.Fatal("missing reporting route")
		}
		var live []capability.LiveAccess
		for i, access := range route.Access {
			if access.Binding == capability.BindRunOfTrigger {
				live = append(live, capability.LiveAccess{RuleIndex: i})
			}
		}
		target := capability.Target{Team: string(f.tenant.Team()), RunID: f.ref.RunID, NodeID: f.ref.NodeID}
		a, err := capability.Authorize(t.Context(), f.s, route, capability.Authenticated{Principal: principal, Method: route.Method, Pattern: route.Path, Live: live}, target)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "metrics" {
			err = f.tenant.WriteMetric(t.Context(), a, store.MetricSample{TS: time.Unix(1800000000, 0), MemoryBytes: 100})
		} else {
			err = f.tenant.WriteUsage(t.Context(), a, store.NodeUsage{CPUTime: 2, Wall: 3})
		}
		if err != nil {
			t.Fatalf("owner %s after job finish: %v", kind, err)
		}
	}
	if usage := metricUsage(t, f); usage.CPUTime != 2 || usage.Wall != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}
