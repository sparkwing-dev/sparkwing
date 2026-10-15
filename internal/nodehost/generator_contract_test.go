//go:build unix

package nodehost

import (
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestGeneratorFixturesUseValidatedV2Declarations(t *testing.T) {
	for _, fixture := range []string{"genfixture", "genoutputfixture"} {
		t.Run(fixture, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), fixture)
			if out, err := exec.Command("go", "build", "-o", bin, "./testdata/"+fixture).CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			validation := nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}
			session, err := StartSession(t.Context(), SessionConfig{Entry: pipelineentry.Binary(bin), Env: []string{"GENFIXTURE_MODE=nested", "GENFIXTURE_SCENARIO=nested"}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := session.Close(); err != nil {
					t.Error(err)
				}
			})
			var raw json.RawMessage
			if err := ask(t.Context(), session, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "probe", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: "probe"}}, &raw); err != nil {
				t.Fatal(err)
			}
			plan, err := nodeproto.ValidatePlan(raw, validation)
			if err != nil {
				t.Fatalf("fixture plan: %v", err)
			}
			if fixture != "genfixture" {
				return
			}
			validation.Expansion, validation.ExistingJobs = true, plan.Jobs
			for _, source := range []string{"targets", "child"} {
				closure := source + "/generate/0"
				raw, err := evalGenerator(t.Context(), session, closure)
				if err != nil {
					t.Fatal(err)
				}
				writes := &expansionWrites{}
				s := &scheduler{run: &Run{ID: "r", Doc: plan, State: writes, Logger: slog.New(slog.DiscardHandler), validation: validation}, nodes: slices.Clone(validation.ExistingJobs), results: map[string]Result{}, started: map[string]bool{}}
				s.expand(t.Context(), source, Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: closure, raw: raw}}})
				if err := s.group(closure).err; err != nil {
					t.Fatal(err)
				}
				if len(writes.nodes) == 0 {
					t.Fatalf("%s generated no jobs", source)
				}
				if !slices.Equal(writes.nodes[0].Deps, []string{source}) {
					t.Fatalf("generated dependency = %v, want %s", writes.nodes[0].Deps, source)
				}
				validation.ExistingJobs = s.nodes
			}
		})
	}
}
