package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestCapturedHostEnvironment(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	snapshots, err := NewFileEnvironmentSnapshots(directory)
	if err != nil {
		t.Fatal(err)
	}
	captured := []string{"SPARKWING_CUSTOM=parent", "EMPTY=", "PATH=/submit/bin"}
	sealed, err := snapshots.Capture(t.Context(), captured)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"EMPTY=", "PATH=/submit/bin", "SPARKWING_CUSTOM=parent"}
	for _, placement := range []string{"plan", "attempt", "eval", "retry_attempt", "retry_eval"} {
		t.Run(placement, func(t *testing.T) {
			var trace []string
			var processes []*scriptedSDK
			input, st, _ := executionHostFixture(t, "success", &trace, &processes)
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.Request.Request.Env = sealed
			if strings.HasPrefix(placement, "retry_") {
				spec.Request.RunID = "source-run"
			}
			input.Accepted.Spec, _ = json.Marshal(spec)
			input.Entry = &source.Entry{Command: []string{"unreachable-sdk"}}
			refused := errors.New("stop after environment verification")
			builds, revocations := 0, 0
			cfg := ProtocolSessionConfig{Revoke: func() error { revocations++; return nil }, Environments: SDKEnvironment{
				Snapshots: snapshots, Store: st,
				Base: procenv.Base{Local: true, Captured: []string{"SPARKWING_CUSTOM=changed"}, Ambient: []string{"EMPTY=daemon"}},
				Build: func(_ procenv.Kind, base procenv.Base, runtime procenv.Runtime) (procenv.Env, error) {
					builds++
					if !reflect.DeepEqual(base.Captured, want) || runtime.RunID != input.Accepted.RunID {
						t.Fatalf("captured=%v runtime=%+v", base.Captured, runtime)
					}
					return procenv.Env{}, refused
				},
			}}
			var process ExecutorProcess
			switch strings.TrimPrefix(placement, "retry_") {
			case "plan":
				process, err = cfg.StartPlanning(t.Context(), PlanningProcessInput{Entry: input.Entry, Environment: sealed, Run: nodeproto.RunContext{RunID: input.Accepted.RunID, Pipeline: "build"}}, func(context.Context, nodeproto.FrameLog) error { return nil })
			case "attempt":
				process, err = cfg.Start(t.Context(), input, ExecutorActivity{})
			case "eval":
				process, err = cfg.StartEvaluation(t.Context(), EvaluationInput{Entry: input.Entry, Accepted: input.Accepted, NodeID: input.NodeID, Generation: int64(input.Generation), SpecHash: input.SpecHash})
			}
			if process != nil || !errors.Is(err, refused) || builds != 1 || revocations != 1 {
				t.Fatalf("process=%v builds=%d revocations=%d error=%v", process, builds, revocations, err)
			}
		})
	}
	t.Run("changed_or_missing_snapshot", func(t *testing.T) {
		path := filepath.Join(directory, string(sealed.Digest)+".json")
		if err := os.WriteFile(path, []byte(`["EMPTY=","PATH=/other/bin","SPARKWING_CUSTOM=parent"]`), 0o600); err != nil {
			t.Fatal(err)
		}
		if env, err := snapshots.Load(t.Context(), sealed); err == nil || env != nil {
			t.Fatalf("changed snapshot: env=%v error=%v", env, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if env, err := snapshots.Load(t.Context(), sealed); err == nil || env != nil {
			t.Fatalf("missing snapshot: env=%v error=%v", env, err)
		}
	})
}
