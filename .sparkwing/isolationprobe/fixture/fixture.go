// Package fixture is the pipeline the cloud-isolation driver uploads as each staging team: one job
// runs the isolation probe, a job it depends on produces the output it may read, and a sibling job
// produces an output it must not read.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sparkwing-pipelines/isolationprobe"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Pipeline is the name the fixture registers and declares in its sparkwing.yaml.
const Pipeline = "isolation-probe"

// Entrypoint is the registered entrypoint name the fixture's sparkwing.yaml points at.
const Entrypoint = "IsolationProbe"

// AllowedJob is the fixture job whose output the probe job depends on and may read.
const AllowedJob = "output-allowed"

// JobTimeout is the probe job's default time limit, an anchor's hold included.
const JobTimeout = 20 * time.Minute

// MaxJobTimeout caps the time limit a run may give its probe job.
const MaxJobTimeout = time.Hour

// Args are the flags the driver passes to each probe run.
type Args struct {
	Role    string `flag:"role" desc:"probe, or anchor to keep the listener up after probing" default:"probe"`
	Hold    string `flag:"hold" desc:"How long an anchor keeps its listener up after probing, as a Go duration" default:"0s"`
	Timeout string `flag:"timeout" desc:"The probe job's time limit as a Go duration, at most 1h; an anchor's hold must end before it" default:"20m"`
}

// IsolationProbe is the hostile containment probe as a Cloud pipeline.
type IsolationProbe struct{ sparkwing.Base }

// ShortHelp describes the pipeline in one line.
func (IsolationProbe) ShortHelp() string {
	return "Hostile containment probe for one Cloud job; the cloud-isolation pipeline runs it on staging"
}

// Plan declares the probe job, the job whose output it reads as a control and the sibling whose
// output it must not read. Discovery plans with empty arguments, which mean a probe that does not
// hold, limited to JobTimeout.
func (IsolationProbe) Plan(_ context.Context, plan *sparkwing.Plan, in Args, run sparkwing.RunContext) error {
	role, holdText, timeoutText := in.Role, in.Hold, in.Timeout
	if role == "" {
		role = isolationprobe.RoleProbe
	}
	if holdText == "" {
		holdText = "0s"
	}
	if timeoutText == "" {
		timeoutText = JobTimeout.String()
	}
	hold, err := time.ParseDuration(holdText)
	if err != nil {
		return fmt.Errorf("--hold: %w", err)
	}
	timeout, err := time.ParseDuration(timeoutText)
	if err != nil {
		return fmt.Errorf("--timeout: %w", err)
	}
	if role != isolationprobe.RoleProbe && role != isolationprobe.RoleAnchor {
		return fmt.Errorf("--role %q must be %s or %s", role, isolationprobe.RoleProbe, isolationprobe.RoleAnchor)
	}
	if timeout <= 0 || timeout > MaxJobTimeout {
		return fmt.Errorf("--timeout %s must be above zero and at most %s", timeout, MaxJobTimeout)
	}
	if hold < 0 || hold >= timeout {
		return fmt.Errorf("--hold %s must be at least zero and under the %s job timeout", hold, timeout)
	}
	sparkwing.Job(plan, isolationprobe.SiblingJob, &siblingJob{})
	allowed := sparkwing.Job(plan, AllowedJob, &allowedJob{})
	probe := &probeJob{Allowed: sparkwing.RefTo[allowedOut](allowed), role: role, hold: hold, runID: run.RunID}
	sparkwing.Job(plan, isolationprobe.ProbeJob, probe).Needs(allowed).Timeout(timeout)
	return nil
}

// Register adds the pipeline to the SDK registry. The fixture's main calls it, so importing this
// package registers nothing.
func Register() {
	sparkwing.Register[Args](Entrypoint, func() sparkwing.Pipeline[Args] { return &IsolationProbe{} })
}

type siblingOut struct {
	Marker string `json:"marker"`
}

type siblingJob struct {
	sparkwing.Base
	sparkwing.Produces[siblingOut]
}

func (siblingJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "produce", func(context.Context) (siblingOut, error) {
		return siblingOut{Marker: Pipeline}, nil
	}), nil
}

type allowedOut struct {
	Marker string `json:"marker"`
}

type allowedJob struct {
	sparkwing.Base
	sparkwing.Produces[allowedOut]
}

func (allowedJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "produce", func(context.Context) (allowedOut, error) {
		return allowedOut{Marker: allowedMarker}, nil
	}), nil
}

const allowedMarker = Pipeline + " allowed output"

type probeJob struct {
	sparkwing.Base
	// safety: holding the ref as a field is what declares the read to the host.
	Allowed sparkwing.Ref[allowedOut]
	role    string
	hold    time.Duration
	runID   string
}

func (j *probeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "probe", j.probe), nil
}

func (j *probeJob) probe(ctx context.Context) error {
	socket := dockerSocket(os.Getenv("DOCKER_HOST"))
	ok, err := isolationprobe.Run(ctx, isolationprobe.Options{
		Role:     j.role,
		PlanPath: filepath.Join(sparkwing.WorkDir(), isolationprobe.PlanFile),
		WorkDir:  workDir(socket),
		RunID:    j.runID,
		Hold:     j.hold,
		Docker:   socket,
		Exec:     runCommand,
		Emit:     func(line string) { sparkwing.Info(ctx, "%s", line) },
		Extra:    j.sdkOutputProbes,
	})
	if err != nil {
		return err
	}
	return j.verdict(ok)
}

func (*probeJob) verdict(ok bool) error {
	if ok {
		return nil
	}
	return errors.New("containment breached, or a probe proved nothing; the probe results above name each one")
}

// safety: the probe learns only the exit code; a command that never exited normally reports no code,
// and nothing it printed is kept.
func runCommand(ctx context.Context, name string, args ...string) (int, error) {
	_, err := sparkwing.Exec(ctx, name, args...).Capture()
	if err == nil {
		return 0, nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return -1, cerr
	}
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) && execErr.ExitCode >= 0 {
		return execErr.ExitCode, nil
	}
	return -1, errors.New("command did not run")
}

// safety: Docker resolves bind sources in the sidecar, so the exchange directory sits beside the
// socket on the scratch volume the job and the sidecar share.
func workDir(socket string) string {
	if socket == "" {
		return filepath.Join(os.TempDir(), "isolation-probe")
	}
	return filepath.Join(filepath.Dir(socket), "isolation-probe")
}

func dockerSocket(host string) string {
	path, _ := strings.CutPrefix(host, "unix://")
	if path == host {
		return ""
	}
	return path
}

// SDK probe names, shared by the probes and the driver's coverage check.
const (
	ProbeSDKAllowed = "SDK reads the output it depends on"
	ProbeSDKSibling = "SDK reads a job output it does not depend on"
	ProbeSDKLastRun = "SDK reads an undeclared pipeline output"
)

// SDKKeys lists the results the fixture's SDK probes report.
func SDKKeys() []isolationprobe.Key {
	key := func(probe string) isolationprobe.Key {
		return isolationprobe.Key{Context: isolationprobe.ContextJob, Item: "C3", Probe: probe}
	}
	return []isolationprobe.Key{key(ProbeSDKAllowed), key(ProbeSDKSibling), key(ProbeSDKLastRun)}
}

// safety: the host refuses an output read either at its store's authority binding or at the
// evaluated job's read set; both are binding refusals of the same request.
var bindingRefusals = []string{"output is outside the evaluated job read set", "resource is outside the authorized binding"}

// safety: only a host binding refusal proves confinement. Any other failure settles nothing, and the
// outcome names the refusal it matched or a fixed category, never the failure's own text.
func sdkOutputProbe(name string, read func()) (r isolationprobe.Result) {
	r = isolationprobe.Result{Item: "C3", Context: isolationprobe.ContextJob, Probe: name, Expect: isolationprobe.ExpectDenied, Outcome: "read", Verdict: isolationprobe.Breach}
	defer func() {
		if failure := recover(); failure != nil {
			message := fmt.Sprint(failure)
			r.Outcome, r.Verdict = "failed without a binding refusal", isolationprobe.Inconclusive
			for _, refusal := range bindingRefusals {
				if strings.Contains(message, refusal) {
					r.Outcome, r.Verdict = "refused: "+refusal, isolationprobe.Pass
				}
			}
		}
	}()
	read()
	return r
}

// safety: the allowed read is the control that proves the SDK can read an output in this job, so
// the refusals beside it are the binding's and not a broken read path.
func allowedReadProbe(read func() string) (r isolationprobe.Result) {
	r = isolationprobe.Result{Item: "C3", Context: isolationprobe.ContextJob, Probe: ProbeSDKAllowed, Expect: isolationprobe.ExpectOpen, Outcome: "failed", Verdict: isolationprobe.Inconclusive}
	defer func() {
		if failure := recover(); failure != nil {
			r.Outcome, r.Verdict = "failed", isolationprobe.Inconclusive
		}
	}()
	if read() == allowedMarker {
		r.Outcome, r.Verdict = "read the expected value", isolationprobe.Pass
	} else {
		r.Outcome = "read an unexpected value"
	}
	return r
}

func (j *probeJob) sdkOutputProbes(ctx context.Context) []isolationprobe.Result {
	return []isolationprobe.Result{
		allowedReadProbe(func() string { return j.Allowed.Get(ctx).Marker }),
		sdkOutputProbe(ProbeSDKSibling, func() {
			sparkwing.Ref[siblingOut]{JobID: isolationprobe.SiblingJob}.Get(ctx)
		}),
		sdkOutputProbe(ProbeSDKLastRun, func() {
			sparkwing.RefToLastRun[siblingOut](Pipeline, isolationprobe.SiblingJob).Get(ctx)
		}),
	}
}
