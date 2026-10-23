//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestOwnerHostReadsAcquiredProjectForSharedPlanning(t *testing.T) {
	f, host, input := newOwnerFixture(t, false)
	planner, process, _, _ := sdkPlanningFixture(t)
	root, sha := hostSourceCheckout(t)
	project := []byte("defaults:\n  args: {from: checkout}\n  guards: {when: [arg:from=checkout]}\n")
	if err := os.WriteFile(filepath.Join(root, "sparkwing.yaml"), project, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "gh:123", LocalCheckout: root, PipelineDir: ".", SHA: sha}, source.Options{Base: procenv.Base{Local: true, Captured: []string{"PATH=" + os.Getenv("PATH")}}, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	entry.Engine = process.Entry.Engine
	f.snapshot.Run.Request.RunID = "run"
	f.snapshot.Run.Request.Request.ExecutionLocation = "local"
	input.SealedRequest, err = json.Marshal(f.snapshot.Run.Request.Request)
	if err != nil {
		t.Fatal(err)
	}
	input.GrantedRunner = process.Runner
	host.Prepare, host.Planner = nil, &planner
	host.Planning = PlanningInput{Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal}}
	host.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return entry, nil }
	if _, err := host.Run(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if f.accepted.Seal.Resolved.Args["from"] != "checkout" || !f.accepted.Seal.Resolved.Guards.Passed || f.accepted.Seal.Declarations.File != "sparkwing.yaml" {
		t.Fatal("default owner planner omitted acquired project inputs")
	}
}

func FuzzCheckoutProjectBoundary(f *testing.F) {
	for _, path := range []string{".", "linked", "linked/..", "../outside", "../outside/nested", "missing/../../outside", "linked//.", "linked/sparkwing.yaml/.."} {
		f.Add(path)
	}
	f.Fuzz(func(t *testing.T, relative string) {
		parent := t.TempDir()
		root, outside := filepath.Join(parent, "checkout"), filepath.Join(parent, "outside")
		for _, dir := range []string{root, outside} {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "sparkwing.yaml"), []byte("inside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "sparkwing.yaml"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
		raw, _, err := readCheckoutProject(t.Context(), root, filepath.Join(root, relative))
		if len(raw) > 0 && string(raw) != "inside" || err != nil && len(raw) != 0 {
			t.Fatal("project discovery returned bytes outside its acquired root")
		}
	})
}
