package controller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSecretsRoundTrip(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	c := client.New(base, nil)
	ctx := context.Background()

	if err := c.CreateSecret(ctx, "api_token", "abc123", true); err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	got, err := c.GetSecret(ctx, "api_token")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != "abc123" {
		t.Fatalf("GetSecret value=%q want abc123", got.Value)
	}
	if got.Principal != "anonymous" {
		t.Fatalf("GetSecret principal=%q want anonymous", got.Principal)
	}

	secs, err := c.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(secs) != 1 {
		t.Fatalf("ListSecrets len=%d want 1", len(secs))
	}
	if secs[0].Value != "" {
		t.Fatalf("ListSecrets leaked value: %q", secs[0].Value)
	}
	if secs[0].Name != "api_token" {
		t.Fatalf("ListSecrets name=%q want api_token", secs[0].Name)
	}

	if err := c.DeleteSecret(ctx, "api_token"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := c.GetSecret(ctx, "api_token"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSecret after delete: want ErrNotFound, got %v", err)
	}
}

func TestSecrets_AdminReadResolvesTheNamedRunsRepository(t *testing.T) {
	f, _ := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	admin, _, err := f.store.CreateToken("ops", store.TokenKindUser,
		[]string{"admin"}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	c := client.NewWithToken(f.url, nil, admin)

	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)
	seedRunNode(t, f.store, "run-web", "build")
	setRunRepo(t, f.store, "run-web", "acme/web")

	if _, err := c.GetSecret(ctx, "DEPLOY_KEY"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSecret without a repository = %v, want ErrNotFound", err)
	}
	for _, get := range []struct {
		name string
		call func() (*client.Secret, error)
	}{
		{"by repo", func() (*client.Secret, error) { return c.GetSecretForRepo(ctx, "DEPLOY_KEY", "acme/web") }},
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
