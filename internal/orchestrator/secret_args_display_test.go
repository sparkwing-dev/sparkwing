package orchestrator_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	secretArgValue    = "s3cr3t-token-value"
	visibleArgValue   = "prod"
	secretArgPipeline = "secret-args-display"
)

type secretArgsInputs struct {
	Token string `flag:"token" secret:"true"`
	Env   string `flag:"env"`
}

type secretArgsPipe struct{ sparkwing.Base }

func (secretArgsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ secretArgsInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "noop", &secretArgsJob{})
	return nil
}

type secretArgsJob struct{ sparkwing.Base }

func (j *secretArgsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(context.Context) error { return nil })
	return nil, nil
}

var secretArgsOnce sync.Once

func registerSecretArgsPipeline() {
	secretArgsOnce.Do(func() {
		sparkwing.Register[secretArgsInputs](secretArgPipeline,
			func() sparkwing.Pipeline[secretArgsInputs] { return &secretArgsPipe{} })
	})
}

func runWithSecretArg(t *testing.T) (orchestrator.Paths, string) {
	t.Helper()
	registerSecretArgsPipeline()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: secretArgPipeline,
		Args:     map[string]string{"token": secretArgValue, "env": visibleArgValue},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	return p, res.RunID
}

func assertRedacted(t *testing.T, surface, out string) {
	t.Helper()
	if strings.Contains(out, secretArgValue) {
		t.Errorf("%s leaked the secret arg value:\n%s", surface, out)
	}
	if !strings.Contains(out, store.RedactedArgValue) {
		t.Errorf("%s carries no %s marker:\n%s", surface, store.RedactedArgValue, out)
	}
	if !strings.Contains(out, visibleArgValue) {
		t.Errorf("%s redacted the non-secret arg too:\n%s", surface, out)
	}
}

func TestSecretArgs_RedactedInRunsListJSON(t *testing.T) {
	p, runID := runWithSecretArg(t)
	var buf bytes.Buffer
	if err := orchestrator.ListJobs(context.Background(), p,
		orchestrator.ListOpts{Limit: 10, JSON: true}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(buf.String(), runID) {
		t.Fatalf("run %s missing from list: %s", runID, buf.String())
	}
	assertRedacted(t, "runs list -o json", buf.String())
}

func TestSecretArgs_RedactedInRunsStatusJSON(t *testing.T) {
	p, runID := runWithSecretArg(t)
	var buf bytes.Buffer
	if err := orchestrator.JobStatus(context.Background(), p, runID,
		orchestrator.StatusOpts{JSON: true}, &buf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	assertRedacted(t, "runs status -o json", buf.String())
}

func TestSecretArgs_RedactedInRunsGetJSON(t *testing.T) {
	p, runID := runWithSecretArg(t)
	var buf bytes.Buffer
	if err := orchestrator.GetRunJSONLocal(context.Background(), p, runID, &buf); err != nil {
		t.Fatalf("GetRunJSONLocal: %v", err)
	}
	assertRedacted(t, "runs get", buf.String())
}

func TestSecretArgs_AbsentFromRunsStatusText(t *testing.T) {
	p, runID := runWithSecretArg(t)
	var buf bytes.Buffer
	if err := orchestrator.JobStatus(context.Background(), p, runID,
		orchestrator.StatusOpts{}, &buf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if strings.Contains(buf.String(), secretArgValue) {
		t.Errorf("runs status text leaked the secret arg:\n%s", buf.String())
	}
}

func TestSecretArgs_RedactedInRunStartEnvelope(t *testing.T) {
	p, runID := runWithSecretArg(t)
	raw, err := os.ReadFile(p.EnvelopeLog(runID))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "run_start") {
		t.Fatalf("no run_start record in envelope:\n%s", body)
	}
	if strings.Contains(body, secretArgValue) {
		t.Errorf("run_start envelope leaked the secret arg:\n%s", body)
	}
	if !strings.Contains(body, store.RedactedArgValue) {
		t.Errorf("run_start envelope carries no redaction marker:\n%s", body)
	}
	if !strings.Contains(body, visibleArgValue) {
		t.Errorf("run_start envelope lost the non-secret arg:\n%s", body)
	}
	if strings.Contains(body, "inputs_hash") {
		t.Errorf("run_start envelope exposes an input-hash oracle:\n%s", body)
	}
}

func TestSecretArgs_StoredRowKeepsPlaintextForReExecution(t *testing.T) {
	p, runID := runWithSecretArg(t)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Args["token"] != secretArgValue {
		t.Fatalf("stored Args[token] = %q, want plaintext %q -- retry and the masker read this",
			run.Args["token"], secretArgValue)
	}
	invArgs, _ := run.Invocation["args"].(map[string]any)
	if invArgs["token"] != secretArgValue {
		t.Errorf("stored invocation.args[token] = %v, want plaintext", invArgs["token"])
	}
	if got := run.SecretArgNames(); len(got) != 1 || got[0] != "token" {
		t.Errorf("stored classification = %v, want [token]", got)
	}
	if _, ok := run.Invocation["inputs_hash"]; ok {
		t.Errorf("stored invocation exposes an input-hash oracle: %v", run.Invocation)
	}

	if run.RedactedForDisplay().Args["token"] != store.RedactedArgValue {
		t.Error("stored row does not redact through RedactedForDisplay")
	}
}

func TestSecretArgs_StateDumpOmitsInputHash(t *testing.T) {
	registerSecretArgsPipeline()
	artifacts, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:      secretArgPipeline,
		Args:          map[string]string{"token": secretArgValue, "env": visibleArgValue},
		ArtifactStore: artifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := artifacts.Get(context.Background(), "runs/"+res.RunID+"/state.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "inputs_hash") {
		t.Errorf("state.ndjson exposes an input-hash oracle:\n%s", body)
	}
}

func TestSecretArgs_StoredRowStillSeedsTheLogMasker(t *testing.T) {
	p, runID := runWithSecretArg(t)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	reg, ok := sparkwing.Lookup(secretArgPipeline)
	if !ok {
		t.Fatal("fixture pipeline not registered")
	}
	values := reg.SecretValues(run.Args)
	if len(values) != 1 || values[0] != secretArgValue {
		t.Fatalf("masker seed from the stored row = %v, want [%s]", values, secretArgValue)
	}
}

func TestSecretArgs_PipelineWithoutSecretsIsUnchanged(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Invocation[store.InvocationSecretArgsKey]; ok {
		t.Errorf("secret-free pipeline wrote a classification key: %v", run.Invocation)
	}
}

func TestSecretArgs_UnsuppliedSecretKeepsInputHash(t *testing.T) {
	registerSecretArgsPipeline()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: secretArgPipeline,
		Args:     map[string]string{"env": visibleArgValue},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Invocation["inputs_hash"]; !ok {
		t.Errorf("optional secret was not supplied, but inputs_hash was omitted: %v", run.Invocation)
	}
}
