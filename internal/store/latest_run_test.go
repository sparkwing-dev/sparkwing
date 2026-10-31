package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func seedLatestRun(t *testing.T, tenant *Tenant, in IntakeRequest, pipeline, status string, startedAgo, finishedAgo time.Duration) string {
	t.Helper()
	in.Request.Pipeline = pipeline
	admitted, err := tenant.AdmitRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	var finished any
	if status != "running" {
		finished = tenant.s.now().Add(-finishedAgo).UnixNano()
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET status=?,started_at=?,finished_at=? WHERE team=? AND id=?`,
		status, tenant.s.now().Add(-startedAgo).UnixNano(), finished, tenant.team, admitted.RunID)
	return admitted.RunID
}

func TestGetLatestRun_ReturnsNewest(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	s := tenant.s
	seedLatestRun(t, tenant, request, "build", "success", 2*time.Hour, 90*time.Minute)
	seedLatestRun(t, tenant, request, "build", "success", 1*time.Hour, 45*time.Minute)
	newest := seedLatestRun(t, tenant, request, "build", "success", 10*time.Minute, 5*time.Minute)

	got, err := s.AsOperator().getLatestRun(context.Background(), "build", []string{"success"}, 0, true)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != newest {
		t.Fatalf("latest: got %q, want new", got.ID)
	}
}

func TestGetLatestRun_FiltersStatus(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	s := tenant.s
	older := seedLatestRun(t, tenant, request, "deploy", "success", 1*time.Hour, 45*time.Minute)
	seedLatestRun(t, tenant, request, "deploy", "failed", 10*time.Minute, 5*time.Minute)

	got, err := s.AsOperator().getLatestRun(context.Background(), "deploy", []string{"success"}, 0, true)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != older {
		t.Fatalf("want older-success, got %q", got.ID)
	}
}

func TestGetLatestRun_NotFound(t *testing.T) {
	tenant, _, _ := intakeFixture(t)
	s := tenant.s
	_, err := s.AsOperator().getLatestRun(context.Background(), "never-ran", []string{"success"}, 0, true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGetLatestRun_MaxAgeRespected(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	s := tenant.s
	stale := seedLatestRun(t, tenant, request, "build", "success", 2*time.Hour, 90*time.Minute)

	_, err := s.AsOperator().getLatestRun(context.Background(), "build", []string{"success"}, 1*time.Hour, true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("1h bound should exclude 90m-old run, got %v", err)
	}
	got, err := s.AsOperator().getLatestRun(context.Background(), "build", []string{"success"}, 2*time.Hour, true)
	if err != nil {
		t.Fatalf("2h bound should include 90m-old run: %v", err)
	}
	if got.ID != stale {
		t.Fatalf("want stale, got %q", got.ID)
	}
}

func TestGetLatestRun_EmptyStatusesMatchesAny(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	s := tenant.s
	seedLatestRun(t, tenant, request, "mix", "success", 1*time.Hour, 45*time.Minute)
	newest := seedLatestRun(t, tenant, request, "mix", "failed", 10*time.Minute, 5*time.Minute)

	got, err := s.AsOperator().getLatestRun(context.Background(), "mix", nil, 0, true)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if got.ID != newest {
		t.Fatalf("empty statuses: want newer-failed, got %q", got.ID)
	}
}

func TestGetLatestRun_RejectsEmptyPipeline(t *testing.T) {
	tenant, _, _ := intakeFixture(t)
	s := tenant.s
	if _, err := s.AsOperator().getLatestRun(context.Background(), "", nil, 0, true); err == nil {
		t.Fatal("expected error for empty pipeline")
	}
}

func TestGetLatestRun_TenantExcludesNewerForeignRun(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	own := seedLatestRun(t, tenant, request, "shared", "success", time.Hour, time.Minute)
	root := authorityRoot("other", "b")
	root.Grants = []capability.Grant{capability.RunsWrite}
	authorityPersist(t, tenant.s, root)
	other := &Tenant{s: tenant.s, team: "b"}
	request.Authority.ID, request.Request.Team = "other", "b"
	foreign := seedLatestRun(t, other, request, "shared", "success", time.Minute, time.Minute)
	got, err := tenant.GetLatestRun(t.Context(), "shared", nil, 0, true)
	if err != nil || got.ID != own {
		t.Fatalf("tenant latest: %+v %v", got, err)
	}
	got, err = tenant.s.AsOperator().getLatestRun(t.Context(), "shared", nil, 0, true)
	if err != nil || got.ID != foreign {
		t.Fatalf("operator latest: %+v %v", got, err)
	}
}

func TestGetLatestRun_PrivateInputProjectionProperty(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	property := func(value string, phase uint8, accepted bool) bool {
		value = "secret-" + value
		request.Request.RequestedArgs = map[string]string{"private": value}
		request.Request.SecretArgs = runspec.Optional[[]string]{}
		switch phase % 4 {
		case 1:
			request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{}}
		case 2:
			request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"private"}}
		case 3:
			request.Request.SecretArgs.Set = true
		}
		id := seedLatestRun(t, tenant, request, "private-inputs", "success", time.Minute, time.Minute)
		if accepted {
			row, err := tenant.ReadRun(t.Context(), id)
			if err != nil {
				return false
			}
			spec, err := json.Marshal(runspec.Spec{Request: row.Request, Hash: "private-spec-hash", Resolved: runspec.Resolved{
				Args: request.Request.RequestedArgs, SecretArgs: request.Request.SecretArgs.Value,
			}})
			if err != nil {
				return false
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=?,spec_hash='private-spec-hash',plan_json=?,plan_hash='private-plan-hash',sealed_at=? WHERE team=? AND id=?`,
				spec, []byte(`{"jobs":[]}`), tenant.s.now().UnixNano(), tenant.team, id)
		}
		got, err := tenant.GetLatestRun(t.Context(), "private-inputs", nil, 0, true)
		if err != nil || got.ID != id {
			return false
		}
		want := runspec.RedactedArgValue
		if phase%4 == 1 {
			want = value
		}
		return got.Args["private"] == want && got.Invocation["args"].(map[string]string)["private"] == want &&
			got.Invocation["spec_hash"] == nil && got.Invocation["request_hash"] == nil && got.Invocation["env_digest"] == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestLatestRunDefaultsToVerifiedDefaultBranch(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	main := seedLatestRun(t, tenant, request, "build", "success", time.Hour, 45*time.Minute)
	row, err := tenant.ReadRun(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := runspec.AppProvenance(runspec.AppFacts{
		Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "latest-main",
		VerifiedAt: tenant.s.now(), Ref: "refs/heads/main", SHA: fixtureOtherSHA,
		DefaultBranch: "main", DefaultBranchPush: true,
	}, runspec.Optional[runspec.PullRequest]{}, "build")
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE team=? AND id=?`, sealWith(t, row.Request, provenance), tenant.team, main)
	uploaded := seedLatestRun(t, tenant, request, "build", "success", 10*time.Minute, 5*time.Minute)
	got, err := tenant.GetLatestRun(t.Context(), "build", []string{"success"}, 0, false)
	if err != nil || got.ID != main {
		t.Fatalf("default selected unverified upload: got=%+v err=%v", got, err)
	}
	got, err = tenant.GetLatestRun(t.Context(), "build", []string{"success"}, 0, true)
	if err != nil || got.ID != uploaded {
		t.Fatalf("explicit opt-out: got=%+v err=%v", got, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET intake_mode='publish',request_json=? WHERE team=? AND id=?`, sealWith(t, row.Request, provenance), tenant.team, uploaded)
	got, err = tenant.GetLatestRun(t.Context(), "build", []string{"success"}, 0, false)
	if err != nil || got.ID != main {
		t.Fatalf("default selected publication: got=%+v err=%v", got, err)
	}
}

func TestLatestRunDoesNotFallBackToUnverifiedOutput(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	seedLatestRun(t, tenant, request, "build", "success", time.Minute, 0)
	if _, err := tenant.GetLatestRun(t.Context(), "build", []string{"success"}, 0, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unverified-only pipeline: want not found, got %v", err)
	}
}
