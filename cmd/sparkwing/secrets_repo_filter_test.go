package main

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSecretListRepositoryForms(t *testing.T) {
	manager := secretManagerFake{list: func() (controllerwire.Secrets, error) {
		return controllerwire.Secrets{Secrets: []controllerwire.SecretMetadata{
			{Name: "TOKEN", Repo: "github.com/acme/web", RepoKey: "gh:1", Pipeline: "deploy"},
			{Name: "OTHER", Repo: "github.com/acme/other", RepoKey: "gh:2", Pipeline: "deploy"},
			{Name: "REGION", Shared: true},
		}}, nil
	}}
	for _, repo := range []string{"acme/web", "github.com/acme/web", "https://github.com/acme/web", "https://github.com/acme/web.git", "https://GitHub.com/ACME/Web.git/", "git@github.com:acme/web.git", "gh:1"} {
		t.Run(repo, func(t *testing.T) {
			var out bytes.Buffer
			inv := secretInvocation(t, "list", "--repo", repo, "-o", "plain")
			if err := manageSecrets(t.Context(), inv, manager, nil, &out, io.Discard, false); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != "TOKEN\n" {
				t.Fatalf("list --repo %q = %q, want TOKEN", repo, got)
			}
		})
	}
}

func TestSecretListRefusesInvalidRepository(t *testing.T) {
	listed := false
	manager := secretManagerFake{list: func() (controllerwire.Secrets, error) {
		listed = true
		return controllerwire.Secrets{}, nil
	}}
	for _, repo := range []string{"web", "github.com/acme", "github.com/acme/web/extra", "acme//web", "acme/../web", "https://github.com/acme/web?ref=main", "https://github.com/acme/web#main", "https://github.com/", "https://[", "gh:0", "gh:01", "gh:abc"} {
		t.Run(repo, func(t *testing.T) {
			var out bytes.Buffer
			inv := secretInvocation(t, "list", "--repo", repo, "-o", "plain")
			if err := validateSecretInvocation(inv); err == nil {
				t.Fatal("invalid repository passed connection preflight")
			}
			err := manageSecrets(t.Context(), inv, manager, nil, &out, io.Discard, false)
			var usage *clisurface.UsageError
			if !errors.As(err, &usage) || usage.Path != "sparkwing secrets list" || out.Len() != 0 || listed {
				t.Fatalf("invalid --repo %q: output %q, error %v", repo, &out, err)
			}
		})
	}
}
