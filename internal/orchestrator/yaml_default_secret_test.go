package orchestrator_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A secret input whose value never appears on the command line: it is
// supplied by sparkwing.yaml, either from the project's `defaults.args`
// block (Options.DefaultArgs) or the pipeline entry's `args:` block
// (Options.PipelineYAML.Args). Both layers reach reg.Invoke through the
// orchestrator's arg merge, so both have to reach the run's masker.
const (
	yamlDefaultSecretValue  = "yaml-defaults-supersecret"
	yamlPipelineSecretValue = "yaml-pipeline-args-supersecret"
	yamlVisibleArgValue     = "staging"
	yamlSecretPipelineName  = "yaml-default-secret"
	yamlSecretNodeID        = "leak"
)

type yamlSecretInputs struct {
	Token string `flag:"token" desc:"deploy token" secret:"true"`
	Env   string `flag:"env" desc:"target environment"`
}

// observedYAMLToken is what the job body actually received. Masking must
// not reach execution: the pipeline still has to run with the real value.
var observedYAMLToken string

type yamlSecretPipe struct{ sparkwing.Base }

func (yamlSecretPipe) Plan(_ context.Context, plan *sparkwing.Plan, in yamlSecretInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, yamlSecretNodeID, func(ctx context.Context) error {
		observedYAMLToken = in.Token
		sparkwing.Info(ctx, "deploying token=%s to env=%s now", in.Token, in.Env)
		return nil
	})
	return nil
}

var yamlSecretOnce sync.Once

func registerYAMLSecretPipeline() {
	yamlSecretOnce.Do(func() {
		sparkwing.Register[yamlSecretInputs](yamlSecretPipelineName,
			func() sparkwing.Pipeline[yamlSecretInputs] { return yamlSecretPipe{} })
	})
}

// runYAMLSecret executes the fixture and returns the paths, the run id,
// and the records the delegate saw.
func runYAMLSecret(t *testing.T, opts orchestrator.Options) (orchestrator.Paths, string, *captureLogger) {
	t.Helper()
	registerYAMLSecretPipeline()
	observedYAMLToken = ""
	p := newPaths(t)
	cap := &captureLogger{}
	opts.Pipeline = yamlSecretPipelineName
	opts.Delegate = cap
	res, err := orchestrator.RunLocal(context.Background(), p, opts)
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (err=%v)", res.Status, res.Error)
	}
	return p, res.RunID, cap
}

// assertYAMLSecretMasked is the whole guarantee in one place: the job ran
// with the real value, and no surface the run wrote holds it.
func assertYAMLSecretMasked(t *testing.T, p orchestrator.Paths, runID string, cap *captureLogger, secret string) {
	t.Helper()

	if observedYAMLToken != secret {
		t.Fatalf("job received token %q, want the real %q -- masking must not reach execution",
			observedYAMLToken, secret)
	}

	body, err := os.ReadFile(p.NodeLog(runID, yamlSecretNodeID))
	if err != nil {
		t.Fatalf("read node log: %v", err)
	}
	if strings.Contains(string(body), secret) {
		t.Errorf("persisted node log leaks the yaml-supplied secret:\n%s", body)
	}
	if !strings.Contains(string(body), "token=*** to") {
		t.Errorf("persisted node log missing the masked line:\n%s", body)
	}
	if !strings.Contains(string(body), "env="+yamlVisibleArgValue) {
		t.Errorf("persisted node log lost the non-secret arg:\n%s", body)
	}

	for _, rec := range cap.Snapshot() {
		if strings.Contains(rec.Msg, secret) {
			t.Errorf("delegate received a record with the raw secret: %+v", rec)
		}
	}
}

// The project-level `defaults.args` block. This is the reported hole: the
// masker was seeded from the caller's args, which never carry this value.
func TestYAMLDefaultSecret_ProjectDefaultsAreMasked(t *testing.T) {
	p, runID, cap := runYAMLSecret(t, orchestrator.Options{
		Args:        map[string]string{"env": yamlVisibleArgValue},
		DefaultArgs: map[string]string{"token": yamlDefaultSecretValue},
	})
	assertYAMLSecretMasked(t, p, runID, cap, yamlDefaultSecretValue)
}

// The per-pipeline `args:` block, the other yaml layer the merge folds in
// below the CLI flag.
func TestYAMLDefaultSecret_PipelineArgsAreMasked(t *testing.T) {
	p, runID, cap := runYAMLSecret(t, orchestrator.Options{
		Args: map[string]string{"env": yamlVisibleArgValue},
		PipelineYAML: &pipelines.Pipeline{
			Name: yamlSecretPipelineName,
			Args: map[string]string{"token": yamlPipelineSecretValue},
		},
	})
	assertYAMLSecretMasked(t, p, runID, cap, yamlPipelineSecretValue)
}

// An explicit CLI flag outranks both yaml layers, so the value that must
// be masked is the flag's -- and the yaml value the run never used must
// not be dragged into the redaction set.
func TestYAMLDefaultSecret_CLIFlagOutranksYAMLAndIsMasked(t *testing.T) {
	const flagSecret = "cli-flag-supersecret"
	p, runID, cap := runYAMLSecret(t, orchestrator.Options{
		Args:        map[string]string{"token": flagSecret, "env": yamlVisibleArgValue},
		DefaultArgs: map[string]string{"token": yamlDefaultSecretValue},
	})
	assertYAMLSecretMasked(t, p, runID, cap, flagSecret)
}

// The display half. A yaml-supplied secret is not recorded on the run row
// at all -- the row keeps the caller's args, which is what the reproducer
// replays -- so the display surfaces cannot leak it. The classification is
// still written, because it comes from the registration's declared names
// rather than from the values, which is what keeps a *flag*-supplied
// secret on the same row redacted.
func TestYAMLDefaultSecret_DisplaySurfacesCarryNoPlaintext(t *testing.T) {
	p, runID, _ := runYAMLSecret(t, orchestrator.Options{
		Args:        map[string]string{"env": yamlVisibleArgValue},
		DefaultArgs: map[string]string{"token": yamlDefaultSecretValue},
	})
	ctx := context.Background()

	var list bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p,
		orchestrator.ListOpts{Limit: 10, JSON: true}, &list); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(list.String(), runID) {
		t.Fatalf("run %s missing from list: %s", runID, list.String())
	}
	if strings.Contains(list.String(), yamlDefaultSecretValue) {
		t.Errorf("runs list -o json leaks the yaml-supplied secret:\n%s", list.String())
	}

	var status bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, runID,
		orchestrator.StatusOpts{JSON: true}, &status); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if strings.Contains(status.String(), yamlDefaultSecretValue) {
		t.Errorf("runs status -o json leaks the yaml-supplied secret:\n%s", status.String())
	}

	env, err := os.ReadFile(p.EnvelopeLog(runID))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var runStart string
	for _, line := range strings.Split(string(env), "\n") {
		if strings.Contains(line, `"event":"run_start"`) {
			runStart = line
		}
	}
	if runStart == "" {
		t.Fatalf("no run_start record in envelope:\n%s", env)
	}
	if strings.Contains(runStart, yamlDefaultSecretValue) {
		t.Errorf("run_start attrs leak the yaml-supplied secret:\n%s", runStart)
	}
	// The envelope is one file: a masked run_start next to an unmasked
	// node log line is still a leaked secret on disk.
	if strings.Contains(string(env), yamlDefaultSecretValue) {
		t.Errorf("run envelope leaks the yaml-supplied secret:\n%s", env)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if names := run.SecretArgNames(); len(names) != 1 || names[0] != "token" {
		t.Errorf("stored classification = %v, want [token] regardless of where the value came from", names)
	}
	if _, ok := run.Args["token"]; ok {
		t.Errorf("run row recorded a yaml-supplied arg it was not given: %v", run.Args)
	}
}

// Retry rehydrates a run's args from the stored row and re-merges the
// yaml layers from the checkout it runs in. The rehydrated args carry no
// token, so the value arrives from sparkwing.yaml again -- the retry must
// execute with the real secret and mask it just like the first attempt.
func TestYAMLDefaultSecret_RetryRehydrationExecutesAndMasks(t *testing.T) {
	defaults := map[string]string{"token": yamlDefaultSecretValue}
	p, runID, _ := runYAMLSecret(t, orchestrator.Options{
		Args:        map[string]string{"env": yamlVisibleArgValue},
		DefaultArgs: defaults,
	})

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	_ = st.Close()

	retryPaths, retryID, retryCap := runYAMLSecret(t, orchestrator.Options{
		Args:        run.Args,
		DefaultArgs: defaults,
		RetryOf:     runID,
	})
	assertYAMLSecretMasked(t, retryPaths, retryID, retryCap, yamlDefaultSecretValue)
}
