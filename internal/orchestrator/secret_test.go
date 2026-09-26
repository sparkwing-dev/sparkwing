package orchestrator_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

func seedLocalSecret(t *testing.T, p orchestrator.Paths, name, value string) {
	t.Helper()
	t.Setenv(localsecrets.KeyFileEnv, filepath.Join(t.TempDir(), "secrets.key"))
	t.Setenv(localsecrets.KeyEnv, "")
	ring, err := localsecrets.LoadKeyring(false)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	row := store.Secret{Name: name, Masked: true, Shared: true}
	sealed, err := controller.SealSecretValue(ring.For(st), store.DefaultTeam, &row, value)
	if err != nil {
		t.Fatalf("seal %s: %v", name, err)
	}
	row.Value = sealed
	if err := st.CreateOrReplaceSecret(row, time.Now()); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func TestSecret_ResolvesFromTheLocalStoreWithoutADaemon(t *testing.T) {
	p := newPaths(t)
	seedLocalSecret(t, p, "TOKEN", "abc123")

	observedToken = ""
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "secret-reader",
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
	p := newPaths(t)
	seedLocalSecret(t, p, "OTHER", "1")

	observedToken = "before"
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "secret-reader",
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
	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
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

func TestSecret_MaskerRedactsResolvedValues(t *testing.T) {
	p := newPaths(t)
	seedLocalSecret(t, p, "TOKEN", "supersecret")

	cap := &captureLogger{}
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "secret-leaker",
		Delegate: cap,
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
