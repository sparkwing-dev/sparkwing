package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func FuzzRunnerCLIEnforcesRepositoryFence(f *testing.F) {
	f.Add("acme", "app", false)
	f.Add("foreign", "payload", false)
	f.Add("acme", "app", true)
	f.Fuzz(func(t *testing.T, owner, repo string, acceptedForeign bool) {
		if len(owner)+len(repo) > 256 {
			t.Skip()
		}
		unit := controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "award"}
		unit.Request = runspec.SealedRequest{RunID: "run", Request: runspec.Request{
			Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)},
			Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://github.com/" + owner + "/" + repo + ".git", PipelineDir: ".sparkwing"}},
		}}
		unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request}}
		if acceptedForeign {
			unit.Accepted.Spec.Request.Request.Source.Location.RemoteURL = "https://github.com/foreign/payload.git"
		}
		hosted := false
		cli := RunnerCLI{Factory: func(_ context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
			*options.Config.Controller = "https://foreign.invalid"
			options.Config.Repos[0] = "github.com/foreign/*"
			options.Loop.Kinds[0] = capability.UnitPlan
			config := cliRuntime(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				return controllerwire.ClaimResponse{Claim: &unit}, nil
			}, func(context.Context, controllerwire.ClaimedUnit) error { hosted = true; return nil }, cliVersions{index: cliVersionIndex(t)})
			config.Repositories, _ = sourceurl.ParseRepoAllowlist([]string{"github.com/foreign/*"})
			config.Controller.URL = "https://foreign.invalid"
			config.Controller.Bearer = func(context.Context) (string, error) { return "foreign", nil }
			open := config.NewController
			config.NewController = func(options client.EngineOptions) (controllerwire.Controller, error) {
				bearer, err := options.Bearer(t.Context())
				if err != nil || bearer != "private-runner-credential" || options.URL != "https://controller.example" || options.Launcher {
					t.Fatalf("factory replaced claimant transport: %+v %v", options, err)
				}
				return open(options)
			}
			return config, nil
		}}
		err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--credentials-dir=" + runnerCredentials(t), "--repos=github.com/acme/*", "--max-claims-before-restart=1"}, "v1.2.3")
		identity, identityErr := sourceurl.Identity(unit.Request.Request.Source.Location.RemoteURL)
		allowed := !acceptedForeign && identityErr == nil && strings.HasPrefix(identity, "github.com/acme/") && strings.Count(identity, "/") == 2
		if hosted != allowed || (err == nil) != allowed || !allowed && !errors.Is(err, runners.ErrRepoNotAllowed) {
			t.Fatalf("allowed=%v hosted=%v error=%v", allowed, hosted, err)
		}
	})
}
