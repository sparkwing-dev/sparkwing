package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestSecrets_PipelineScopeResolution(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	for _, s := range []struct{ name, value, pipeline string }{
		{"SHARED", "shared-value", ""},
		{"DEPLOY_KEY", "web-key", "deploy-web"},
		{"DEPLOY_KEY", "api-key", "deploy-api"},
	} {
		if err := teststore.SeedSecret(st, store.DefaultTeam, store.Secret{
			Name: s.name, Value: s.value, Principal: "alice", Pipeline: s.pipeline, Masked: true,
		}, now); err != nil {
			t.Fatalf("CreateOrReplaceSecret %s/%s: %v", s.name, s.pipeline, err)
		}
	}

	for _, tc := range []struct {
		name     string
		secret   string
		pipeline string
		want     string
		wantErr  bool
	}{
		{"pipeline owns the name", "DEPLOY_KEY", "deploy-web", "web-key", false},
		{"sibling pipeline gets its own", "DEPLOY_KEY", "deploy-api", "api-key", false},
		{"unscoped is readable by every pipeline", "SHARED", "deploy-web", "shared-value", false},
		{"no unscoped fallback exists", "DEPLOY_KEY", "", "", true},
		{"stranger pipeline has no row", "DEPLOY_KEY", "other-app", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretForPipeline(tc.secret, tc.pipeline)
			if tc.wantErr {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("GetSecretForPipeline(%q, %q) err = %v, want ErrNotFound", tc.secret, tc.pipeline, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetSecretForPipeline(%q, %q): %v", tc.secret, tc.pipeline, err)
			}
			if got.Value != tc.want {
				t.Errorf("GetSecretForPipeline(%q, %q) = %q, want %q", tc.secret, tc.pipeline, got.Value, tc.want)
			}
		})
	}

	if _, err := st.DB().ExecContext(t.Context(), `DELETE FROM secrets WHERE team=? AND name=? AND pipeline=?`, store.DefaultTeam, "DEPLOY_KEY", "deploy-web"); err != nil {
		t.Fatalf("DeleteSecret scoped: %v", err)
	}
	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretForPipeline("DEPLOY_KEY", "deploy-api"); err != nil {
		t.Errorf("deleting one pipeline's row removed a sibling's: %v", err)
	}
}
