package orchestrator_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// End-to-end check that a job calling sparkwing.Secret resolves
// through the per-run resolver wired from
// Options.SecretSource. Uses a dotenv source seeded in TempDir so the
// test never touches the user's real ~/.config/sparkwing.

type secretReaderJob struct{ sparkwing.Base }

var observedToken string

func (j *secretReaderJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run)
	return nil, nil
}

func (secretReaderJob) run(ctx context.Context) error {
	v, err := sparkwing.Secret(ctx, "TOKEN")
	if err != nil {
		return err
	}
	observedToken = v
	return nil
}

type secretReaderPipe struct{ sparkwing.Base }

func (secretReaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "read", &secretReaderJob{})
	return nil
}

func init() {
	register("secret-reader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &secretReaderPipe{} })
}

func TestSecret_ResolvesFromDotenvSource(t *testing.T) {
	dir := t.TempDir()
	dotenvPath := filepath.Join(dir, "secrets.env")
	if err := secrets.WriteDotenvEntry(dotenvPath, "TOKEN", "abc123"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	observedToken = ""
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "secret-reader",
		SecretSource: secrets.NewDotenvSource(dotenvPath),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (err=%v)", res.Status, res.Error)
	}
	if observedToken != "abc123" {
		t.Fatalf("job saw TOKEN = %q, want abc123", observedToken)
	}
}

func TestSecret_MissingNameFailsTheJob(t *testing.T) {
	dir := t.TempDir()
	dotenvPath := filepath.Join(dir, "secrets.env")
	// Empty file: TOKEN won't resolve.
	if err := secrets.WriteDotenvEntry(dotenvPath, "OTHER", "1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	observedToken = "before"
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "secret-reader",
		SecretSource: secrets.NewDotenvSource(dotenvPath),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed (TOKEN should be missing)", res.Status)
	}
	if res.Error == nil {
		t.Fatal("expected non-nil run error")
	}
	// Per-node error carries the verbatim message; the run-level
	// error just lists which nodes failed.
	st, _ := store.Open(p.StateDB())
	defer st.Close()
	node, gerr := st.GetNode(context.Background(), res.RunID, "read")
	if gerr != nil {
		t.Fatalf("GetNode: %v", gerr)
	}
	if !strings.Contains(node.Error, "secret not found") {
		t.Fatalf("node error = %q, want one mentioning the missing secret", node.Error)
	}
	if observedToken != "before" {
		t.Fatalf("job mutated observedToken to %q despite Secret error", observedToken)
	}
}

// captureLogger collects every emitted record for inspection.
type captureLogger struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
}

func (c *captureLogger) Log(level, msg string) {
	c.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}
func (c *captureLogger) Emit(rec sparkwing.LogRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, rec)
}
func (c *captureLogger) Snapshot() []sparkwing.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sparkwing.LogRecord, len(c.records))
	copy(out, c.records)
	return out
}

type secretLeakerJob struct{ sparkwing.Base }

func (j *secretLeakerJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run)
	return nil, nil
}

func (secretLeakerJob) run(ctx context.Context) error {
	v, err := sparkwing.Secret(ctx, "TOKEN")
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "deploying with token=%s now", v)
	return nil
}

type secretLeakerPipe struct{ sparkwing.Base }

func (secretLeakerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "leak", &secretLeakerJob{})
	return nil
}

func init() {
	register("secret-leaker", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &secretLeakerPipe{} })
}

// TestSecret_MaskerRedactsResolvedValues verifies the per-run masker
// hooks the delegate logger so a job that accidentally logs a secret
// emits the redaction marker instead of the raw value.
func TestSecret_MaskerRedactsResolvedValues(t *testing.T) {
	dir := t.TempDir()
	dotenvPath := filepath.Join(dir, "secrets.env")
	if err := secrets.WriteDotenvEntry(dotenvPath, "TOKEN", "supersecret"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cap := &captureLogger{}
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "secret-leaker",
		SecretSource: secrets.NewDotenvSource(dotenvPath),
		Delegate:     cap,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	for _, rec := range cap.Snapshot() {
		if strings.Contains(rec.Msg, "supersecret") {
			t.Fatalf("delegate received a record with the raw secret value: %+v", rec)
		}
	}
	// Sanity: the redaction marker is present on the deploy log line.
	var sawRedacted bool
	for _, rec := range cap.Snapshot() {
		if strings.Contains(rec.Msg, "deploying with token=***") {
			sawRedacted = true
		}
	}
	if !sawRedacted {
		t.Fatal("expected a delegate record containing the masked deploy line")
	}
}
