package controller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSecrets_AdminReadResolvesTheNamedRunsPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f, _ := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	admin, _, err := f.store.CreateToken("ops", store.TokenKindUser,
		[]string{"admin"}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	c := client.NewWithToken(f.url, nil, admin)

	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "deploy-web", false)
	seedRunNode(t, f.store, "run-web", "build")
	setRunPipeline(t, f.store, "run-web", "deploy-web")

	if _, err := c.GetSecret(ctx, "DEPLOY_KEY"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSecret without a pipeline = %v, want ErrNotFound", err)
	}
	for _, get := range []struct {
		name string
		call func() (*client.Secret, error)
	}{
		{"by pipeline", func() (*client.Secret, error) { return c.GetSecretForPipeline(ctx, "DEPLOY_KEY", "deploy-web") }},
		{"by run", func() (*client.Secret, error) { return c.GetSecretForRun(ctx, "DEPLOY_KEY", "run-web") }},
	} {
		sec, err := get.call()
		if err != nil {
			t.Fatalf("admin read %s: %v", get.name, err)
		}
		if sec.Value != "web-key" {
			t.Errorf("admin read %s = %q, want web-key", get.name, sec.Value)
		}
	}
}
