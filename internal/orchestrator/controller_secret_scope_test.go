package orchestrator_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestControllerSecretResolverWithoutARunReadsOnlyUnscopedSecrets(t *testing.T) {
	isolateProfiles(t)
	p := newPathsWithStore(t)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, _, err := st.CreateToken("private-owner", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	srv := controller.New(st, nil).EnableAuthFromStore().WithSecretsCipher(cipher)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	c := client.NewWithToken(httpSrv.URL, nil, admin)
	ctx := context.Background()
	for _, sec := range []struct{ name, value, pipeline string }{
		{"DEPLOY_TOKEN", "own-pipeline", "orch-sec-reader"},
		{"DEPLOY_TOKEN", "foreign-pipeline", "other-pipeline"},
		{"NICE_TO_HAVE", "foreign-only", "other-pipeline"},
		{"LEGACY", "unscoped-value", ""},
	} {
		if err := c.CreateSecretForPipeline(ctx, sec.name, sec.value, sec.pipeline, false, false); err != nil {
			t.Fatal(err)
		}
	}
	spec := backends.Spec{Type: backends.TypeController, URL: httpSrv.URL + "/", Token: admin}
	standalone, err := sparkwing.NewSecretResolverFromSpec(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if v, _, err := standalone.Resolve(ctx, "LEGACY"); err != nil || v != "unscoped-value" {
		t.Fatalf("unscoped lookup value=%q err=%v", v, err)
	}
	if _, _, err := standalone.Resolve(ctx, "DEPLOY_TOKEN"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Fatalf("no-context scoped lookup=%v", err)
	}
}
