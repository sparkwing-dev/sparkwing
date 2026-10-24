package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestChangedRevisionChildRequiredSecretSelection(t *testing.T) {
	s := newTestStore(t)
	parent, git := referenceAppProvenance(t, "refs/heads/main", referenceMain)
	tenant, id := childReference(t, s, parent, git, childRequest("deploy", "refs/heads/feature", referenceFeature))
	s.testOnlyExec(t, `INSERT INTO secrets(team,name,value,principal,pipeline,masked,shared,created_at,updated_at,only_on) VALUES ('a','TOKEN','excluded','owner','',1,0,1,1,'prod')`)
	s.planSecretOpener = fakePlanSecretOpener(func(_ context.Context, _ Team, secret Secret) (string, error) {
		if secret.Name == "TOKEN" {
			t.Fatal("excluded row reached plan decryption")
		}
		return secret.Value, nil
	})
	run, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Pipeline = "submitted-pipeline"
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		selection, err := runSecretSelection(run.Request)
		if err != nil {
			return err
		}
		if _, _, err := selectedSecretTx(t.Context(), tx, tenant.team, "TOKEN", selection); !errors.Is(err, ErrNotFound) {
			t.Fatalf("release: %v", err)
		}
		var refused *secretPreflightError
		if err := secretPreflightTx(t.Context(), tx, tenant.team, run, selection, []string{"TOKEN"}); !errors.As(err, &refused) {
			t.Fatalf("preflight accepted secret release refuses: %v", err)
		}
		if refused.Pipeline != run.Request.Provenance.Pipeline() {
			t.Fatalf("preflight pipeline = %q, want sealed %q", refused.Pipeline, run.Request.Provenance.Pipeline())
		}
		spec := runspec.Spec{Request: run.Request, Resolved: runspec.Resolved{SecretNames: []string{"TOKEN"}}}
		if _, err := planSecretsTx(t.Context(), tx, tenant.team, spec, selection); err != nil {
			t.Fatalf("excluded row reached plan decryption: %v", err)
		}
		if err := checkExpansionSecretPoolsTx(t.Context(), tx, tenant.team, spec, nil, selection); err != nil {
			t.Fatalf("excluded row restricted placement: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.testOnlyExec(t, `UPDATE secrets SET shared=1,only_on='' WHERE team='a' AND name='TOKEN'`)
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		selection, err := runSecretSelection(run.Request)
		if err != nil {
			return err
		}
		if _, _, err := selectedSecretTx(t.Context(), tx, tenant.team, "TOKEN", selection); err != nil {
			t.Fatalf("shared release: %v", err)
		}
		return secretPreflightTx(t.Context(), tx, tenant.team, run, selection, []string{"TOKEN"})
	})
	if err != nil {
		t.Fatalf("shared preflight: %v", err)
	}
}

func TestSecretSelectionScopeAndShadowing(t *testing.T) {
	s := newTestStore(t)
	referenceWorld(t, s, "gh:701")
	s.testOnlyExec(t, `INSERT INTO secrets(team,name,value,principal,pipeline,masked,shared,created_at,updated_at) VALUES ('a','TOKEN','team','owner','',0,1,1,1),('a','TOKEN','pipeline','owner','ci',0,0,1,1),('a','PRIVATE','private','owner','',0,0,1,1)`)
	for _, tc := range []struct {
		name      string
		selection secretSelection
		want      string
		private   bool
	}{
		{"none", secretSelection{}, "", false},
		{"shared", secretSelection{scope: secretSharedOnly}, "team", false},
		{"pipeline", secretSelection{scope: secretPipelineScope, pipeline: "ci"}, "pipeline", false},
		{"member", secretSelection{scope: secretPipelineScope, includeUnshared: true}, "team", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				selected, err := selectedSecretsTx(t.Context(), tx, "a", "", tc.selection)
				if err != nil {
					return err
				}
				got := map[string]string{}
				for _, row := range selected {
					if _, exists := got[row.secret.Name]; exists {
						t.Fatal("duplicate selected row")
					}
					got[row.secret.Name] = row.secret.Value
				}
				if got["TOKEN"] != tc.want || (got["PRIVATE"] != "") != tc.private {
					t.Fatalf("selected %v", got)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
