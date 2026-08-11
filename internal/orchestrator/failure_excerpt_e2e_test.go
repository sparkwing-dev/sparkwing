package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const excerptSecretValue = "excerpt-deploy-token"

// excerptBuildJob fails the way a real build step does: an ExecError
// whose message embeds the command's whole stderr, here long enough to
// need bounding and carrying a secret the step had resolved.
type excerptBuildJob struct{ sparkwing.Base }

func (j *excerptBuildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "compile", j.run)
	return nil, nil
}

func (excerptBuildJob) run(ctx context.Context) error {
	token, err := sparkwing.Secret(ctx, "DEPLOY_TOKEN")
	if err != nil {
		return err
	}
	var b strings.Builder
	for i := range 400 {
		fmt.Fprintf(&b, "pkg/thing/file_%03d.go:12: undefined: Helper\n", i)
	}
	fmt.Fprintf(&b, "auth token %s rejected\n", token)
	b.WriteString("FAIL\texit status 2")
	return &sparkwing.ExecError{Command: "go build ./...", Stderr: b.String(), ExitCode: 2}
}

type excerptNoopJob struct{ sparkwing.Base }

func (j *excerptNoopJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(context.Context) error { return nil })
	return nil, nil
}

type excerptPipe struct{ sparkwing.Base }

func (excerptPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &excerptBuildJob{})
	sparkwing.Job(plan, "deploy", &excerptNoopJob{}).Needs(build)
	return nil
}

func init() {
	register("excerpt-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &excerptPipe{} })
}

// TestRun_FailedNodeRecordsBoundedMaskedExcerpt is the end-to-end shape
// of the change: what a failing node persists is a bounded, masked tail
// of the command output plus a pointer to the full log -- not the raw
// unbounded stderr. The cancelled downstream node is left alone: it has
// no output of its own, and going to the logs for it is exactly what
// this must not do.
func TestRun_FailedNodeRecordsBoundedMaskedExcerpt(t *testing.T) {
	p := newPaths(t)
	dotenv := t.TempDir() + "/secrets.env"
	if err := secrets.WriteDotenvEntry(dotenv, "DEPLOY_TOKEN", excerptSecretValue); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "excerpt-fail",
		SecretSource: secrets.NewDotenvSource(dotenv),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	build, err := st.GetNode(ctx, res.RunID, "build")
	if err != nil {
		t.Fatalf("GetNode build: %v", err)
	}
	if len(build.Error) > 8192 {
		t.Fatalf("persisted node error is %d bytes; the excerpt is not bounded", len(build.Error))
	}
	if !strings.Contains(build.Error, "FAIL\texit status 2") {
		t.Fatalf("persisted error dropped the conclusion of the output:\n%s", build.Error)
	}
	if strings.Contains(build.Error, "file_000.go") {
		t.Fatalf("persisted error kept the head of the output:\n%s", build.Error)
	}
	if strings.Contains(build.Error, excerptSecretValue) {
		t.Fatalf("persisted error leaked the secret value:\n%s", build.Error)
	}
	if !strings.Contains(build.Error, "auth token *** rejected") {
		t.Fatalf("persisted error missing the redacted line:\n%s", build.Error)
	}
	wantPointer := "sparkwing runs logs --run " + res.RunID + " --node build"
	if !strings.Contains(build.Error, wantPointer) {
		t.Fatalf("persisted error missing %q:\n%s", wantPointer, build.Error)
	}

	// The node log holds the full output, and every record in it --
	// including the structured attributes of step_end, which carry the
	// failed command's whole error text -- is redacted.
	logBody, err := os.ReadFile(p.NodeLog(res.RunID, "build"))
	if err != nil {
		t.Fatalf("read node log: %v", err)
	}
	if strings.Contains(string(logBody), excerptSecretValue) {
		t.Fatalf("persisted node log leaks the raw secret value")
	}
	var sawStepEndError bool
	for line := range strings.SplitSeq(strings.TrimSpace(string(logBody)), "\n") {
		var rec struct {
			Event string         `json:"event"`
			Attrs map[string]any `json:"attrs"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if rec.Event != "step_end" {
			continue
		}
		msg, _ := rec.Attrs["error"].(string)
		if msg == "" {
			continue
		}
		sawStepEndError = true
		if !strings.Contains(msg, "auth token *** rejected") {
			t.Fatalf("step_end attrs.error is not redacted:\n%s", msg)
		}
	}
	if !sawStepEndError {
		t.Fatal("expected a step_end record carrying the step's error in attrs")
	}

	// `runs errors -o json`: the same excerpt as structured data, so a
	// consumer never has to parse the error string.
	var errBuf bytes.Buffer
	if err := orchestrator.JobErrors(ctx, p, res.RunID, true, &errBuf); err != nil {
		t.Fatalf("JobErrors: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(errBuf.Bytes(), &rows); err != nil {
		t.Fatalf("decode runs errors json: %v\n%s", err, errBuf.String())
	}
	if len(rows) != 1 || rows[0]["node"] != "build" {
		t.Fatalf("runs errors should list the one owning failure, got: %s", errBuf.String())
	}
	excerpt, _ := rows[0]["log_excerpt"].(string)
	if excerpt == "" {
		t.Fatalf("log_excerpt missing:\n%s", errBuf.String())
	}
	if rows[0]["log_excerpt_truncated"] != true {
		t.Fatalf("log_excerpt_truncated = %#v, want true", rows[0]["log_excerpt_truncated"])
	}
	if strings.Contains(excerpt, excerptSecretValue) {
		t.Fatalf("log_excerpt leaked the secret value:\n%s", excerpt)
	}
	if strings.Contains(excerpt, "earlier output omitted") || strings.Contains(excerpt, "command failed") {
		t.Fatalf("log_excerpt should be raw output, not decorated text:\n%s", excerpt)
	}
	if !strings.HasSuffix(excerpt, "FAIL\texit status 2") {
		t.Fatalf("log_excerpt should end at the command's conclusion:\n%s", excerpt)
	}
	if n := strings.Count(excerpt, "\n") + 1; n > 20 {
		t.Fatalf("log_excerpt is %d lines, want at most 20", n)
	}

	// `runs status -o json` carries the same pair per node, and only
	// for the node that owns the failure.
	var statusBuf bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, res.RunID,
		orchestrator.StatusOpts{JSON: true}, &statusBuf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	var detail struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(statusBuf.Bytes(), &detail); err != nil {
		t.Fatalf("decode runs status json: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, n := range detail.Nodes {
		id, _ := n["id"].(string)
		byID[id] = n
	}
	if got, _ := byID["build"]["log_excerpt"].(string); got != excerpt {
		t.Fatalf("runs status log_excerpt differs from runs errors:\n%q\n%q", got, excerpt)
	}
	if byID["build"]["log_excerpt_truncated"] != true {
		t.Fatalf("runs status log_excerpt_truncated = %#v", byID["build"]["log_excerpt_truncated"])
	}
	if _, present := byID["deploy"]["log_excerpt"]; present {
		t.Fatalf("cancelled node must carry no excerpt: %#v", byID["deploy"])
	}
	if _, present := byID["deploy"]["log_excerpt_truncated"]; present {
		t.Fatalf("cancelled node must carry no truncation flag: %#v", byID["deploy"])
	}

	deploy, err := st.GetNode(ctx, res.RunID, "deploy")
	if err != nil {
		t.Fatalf("GetNode deploy: %v", err)
	}
	if deploy.Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("deploy outcome = %q, want cancelled", deploy.Outcome)
	}
	if deploy.Error != "upstream-failed" {
		t.Fatalf("cancelled node error = %q, want the untouched cascade reason", deploy.Error)
	}
}
