package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A node that fails with a plain Go error has no command output to
// excerpt. Absence is the honest report; the error still stands.
type plainFailJob struct{ sparkwing.Base }

func (j *plainFailJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "check", func(context.Context) error {
		return errors.New(`no such cluster "prod-2"`)
	})
	return nil, nil
}

type plainFailPipe struct{ sparkwing.Base }

func (plainFailPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "check", &plainFailJob{})
	return nil
}

// A short command failure fits inside the bound: excerpt present,
// truncation flag false.
type shortFailJob struct{ sparkwing.Base }

func (j *shortFailJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "compile", func(context.Context) error {
		return &sparkwing.ExecError{
			Command:  "go vet ./...",
			Stderr:   "vet: pkg/a.go:3:2: undefined: Foo",
			ExitCode: 1,
		}
	})
	return nil, nil
}

type shortFailPipe struct{ sparkwing.Base }

func (shortFailPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "compile", &shortFailJob{})
	return nil
}

func init() {
	register("excerpt-plain-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &plainFailPipe{} })
	register("excerpt-short-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &shortFailPipe{} })
}

func runsErrorsJSON(t *testing.T, p orchestrator.Paths, runID string) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := orchestrator.JobErrors(context.Background(), p, runID, true, &buf); err != nil {
		t.Fatalf("JobErrors: %v", err)
	}
	return decodeNDJSON[map[string]any](t, buf.String())
}

// No output, no excerpt: the fields are absent rather than empty, so a
// consumer can tell "nothing was captured" from "captured nothing".
func TestJobErrorsJSON_PlainErrorHasNoExcerpt(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "excerpt-plain-fail"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	rows := runsErrorsJSON(t, p, res.RunID)
	if len(rows) != 1 {
		t.Fatalf("want one failed node, got %d", len(rows))
	}
	if !strings.Contains(rows[0]["error"].(string), `no such cluster "prod-2"`) {
		t.Fatalf("error text lost: %#v", rows[0]["error"])
	}
	if _, present := rows[0]["log_excerpt"]; present {
		t.Fatalf("plain error must not carry log_excerpt: %#v", rows[0])
	}
	if _, present := rows[0]["log_excerpt_truncated"]; present {
		t.Fatalf("plain error must not carry log_excerpt_truncated: %#v", rows[0])
	}
}

// Output that fits keeps the excerpt and reports truncated=false --
// the flag has to be usable in both directions.
func TestJobErrorsJSON_ShortOutputNotTruncated(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "excerpt-short-fail"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	rows := runsErrorsJSON(t, p, res.RunID)
	if len(rows) != 1 {
		t.Fatalf("want one failed node, got %d", len(rows))
	}
	if got := rows[0]["log_excerpt"]; got != "vet: pkg/a.go:3:2: undefined: Foo" {
		t.Fatalf("log_excerpt = %#v", got)
	}
	if got, present := rows[0]["log_excerpt_truncated"]; !present || got != false {
		t.Fatalf("log_excerpt_truncated = %#v (present=%v), want false", got, present)
	}
}

// Remote parity: the excerpt rides the run's event stream, which every
// backend serves, so a controller-backed read renders the same pair a
// local read does.
func TestJobsRemoteJSON_CarriesFailureExcerpt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	const (
		runID    = "run-remote-excerpt"
		failedID = "build"
		cascaded = "deploy"
	)
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "p", Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	for _, id := range []string{failedID, cascaded} {
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: id, Status: "pending"}); err != nil {
			t.Fatalf("create node %s: %v", id, err)
		}
	}
	if err := st.FinishNode(ctx, runID, failedID, "failed",
		"build: command failed (exit 2): go build ./...\n… earlier output omitted\nFAIL", nil); err != nil {
		t.Fatalf("finish build: %v", err)
	}
	if err := st.FinishNode(ctx, runID, cascaded, "cancelled", "upstream-failed", nil); err != nil {
		t.Fatalf("finish deploy: %v", err)
	}
	payload := []byte(`{"log_excerpt":"undefined: Helper\nFAIL","log_excerpt_truncated":true}`)
	if _, err := st.AppendEvent(ctx, runID, failedID, "node_failure_excerpt", payload); err != nil {
		t.Fatalf("append excerpt event: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	defer srv.Close()

	var errBuf bytes.Buffer
	if err := orchestrator.JobErrorsRemote(ctx, srv.URL, "", runID, true, &errBuf); err != nil {
		t.Fatalf("JobErrorsRemote: %v", err)
	}
	rows := decodeNDJSON[map[string]any](t, errBuf.String())
	if len(rows) != 1 || rows[0]["node"] != failedID {
		t.Fatalf("remote runs errors: %s", errBuf.String())
	}
	if rows[0]["log_excerpt"] != "undefined: Helper\nFAIL" {
		t.Fatalf("remote log_excerpt = %#v", rows[0]["log_excerpt"])
	}
	if rows[0]["log_excerpt_truncated"] != true {
		t.Fatalf("remote log_excerpt_truncated = %#v", rows[0]["log_excerpt_truncated"])
	}

	var statusBuf bytes.Buffer
	if err := orchestrator.JobStatusRemote(ctx, srv.URL, "", runID,
		orchestrator.StatusOpts{JSON: true}, &statusBuf); err != nil {
		t.Fatalf("JobStatusRemote: %v", err)
	}
	var detail struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(statusBuf.Bytes(), &detail); err != nil {
		t.Fatalf("decode remote runs status: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, n := range detail.Nodes {
		id, _ := n["id"].(string)
		byID[id] = n
	}
	if byID[failedID]["log_excerpt"] != "undefined: Helper\nFAIL" {
		t.Fatalf("remote status log_excerpt = %#v", byID[failedID]["log_excerpt"])
	}
	if _, present := byID[cascaded]["log_excerpt"]; present {
		t.Fatalf("cancelled node carries an excerpt remotely: %#v", byID[cascaded])
	}
}
