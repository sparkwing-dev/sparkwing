package store

import (
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"testing/quick"
)

func TestRunViewNeverReturnsAnotherTenantsState(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	if view, err := tenant.ReadRunView(t.Context(), "run"); err != nil || len(view.Attempts) != 1 || view.Attempts[0].Identity.Attempt != 3 {
		t.Fatalf("own view: %+v, %v", view, err)
	}
	property := func(name string) bool {
		foreign := &Tenant{s: tenant.s, team: Team("foreign-" + hex.EncodeToString([]byte(name)))}
		view, err := foreign.ReadRunView(t.Context(), "run")
		return errors.Is(err, ErrNotFound) && view.Snapshot.Run.ID == "" && len(view.Snapshot.Nodes) == 0 && len(view.Attempts) == 0 && len(view.Logs) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestRunViewDiscardsPartialStateOnReadFailure(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `DROP TABLE log_streams`)
	view, err := tenant.ReadRunView(t.Context(), "run")
	if err == nil || view.Snapshot.Run.ID != "" || len(view.Attempts) != 0 {
		t.Fatalf("partial view escaped a failed snapshot: %+v, %v", view, err)
	}
}

func TestRunViewPagesKeepNumberBoundsOrderAndTenant(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	for _, row := range []struct {
		id     string
		number int
	}{{"second", 2}, {"fourth", 4}} {
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,?,?,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team=? AND id='run'`, row.id, row.number, tenant.team)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET error='masked failure' WHERE team=? AND id='run'`, tenant.team)
	for _, tc := range []struct {
		after, through int64
		limit          int
		want           []string
	}{{0, 4, 2, []string{"fourth", "second"}}, {0, 2, 2, []string{"second", "run"}}, {1, 3, 1, []string{"second"}}, {4, 4, 1, nil}} {
		views, err := tenant.ListRunViews(t.Context(), tc.after, tc.through, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, view := range views {
			ids = append(ids, view.Snapshot.Run.ID)
			if view.Snapshot.Run.ID == "run" && (!reflect.DeepEqual(view.Failures, []string{"masked failure"}) || len(view.Attempts) != 1) {
				t.Fatalf("incomplete page view: %+v", view)
			}
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("bounds %+v: got %v", tc, ids)
		}
	}
	property := func(name string) bool {
		foreign := &Tenant{s: tenant.s, team: Team("foreign-" + hex.EncodeToString([]byte(name)))}
		views, err := foreign.ListRunViews(t.Context(), 0, 100, 10)
		return err == nil && len(views) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `DROP TABLE log_streams`)
	if views, err := tenant.ListRunViews(t.Context(), 0, 100, 10); err == nil || len(views) != 0 {
		t.Fatalf("partial page escaped read failure: %+v %v", views, err)
	}
}
