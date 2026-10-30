package controller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type launcherSyncStoreFailure struct {
	*store.Operator
	fail    bool
	failure error
}

func (s *launcherSyncStoreFailure) ReconcileLauncherAwards(ctx context.Context, ref capability.AuthorityRef, observed []capability.LauncherObservation, reservations, preparing, rejected []capability.LeaseRef, activations []capability.LauncherActivation) ([]store.LauncherAward, []store.LauncherPreparation, error) {
	if s.fail {
		if s.failure != nil {
			return nil, nil, s.failure
		}
		return nil, nil, errors.New("private-storage-failure")
	}
	return s.Operator.ReconcileLauncherAwards(ctx, ref, observed, reservations, preparing, rejected, activations)
}

func TestLauncherSyncFailureIsVisibleToPendingRun(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "sync-failure", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET status='pending',dispatch='controller'`)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
	engineSQL(t, e.Store, `INSERT INTO teams(name,created_at,updated_at,next_run_number) VALUES ('b',1,1,2)`)
	engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,lineage_json,log_retention_class,log_retention_days) SELECT 'b','second',number,pipeline,status,CAST(REPLACE(REPLACE(CAST(request_json AS TEXT),'"run"','"second"'),'"team":"a"','"team":"b"') AS BLOB),request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,lineage_json,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
	engineSQL(t, e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT 'b','second',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE team='a' AND id='run'`)
	second, err := e.Store.ForTeam(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "sync-failure-b", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	for range 2 {
		if _, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "launcher", Kinds: []capability.UnitKind{capability.UnitPlan}}); err != nil {
			t.Fatal(err)
		}
	}
	broken := &launcherSyncStoreFailure{Operator: e.Store.AsOperator(), fail: true}
	e.Launcher = storeLauncherSync{store: broken}
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e.EngineController).Handler()
	foreign := engineHTTPRoot(t, e, "", "other-launcher", []capability.Grant{capability.ClaimsLaunch})
	engineClaimHTTP(t, h, foreign, "POST", "/api/v1/launcher/sync", `{}`, 500)
	view, err := tenant.ReadRunView(t.Context(), "run")
	if err != nil || len(view.Failures) != 0 {
		t.Fatalf("foreign launcher changed the run: %+v, %v", view.Failures, err)
	}
	engineHTTPRoot(t, e, "a", "runner", []capability.Grant{capability.NodesClaim})
	if err := e.Store.AsOperator().RecordLauncherSyncFailure(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"}); err != nil {
		t.Fatal(err)
	}
	view, err = tenant.ReadRunView(t.Context(), "run")
	if err != nil || len(view.Failures) != 0 {
		t.Fatalf("non-Cloud runner changed the run: %+v, %v", view.Failures, err)
	}
	broken.fail, broken.failure = true, &apierr.Error{Code: apierr.Forbidden, Message: "binding refused"}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", `{}`, 403)
	view, err = tenant.ReadRunView(t.Context(), "run")
	if err != nil || len(view.Failures) != 0 {
		t.Fatalf("caller refusal changed the run: %+v, %v", view.Failures, err)
	}
	broken.failure = nil
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", `{}`, 500)
	if strings.Contains(w.Body.String(), "private-storage-failure") {
		t.Fatal("launcher response disclosed storage details")
	}
	view, err = tenant.ReadRunView(t.Context(), "run")
	if err != nil || len(view.Failures) != 1 || !strings.Contains(view.Failures[0].Error, "Cloud") || strings.Contains(view.Failures[0].Error, "private-storage-failure") {
		t.Fatalf("pending run has no safe Cloud failure: %+v, %v", view.Failures, err)
	}
	secondView, err := second.ReadRunView(t.Context(), "second")
	if err != nil || string(secondView.Snapshot.Run.State) != "pending" || len(secondView.Failures) != 1 || secondView.Failures[0].Error != view.Failures[0].Error {
		t.Fatalf("another team's pending plan has no safe Cloud failure: %+v, %v", secondView.Failures, err)
	}
	detail, err := e.RunDetail(t.Context(), capability.Authorized{Target: capability.Target{Team: "a", RunID: "run"}})
	if err != nil || detail.Status != "pending" || detail.Failure != view.Failures[0].Error {
		t.Fatalf("public run detail omitted reconciliation failure: %+v, %v", detail, err)
	}
	broken.fail = false
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", `{}`, 200)
	view, err = tenant.ReadRunView(t.Context(), "run")
	if err != nil || len(view.Failures) != 1 {
		t.Fatalf("healthy sync erased the diagnostic before work resumed: %+v, %v", view.Failures, err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET error='pipeline failure' WHERE team='a' AND id='run'`)
	for _, failed := range []bool{true, false} {
		broken.fail = failed
		status := 200
		if failed {
			status = 500
		}
		engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", `{}`, status)
		view, err = tenant.ReadRunView(t.Context(), "run")
		if err != nil || len(view.Failures) != 1 || view.Failures[0].Error != "pipeline failure" {
			t.Fatalf("sync changed an execution error: %+v, %v", view.Failures, err)
		}
	}
}
