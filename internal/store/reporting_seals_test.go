package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestReportingReadsSealedFactsWithoutSecretValuesProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(423, 173))
	for i := range 16 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
			secret := fmt.Sprintf("private-%x", random.Uint64())
			public := fmt.Sprintf("visible-%x", random.Uint64())
			tenant, plan, err := storePlanSealRequestWith(t.Context(), s, func(in *store.IntakeRequest, _ *nodeproto.PlanDoc) {
				in.Request.RequestedArgs = map[string]string{"secret": secret, "public": public}
				in.Request.SecretArgs = runspec.Optional[[]string]{Set: i%2 == 0, Value: []string{"secret"}}
				in.Request.Git.Ref = "main"
				in.Request.Git.SHA = "abcdef1234abcdef1234abcdef1234abcdef1234"
				in.Request.Source.Ref = in.Request.Git.SHA
			})
			if err != nil {
				t.Fatal(err)
			}
			foreign := tenantFor(t, s, "foreign")
			for _, accepted := range []bool{false, true} {
				if accepted {
					plan.Seal.Resolved.SecretArgs = []string{"secret"}
					if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
						t.Fatal(err)
					}
				}
				run, err := tenant.GetRun(t.Context(), plan.RunID)
				if err != nil {
					t.Fatal(err)
				}
				wantPublic := public
				if !accepted && i%2 != 0 {
					wantPublic = runspec.RedactedArgValue
				}
				if run.Args["secret"] != runspec.RedactedArgValue || run.Args["public"] != wantPublic || run.GitSHA != "abcdef1234abcdef1234abcdef1234abcdef1234" || run.GitBranch != "main" || run.CreatedAt.IsZero() || !run.StartedAt.IsZero() {
					t.Fatalf("reported run = %+v", run)
				}
				body, err := json.Marshal(run)
				if err != nil || strings.Contains(string(body), secret) {
					t.Fatalf("secret in report: %s, %v", body, err)
				}
				rec := receipt.BuildReceipt(run, nil, 0, "")
				body, err = json.Marshal(rec)
				if err != nil || strings.Contains(string(body), secret) || rec.Identity.InputsHash != "" {
					t.Fatalf("unsafe receipt: %s %v", body, err)
				}

				filter := store.RunFilter{Pipelines: []string{"fixture"}, Text: []string{"acme"}}
				runs, err := tenant.ListRuns(t.Context(), filter)
				if err != nil || len(runs) != 1 || !reflect.DeepEqual(runs[0], run) {
					t.Fatalf("run list = %+v, %v", runs, err)
				}
				if count, err := tenant.CountRuns(t.Context(), filter); err != nil || count != 1 {
					t.Fatalf("run count = %d, %v", count, err)
				}
				if _, err := foreign.GetRun(t.Context(), plan.RunID); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("foreign read = %v", err)
				}
				if runs, err := foreign.ListRuns(t.Context(), filter); err != nil || len(runs) != 0 {
					t.Fatalf("foreign list = %+v, %v", runs, err)
				}
				trigger, err := tenant.GetTrigger(t.Context(), plan.RunID)
				if err != nil || trigger.Pipeline != run.Pipeline || trigger.Team != tenant.Team() || !reflect.DeepEqual(trigger.Args, run.Args) || runspec.ClaimGeneration(trigger.ClaimSeq) != plan.Actor.Owner.Generation {
					t.Fatalf("trigger = %+v, %v", trigger, err)
				}
				triggers, err := tenant.ListTriggers(t.Context(), store.TriggerFilter{Pipelines: []string{"fixture"}, Repo: "acme/repo"})
				if err != nil || len(triggers) != 1 || !reflect.DeepEqual(triggers[0], trigger) {
					t.Fatalf("trigger list = %+v, %v", triggers, err)
				}
				body, err = json.Marshal(triggers)
				if err != nil || strings.Contains(string(body), secret) {
					t.Fatalf("secret in trigger list: %s, %v", body, err)
				}
				if _, err := foreign.GetTrigger(t.Context(), plan.RunID); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("foreign trigger read = %v", err)
				}
				if triggers, err := foreign.ListTriggers(t.Context(), store.TriggerFilter{}); err != nil || len(triggers) != 0 {
					t.Fatalf("foreign trigger list = %+v, %v", triggers, err)
				}
			}
		})
	}
}

func TestReportingPagesUnstartedRunsAndFiltersQueueState(t *testing.T) {
	s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{plan.RunID}
	for range 6 {
		admitted, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: plan.Seal.Request.AuthorityRef, Request: plan.Seal.Request.Request})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, admitted.RunID)
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	var got []string
	filter := store.RunFilter{Limit: 2}
	for range len(ids) {
		page, err := tenant.ListRuns(t.Context(), filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, run := range page {
			got = append(got, run.ID)
		}
		last := page[len(page)-1]
		filter.AfterID, filter.AfterStartedAt = last.ID, store.RunCursorKey(last.StartedAt)
	}
	if !slices.Equal(ids, got) {
		t.Fatalf("pending cursor = %v, want %v", got, ids)
	}
	for status, count := range map[string]int{"pending": 6, "claimed": 1, "done": 0} {
		triggers, err := tenant.ListTriggers(t.Context(), store.TriggerFilter{Statuses: []string{status}})
		if err != nil || len(triggers) != count {
			t.Fatalf("queue %s = %+v, %v", status, triggers, err)
		}
	}
	for _, repo := range []string{"missing", "acme/repo' OR 1=1 --", "%", "_"} {
		triggers, err := tenant.ListTriggers(t.Context(), store.TriggerFilter{Repo: repo})
		if err != nil || len(triggers) != 0 {
			t.Fatalf("repo filter %q = %+v, %v", repo, triggers, err)
		}
	}
}

func TestReportingRefusesMalformedSeals(t *testing.T) {
	s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, `UPDATE runs SET request_json=? WHERE team=? AND id=?`), []byte(`{`), tenant.Team(), plan.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.GetRun(t.Context(), plan.RunID); err == nil {
		t.Fatal("corrupt request returned a run")
	}
	if _, err := tenant.ListTriggers(t.Context(), store.TriggerFilter{}); err == nil {
		t.Fatal("corrupt request returned a trigger")
	}
}

func TestReportingReadsCurrentJobLifecycle(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.New(t))
	foreign := tenantFor(t, f.s, "foreign")
	for _, started := range []bool{false, true} {
		if started {
			if _, err := f.tenant.TransitionNode(t.Context(), store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
		}
		current, err := f.tenant.ReadNode(t.Context(), f.ref)
		if err != nil {
			t.Fatal(err)
		}
		job, err := f.tenant.GetNode(t.Context(), string(f.ref.RunID), string(f.ref.NodeID))
		if err != nil || job.ClaimedBy != current.ClaimedBy || runspec.ClaimGeneration(job.ClaimGeneration) != current.Generation || job.AttemptsConsumed != int(current.Attempt) || !reflect.DeepEqual(job.StartedAt, current.StartedAt) || !reflect.DeepEqual(job.LeaseExpiresAt, current.LeaseExpiresAt) {
			t.Fatalf("job report = %+v, current = %+v, %v", job, current, err)
		}
		if len(job.ExecutionAttempts) != int(current.Attempt) {
			t.Fatalf("attempt report = %+v", job.ExecutionAttempts)
		}
		jobs, err := f.tenant.ListNodes(t.Context(), string(f.ref.RunID))
		if err != nil || len(jobs) != 1 || !reflect.DeepEqual(jobs[0], job) {
			t.Fatalf("job list = %+v, %v", jobs, err)
		}
		if _, err := foreign.GetNode(t.Context(), string(f.ref.RunID), string(f.ref.NodeID)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign job read = %v", err)
		}
		if jobs, err := foreign.ListNodes(t.Context(), string(f.ref.RunID)); err != nil || len(jobs) != 0 {
			t.Fatalf("foreign job list = %+v, %v", jobs, err)
		}
	}
}

func TestReportingDistinguishesUnknownAndEmptyClassification(t *testing.T) {
	for _, classification := range []struct {
		name         string
		names        []string
		set, visible bool
		refused      bool
	}{
		{name: "absent"},
		{name: "nil", set: true},
		{name: "empty", set: true, names: []string{}, visible: true},
		{name: "duplicate", set: true, names: []string{"value", "value"}},
		{name: "invalid", set: true, names: []string{""}, refused: true},
	} {
		t.Run(classification.name, func(t *testing.T) {
			s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
			tenant, request, err := storePlanSealRequestWith(t.Context(), s, func(in *store.IntakeRequest, _ *nodeproto.PlanDoc) {
				in.Request.RequestedArgs = map[string]string{"value": "public-when-classified"}
				in.Request.SecretArgs = runspec.Optional[[]string]{Set: classification.set, Value: classification.names}
			})
			if classification.refused {
				if err == nil || !strings.Contains(err.Error(), "absent argument") {
					t.Fatalf("invalid classification intake: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			run, err := tenant.GetRun(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			want := runspec.RedactedArgValue
			if classification.visible {
				want = "public-when-classified"
			}
			if run.Args["value"] != want {
				t.Fatalf("classification %s displayed %q", classification.name, run.Args["value"])
			}
			rec := receipt.BuildReceipt(run, nil, 0, "")
			if (rec.Identity.InputsHash != "") != classification.visible {
				t.Fatalf("classification %s hashed arguments: %+v", classification.name, rec.Identity)
			}
		})
	}
}
