package source_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func request() runspec.SealedRequest {
	return runspec.SealedRequest{Request: runspec.Request{
		Git: runspec.Git{
			Repository:    runspec.Repository{Key: "github:123", Display: "untrusted display"},
			SHA:           strings.Repeat("a", 40),
			DefaultBranch: runspec.Optional[string]{Set: true, Value: "trunk"},
		},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://github.com/team/repo", PipelineDir: ".sparkwing"}, Entrypoint: "ignored-entrypoint", BundleDigest: runspec.Optional[string]{Set: true, Value: "bundle-digest"}},
		Options: runspec.Options{
			RepoLocation: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "https://github.com/team/repo"}},
			Checkout:     runspec.Setting[runspec.CheckoutPolicy]{Optional: runspec.Optional[runspec.CheckoutPolicy]{Set: true, Value: runspec.CheckoutPolicy{Policy: nodeproto.SourceSpec{Depth: new(2)}}}},
		},
	}}
}

func TestSourceProjectionUsesCapturedAndAcceptedCheckoutWinners(t *testing.T) {
	r := request()
	first, err := source.ForRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if first.RemoteURL != r.Request.Source.Location.RemoteURL || first.RepoKey != "github:123" || first.SHA != r.Request.Git.SHA || first.DefaultBranch != "trunk" || first.BundleDigest != "bundle-digest" || *first.Checkout.Policy.Depth != 2 {
		t.Fatalf("request projection = %+v", first)
	}
	accepted := runspec.Spec{Request: r, Resolved: runspec.Resolved{
		Engine:     engineid.Identity{Version: "v1.2.3", ManifestDigest: "manifest"},
		Entrypoint: "pipelines/release", DefaultBranch: runspec.Optional[string]{Set: true, Value: "accepted-default"},
		Settings: runspec.ExecutionSettings{Options: runspec.Options{Checkout: runspec.Setting[runspec.CheckoutPolicy]{Optional: runspec.Optional[runspec.CheckoutPolicy]{Set: true, Value: runspec.CheckoutPolicy{Policy: nodeproto.SourceSpec{FullHistory: new(true)}}}}}},
	}}
	second, err := source.ForExecution(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if second.Checkout.Policy.Depth != nil || second.Checkout.Policy.FullHistory == nil || !*second.Checkout.Policy.FullHistory || second.Engine.Version != "v1.2.3" || second.SHA != first.SHA || second.PipelineDir != ".sparkwing" || second.DefaultBranch != "trunk" {
		t.Fatalf("execution projection = %+v", second)
	}
	*first.Checkout.Policy.Depth = 9
	if *r.Request.Options.Checkout.Value.Policy.Depth != 2 {
		t.Fatal("projection mutates sealed checkout")
	}
}

func TestSourceProjectionCannotInventLocationFromDisplay(t *testing.T) {
	r := request()
	r.Request.Source.Location = runspec.SourceLocation{}
	r.Request.Git.Repository.Display = "https://github.com/attacker/repo"
	if _, err := source.ForRequest(r); err == nil {
		t.Fatal("used repository display as executable location")
	}
}

func TestSourceProjectionKeepsResolvedDefaultWhenIntakeHadNone(t *testing.T) {
	r := request()
	r.Request.Git.DefaultBranch = runspec.Optional[string]{}
	spec, err := source.ForExecution(runspec.Spec{Request: r, Resolved: runspec.Resolved{DefaultBranch: runspec.Optional[string]{Set: true, Value: "trunk"}}})
	if err != nil || spec.DefaultBranch != "trunk" {
		t.Fatalf("resolved branch = %q, %v", spec.DefaultBranch, err)
	}
}

func TestSourceProjectionIgnoresLegacyLocationAndRetainsSealedDirectory(t *testing.T) {
	r := request()
	r.Request.Options.RepoLocation.Value = "https://attacker.example/replacement"
	accepted := runspec.Spec{Request: r, Resolved: runspec.Resolved{Entrypoint: "../replacement"}}
	got, err := source.ForExecution(accepted)
	if err != nil || got.RemoteURL != r.Request.Source.Location.RemoteURL || got.PipelineDir != ".sparkwing" {
		t.Fatalf("sealed source changed: %+v, %v", got, err)
	}
	for _, location := range []runspec.SourceLocation{
		{RemoteURL: "https://github.com/team/repo", LocalCheckout: "/checkout", PipelineDir: ".sparkwing"},
		{RemoteURL: "https://token@github.com/team/repo", PipelineDir: ".sparkwing"},
		{LocalCheckout: "relative", PipelineDir: ".sparkwing"},
		{RemoteURL: "https://github.com/team/repo", PipelineDir: "../escape"},
	} {
		r.Request.Source.Location = location
		if _, err := source.ForRequest(r); err == nil {
			t.Fatalf("accepted invalid location: %+v", location)
		}
	}
}
