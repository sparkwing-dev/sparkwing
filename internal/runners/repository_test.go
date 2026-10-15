package runners

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func repositoryRunner(t *testing.T, unit controllerwire.ClaimedUnit, patterns []string, host UnitHost) *ManagedRunner {
	t.Helper()
	index, _ := runnerPolicyFixtures(t)
	repositories, err := sourceurl.ParseRepoAllowlist(patterns)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewManagedRunner(t.Context(), RuntimeConfig{
		Controller: client.EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "root", nil }},
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			return runtimeController{award: unit}, nil
		},
		Loop:     LoopConfig{Version: "v0.10.0", MaxConcurrent: 1, MaxClaims: 1},
		Versions: runtimePolicy{index: index}, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner},
		Repositories: repositories, Hosting: host,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func repositoryAward(kind capability.UnitKind, remote string) controllerwire.ClaimedUnit {
	unit := hostingAward(kind)
	unit.Request.Request.Git = runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)}
	unit.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"}
	if unit.Accepted != nil {
		unit.Accepted.Spec.Request = unit.Request
	}
	return unit
}

func TestMachineRunnerAdmitsSealedSourceBeforeHosting(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		for _, remote := range []string{"git@github.com:Acme/App.git", "https://github.com/acme/app.git", "ssh://git@github.com/acme/app"} {
			unit := repositoryAward(kind, remote)
			hosted := false
			runner := repositoryRunner(t, unit, []string{"github.com/acme/*"}, hostFunc(func(context.Context, controllerwire.ClaimedUnit) error {
				hosted = true
				return nil
			}))
			if err := runner.Run(t.Context()); err != nil || !hosted {
				t.Fatalf("%s %s: hosted=%v: %v", kind, remote, hosted, err)
			}
		}
	}
}

func FuzzMachineRunnerRepositoryFence(f *testing.F) {
	f.Add("acme", "app", uint8(0), true)
	f.Add("evil", "payload", uint8(1), false)
	f.Add("acme", "app", uint8(2), true)
	f.Fuzz(func(t *testing.T, owner, repo string, mode uint8, configured bool) {
		if len(owner)+len(repo) > 256 {
			t.Skip()
		}
		unit := repositoryAward(capability.UnitNode, "https://github.com/"+owner+"/"+repo+".git")
		patterns := []string(nil)
		if configured {
			patterns = []string{"github.com/acme/*"}
		}
		switch mode % 8 {
		case 1:
			unit.Accepted.Spec.Request.Request.Source.Location.RemoteURL = "https://git.invalid/evil/payload.git"
		case 2:
			unit.Request.Request.Source.Location.RemoteURL = ""
			unit.Request.Request.Source.Location.LocalCheckout = t.TempDir()
		case 3:
			unit.Request.Request.Source.Location.PipelineDir = "../outside"
		case 4:
			unit.Request.Request.Git.SHA = "HEAD"
		case 5:
			unit.Request.Request.Git.Repository.Key = ""
		case 6:
			unit.Request.Request.Git.SHA = ""
		case 7:
			unit.Request.Request.Source.Location.RemoteURL = ""
		}
		expected := false
		if mode%8 == 0 && configured {
			identity, err := sourceurl.Identity(unit.Request.Request.Source.Location.RemoteURL)
			expected = err == nil && strings.HasPrefix(identity, "github.com/acme/") && strings.Count(identity, "/") == 2
		}
		hosted := false
		runner := repositoryRunner(t, unit, patterns, hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { hosted = true; return nil }))
		err := runner.Run(t.Context())
		if hosted != expected || (err == nil) != expected || !expected && !errors.Is(err, ErrRepoNotAllowed) {
			t.Fatalf("mode=%d configured=%v hosted=%v expected=%v: %v", mode%8, configured, hosted, expected, err)
		}
	})
}
