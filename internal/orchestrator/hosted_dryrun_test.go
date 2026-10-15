//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const dryRunPipeline = "hosted-dryrun-fixture"

type dryRunFixture struct{ sparkwing.Base }

func (dryRunFixture) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	marker := os.Getenv("HOSTED_DRYRUN_MARKER")
	if err := os.WriteFile(marker+".plan", []byte(os.Getenv("SPARKWING_DRY_RUN")), 0o600); err != nil {
		return err
	}
	sparkwing.Job(plan, "body", dryRunBody{marker: marker})
	return nil
}

type dryRunBody struct {
	sparkwing.Base
	marker string
}

func (b dryRunBody) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	record := func(how string) func(context.Context) error {
		return func(ctx context.Context) error {
			return os.WriteFile(b.marker+".node", fmt.Appendf(nil, "%s %s", how, os.Getenv("SPARKWING_DRY_RUN")), 0o600)
		}
	}
	return sparkwing.Step(w, "deploy", record("real")).DryRun(record("dry")), nil
}

func init() {
	if os.Getenv(nodeProtocolEntryEnv) == "1" {
		sparkwing.Register(dryRunPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return dryRunFixture{} })
	}
}

func runDryRunFixture(t *testing.T, opts Options) (plan, node string) {
	t.Helper()
	plan, node, _, _ = runDryRunFixtureIn(t, t.TempDir(), opts, nil)
	return plan, node
}

func runDryRunFixtureIn(t *testing.T, dir string, opts Options, flags map[string]any) (plan, node string, st *store.Store, runID string) {
	t.Helper()
	paths := PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	marker := filepath.Join(dir, "marker")
	opts.RunID, opts.Pipeline, opts.State, opts.Delegate = "dry", dryRunPipeline, st, &storedLog{}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := RunHosted(ctx, paths, opts, HostedEntry{
		Command: pipelineentry.Binary(os.Args[0]), Dir: dir,
		Env:   append(os.Environ(), nodeProtocolEntryEnv+"=1", "HOSTED_DRYRUN_MARKER="+marker, "GORACE=atexit_sleep_ms=0"),
		Flags: flags,
	})
	if err != nil || out.Status != "success" {
		t.Fatalf("run = %+v, %v", out, err)
	}
	read := func(suffix string) string {
		data, err := os.ReadFile(marker + suffix)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	return read(".plan"), read(".node"), st, out.RunID
}

func TestHostedDryRunReachesThePlanAndEveryNode(t *testing.T) {
	t.Setenv("SPARKWING_DRY_RUN", "")
	plan, node := runDryRunFixture(t, Options{DryRun: true})
	if plan != "true" || node != "dry true" {
		t.Errorf("plan saw SPARKWING_DRY_RUN=%q, node %q; plan code must see what its nodes see or they plan differently", plan, node)
	}
}

func TestHostedRunInsideADryRunNodeIsADryRun(t *testing.T) {
	t.Setenv("SPARKWING_DRY_RUN", "true")
	if _, node := runDryRunFixture(t, Options{}); node != "dry true" {
		t.Errorf("a run started inside a dry-run node saw %q; its steps ran for real", node)
	}
}

func TestHostedRunRecordsHandleReproducerAndRequires(t *testing.T) {
	t.Setenv("SPARKWING_DRY_RUN", "")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "defaults:\n  requires: [linux]\npipelines:\n  - name: " + dryRunPipeline + "\n    entrypoint: Fixture\n"
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(dir, "handle.json")
	_, _, st, runID := runDryRunFixtureIn(t, dir, Options{RunHandlePath: handle, DryRun: true},
		map[string]any{"ref": "refs/tags/probe", "allow": "destructive,prod", "only": "body"})
	data, err := os.ReadFile(handle)
	if err != nil || !strings.Contains(string(data), runID) {
		t.Fatalf("handle %s (%v) does not name run %s", data, err, runID)
	}
	row, err := st.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := row.Invocation["reproducer"].(string)
	for _, flag := range []string{"--sw-ref=refs/tags/probe", "--sw-allow=destructive,prod", "--sw-only=body", "--sw-dry-run"} {
		if !strings.Contains(line, flag) {
			t.Errorf("reproducer %q omits %s", line, flag)
		}
	}
	var doc nodeproto.PlanDoc
	if err := json.Unmarshal(row.PlanSnapshot, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Jobs) != 1 || !slices.Equal(doc.Jobs[0].Requires, []string{"linux"}) {
		t.Errorf("accepted plan requires %v, want the project default [linux]", doc.Jobs)
	}
}
