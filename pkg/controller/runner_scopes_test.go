package controller_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

var runnerScopes = []string{
	controller.ScopeNodesClaim,
	controller.ScopeTriggersClaim,
	controller.ScopeRunsState,
	controller.ScopeSecretsRead,
}

type scopedFixture struct {
	url   string
	store *store.Store
}

func newScopedFixture(t *testing.T, scopes []string) (scopedFixture, string) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if _, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, now); err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	raw, _, err := st.CreateToken("pool", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatalf("CreateToken runner: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().WithSecretsCipher(testCipher(t)).Handler())
	t.Cleanup(srv.Close)
	return scopedFixture{url: srv.URL, store: st}, raw
}

func seedRepoTrigger(t *testing.T, st *store.Store, id, repo string) {
	t.Helper()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: id, Pipeline: "deploy", Repo: repo, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTrigger %s: %v", id, err)
	}
}

func seedSecret(t *testing.T, st *store.Store, name, value, pipeline string, shared bool) {
	t.Helper()
	if err := teststore.SeedSecret(st, store.DefaultTeam, sealedSecret(t, store.DefaultTeam, store.Secret{
		Name: name, Value: value, Principal: "root", Pipeline: pipeline, Masked: true, Shared: shared,
	}), time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret %s/%s: %v", name, pipeline, err)
	}
}

func declareSecrets(t *testing.T, st *store.Store, runID string, names ...string) {
	t.Helper()
	var doc strings.Builder
	doc.WriteString(`{"nodes":[],"secrets":[`)
	for i, name := range names {
		if i > 0 {
			doc.WriteString(",")
		}
		fmt.Fprintf(&doc, `{"name":%q}`, name)
	}
	doc.WriteString(`]}`)
	if err := st.UpdatePlanSnapshot(context.Background(), runID, []byte(doc.String())); err != nil {
		t.Fatal(err)
	}
}

func setRunPipeline(t *testing.T, st *store.Store, runID, pipeline string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE runs SET pipeline = ? WHERE id = ?`, pipeline, runID); err != nil {
		t.Fatal(err)
	}
}

func (f scopedFixture) do(t *testing.T, token, method, path, body string) int {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
