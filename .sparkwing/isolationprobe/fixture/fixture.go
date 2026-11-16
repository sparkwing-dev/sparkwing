// Package fixture is the pipeline the cloud-isolation driver uploads as each staging team: one job
// runs the isolation probe, and a sibling job produces an output the probe must not read.
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

// JobTimeout bounds the probe job, an anchor's hold included.
const JobTimeout = 20 * time.Minute

// Args are the flags the driver passes to each probe run.
type Args struct {
	Role string `flag:"role" desc:"probe, or anchor to keep the listener up after probing" default:"probe"`
	Hold string `flag:"hold" desc:"How long an anchor keeps its listener up after probing, as a Go duration" default:"0s"`
}

// IsolationProbe is the hostile containment probe as a Cloud pipeline.
type IsolationProbe struct{ sparkwing.Base }

// ShortHelp describes the pipeline in one line.
func (IsolationProbe) ShortHelp() string {
	return "Hostile containment probe for one Cloud job; the cloud-isolation pipeline runs it on staging"
}

// Plan declares the probe job and the sibling whose output the probe must not read. Discovery
// plans with empty arguments, which mean a probe that does not hold.
func (IsolationProbe) Plan(_ context.Context, plan *sparkwing.Plan, in Args, run sparkwing.RunContext) error {
	role, holdText := in.Role, in.Hold
	if role == "" {
		role = isolationprobe.RoleProbe
	}
	if holdText == "" {
		holdText = "0s"
	}
	hold, err := time.ParseDuration(holdText)
	if err != nil {
		return fmt.Errorf("--hold: %w", err)
	}
	if role != isolationprobe.RoleProbe && role != isolationprobe.RoleAnchor {
		return fmt.Errorf("--role %q must be %s or %s", role, isolationprobe.RoleProbe, isolationprobe.RoleAnchor)
	}
	if hold < 0 || hold >= JobTimeout {
		return fmt.Errorf("--hold %s must be at least zero and under the %s job timeout", hold, JobTimeout)
	}
	sparkwing.Job(plan, isolationprobe.SiblingJob, &siblingJob{})
	sparkwing.Job(plan, isolationprobe.ProbeJob, &probeJob{role: role, hold: hold, runID: run.RunID}).Timeout(JobTimeout)
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

type probeJob struct {
	sparkwing.Base
	role  string
	hold  time.Duration
	runID string
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
		Emit:     func(line string) { sparkwing.Info(ctx, "%s", line) },
		Extra:    sdkOutputProbes,
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

// safety: only a host binding refusal proves confinement; any other failure settles nothing and is
// reported with its message, which names the refusal and never an output value.
func sdkOutputProbe(name string, read func()) (r isolationprobe.Result) {
	r = isolationprobe.Result{Item: "C3", Context: isolationprobe.ContextJob, Probe: name, Expect: isolationprobe.ExpectDenied, Outcome: "read", Verdict: isolationprobe.Breach}
	defer func() {
		if failure := recover(); failure != nil {
			message := strings.Join(strings.Fields(fmt.Sprint(failure)), " ")
			r.Outcome, r.Verdict = "failed without the read-set refusal: "+truncate(message, 200), isolationprobe.Inconclusive
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

// safety: the host refuses an output read either at its store's authority binding or at the
// evaluated job's read set; both are binding refusals of the same request.
var bindingRefusals = []string{"output is outside the evaluated job read set", "resource is outside the authorized binding"}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sdkOutputProbes(ctx context.Context) []isolationprobe.Result {
	return []isolationprobe.Result{
		sdkOutputProbe("SDK reads a job output it does not depend on", func() {
			sparkwing.Ref[siblingOut]{JobID: isolationprobe.SiblingJob}.Get(ctx)
		}),
		sdkOutputProbe("SDK reads an undeclared pipeline output", func() {
			sparkwing.RefToLastRun[siblingOut](Pipeline, isolationprobe.SiblingJob).Get(ctx)
		}),
	}
}
