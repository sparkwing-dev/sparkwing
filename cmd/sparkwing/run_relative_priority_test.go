package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestRunRelativePrioritySealsControllerQueueRank(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: prepares and submits a real pipeline through the local controller")
	}
	connection := productionLocalPolicyClient(t)
	controller, err := client.NewEngineClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, test := range []struct {
		mode string
		want int
	}{{"front", 1}, {"back", -1}} {
		t.Run(test.mode, func(t *testing.T) {
			inv, err := clisurface.Parse("sparkwing", []string{"run", "example", "--sw-detached", "--sw-priority", test.mode})
			if err != nil {
				t.Fatal(err)
			}
			flags, args := runFlagsFromInvocation(inv)
			var out bytes.Buffer
			err = runLocalCommand(t.Context(), "example", flags, args, &out, localRunDependencies{resolve: func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
				runtime, err := resolveLocalRunRuntime(ctx, options)
				runtime.controller, runtime.ensureController = controller, nil
				return runtime, err
			}})
			if err != nil {
				t.Fatal(err)
			}
			var receipt struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			award, err := controller.Claim(t.Context(), controllerwire.ClaimRequest{RunID: receipt.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: installedVersion(), AvailableCores: 1})
			if err != nil {
				t.Fatal(err)
			}
			if award.Claim == nil {
				t.Fatal("run has no owner award")
			}
			priority := award.Claim.Request.Request.Options.Priority
			if !priority.Set || priority.Value != test.want || priority.Origin.Layer != runspec.LayerFlag {
				t.Fatalf("sealed priority=%+v, want %d from flag", priority, test.want)
			}
		})
	}
}
