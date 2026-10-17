package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestDetachedRequestIDSurvivesRealPreparationIntakeAndRetry(t *testing.T) {
	connection := productionLocalPolicyClient(t)
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{ sw.Base }
func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 sw.Job(p, "verify", func(context.Context) error { return nil })
 return nil
}
func main() { sw.Register("check", func() sw.Pipeline[sw.NoInputs] { return pipeline{} }); sw.Main() }
`)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: check\n    entrypoint: check\n", 0o600)
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareNativeLocalRun(t.Context(), home, dir, "check", os.Environ(), snapshots, runFlags{})
	if err != nil {
		t.Fatal(err)
	}
	flags, args := parseRunFlagsArgs([]string{"--sw-detached", "--sw-request-id=trace-42"})
	request, err := prepareLocalRunRequest(t.Context(), prepared, flags, args)
	if err != nil {
		t.Fatal(err)
	}
	checkRequestID(t, request.Request, "trace-42")
	controller, err := client.NewEngineClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	first, err := controller.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	checkRequestID(t, first.Request.Request, "trace-42")
	second, err := controller.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID || second.Replayed {
		t.Fatal("tracing ID deduplicated separate submissions")
	}
	carried, err := runspec.CarryRetry(runspec.RetryInput{Request: first.Request, SourceRunID: first.RunID})
	if err != nil {
		t.Fatal(err)
	}
	checkRequestID(t, carried.Request.Request, "trace-42")
	var output bytes.Buffer
	if err := emitLocalRunReceipt(&output, first, "json"); err != nil {
		t.Fatal(err)
	}
	checkRequestID(t, json.RawMessage(output.Bytes()), "trace-42")
}

func checkRequestID(t *testing.T, value any, want string) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := json.Unmarshal(fields["request_id"], &got); err != nil || got != want {
		t.Fatalf("request_id=%q: %v", got, err)
	}
}
