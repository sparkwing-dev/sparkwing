package controller_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRepositoryResolveUsesTeamsPrivateAppInstallation(t *testing.T) {
	f := newAppFixture(t)
	owner, other := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(owner, 501, 7, acmeAdmin)
	f.connect(other, 502, 8, nil)
	cfg := f.app.Config()
	cfg.HTTP = &http.Client{Transport: automationTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "" {
			t.Error("anonymous GitHub request")
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	f.srv.WithGitHubApp(cfg)
	options := client.EngineOptions{URL: f.url, HTTP: &http.Client{Transport: automationTransport(func(r *http.Request) (*http.Response, error) {
		if err := controller.ConsoleFixtureRequest(f.srv, r, strings.TrimPrefix(owner.auth, "Session ")); err != nil {
			return nil, err
		}
		r.Header.Set("Origin", f.url)
		return http.DefaultTransport.RoundTrip(r)
	})}}
	for _, remote := range []string{"git@github.com:acme/plans.git", "https://github.com/acme/plans.git"} {
		got, err := client.ResolveRepository(t.Context(), options, remote)
		if err != nil || got.Key != "gh:702" || got.Display != "github.com/acme/plans" {
			t.Fatalf("private identity: %+v %v", got, err)
		}
	}
	var issued controllerwire.CreatedToken
	if code := f.call(http.MethodPost, "/api/v1/tokens", owner.auth, map[string]any{"grants": []string{"runs.write"}}, &issued); code != http.StatusCreated {
		t.Fatalf("create token: HTTP %d", code)
	}
	var reader controllerwire.CreatedToken
	if code := f.call(http.MethodPost, "/api/v1/tokens", owner.auth, map[string]any{"grants": []string{"runs.read"}}, &reader); code != http.StatusCreated {
		t.Fatalf("create reader token: HTTP %d", code)
	}
	readOptions := client.EngineOptions{URL: f.url, Bearer: func(context.Context) (string, error) { return reader.Capability, nil }}
	if got, err := client.ResolveRepository(t.Context(), readOptions, "git@github.com:acme/plans.git"); err != nil || got.Key != "gh:702" {
		t.Fatalf("reader identity: %+v %v", got, err)
	}

	parent, err := f.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(issued.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"gh:701", "gh:702", "github.com/acme/*"} {
		restricted, err := f.store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{scope}})
		if err != nil {
			t.Fatal(err)
		}
		scoped := client.EngineOptions{URL: f.url, Bearer: func(context.Context) (string, error) { return restricted.Raw, nil }}
		got, err := client.ResolveRepository(t.Context(), scoped, "git@github.com:acme/plans.git")
		if scope == "gh:701" {
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
				t.Fatalf("outside scope: %+v %v", got, err)
			}
		} else if err != nil || got.Key != "gh:702" {
			t.Fatalf("within scope: %+v %v", got, err)
		}
	}

	for _, tc := range []struct {
		auth, remote string
		status       int
	}{
		{other.auth, "git@github.com:acme/plans.git", http.StatusForbidden},
		{"", "git@github.com:acme/plans.git", http.StatusUnauthorized},
		{owner.auth, "https://user:secret@github.com/acme/plans.git", http.StatusBadRequest},
		{owner.auth, "", http.StatusBadRequest},
	} {
		status := f.call(http.MethodPost, "/api/v1/repositories/resolve", tc.auth, map[string]string{"remote_url": tc.remote}, nil)
		if status != tc.status {
			t.Fatalf("resolve %q: HTTP %d, want %d", tc.remote, status, tc.status)
		}
	}
	_, err = client.ResolveRepository(t.Context(), options, "https://github.com/acme/missing.git")
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
		t.Fatalf("uncovered repository: %v", err)
	}
}
