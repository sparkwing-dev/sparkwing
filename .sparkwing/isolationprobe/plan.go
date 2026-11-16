// Package isolationprobe tests a Sparkwing Cloud job's containment from inside the job: the egress
// fence, instance metadata, the kube API, neighboring nodes and pods, the node filesystem and the
// DinD sidecar's namespaced privileges. Each probe reports a verdict and what happened, never a
// value it read.
package isolationprobe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

// Command is the argument that turns the fixture's pipeline binary into the probe, which is how a
// DinD container runs the same static executable.
const Command = "isolation-probe"

// PlanFile is the name of the plan the driver commits at the root of each probe checkout.
const PlanFile = "isolation-plan.json"

// Roles a run plays in the driver's sequence. An anchor keeps its listener up after probing so a
// later run can aim at it.
const (
	RoleProbe  = "probe"
	RoleAnchor = "anchor"
)

// Expect says what must happen when a probe reaches for a target.
type Expect string

const (
	// ExpectBlocked targets sit behind the egress fence: the connection must fail.
	ExpectBlocked Expect = "blocked"
	// ExpectDenied targets may answer, but only to refuse an anonymous caller.
	ExpectDenied Expect = "denied"
	// ExpectOpen targets are positive controls the job must reach.
	ExpectOpen Expect = "open"
	// ExpectObserved targets are recorded without a pass or fail.
	ExpectObserved Expect = "observed"
)

// Grade names the anonymous follow-up request that settles a target that accepted a connection.
type Grade string

const (
	// GradeKubelet asks the kubelet for its pods.
	GradeKubelet Grade = "kubelet"
	// GradeKubeAPI lists the target namespace's secrets, reviews the caller's rules there and
	// submits a dry-run Job that mounts a secret.
	GradeKubeAPI Grade = "kube-api"
	// GradeIMDS asks for an IMDSv2 token and IMDSv1 metadata.
	GradeIMDS Grade = "imds"
)

// Target is one network destination and what reaching it means. Listening marks a destination
// the driver knows answers, so a refused connection there proves the packet reached the host.
// Namespace names the deployment's namespace a kube API grade asks about.
type Target struct {
	Item      string `json:"item"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	Expect    Expect `json:"expect"`
	Listening bool   `json:"listening,omitempty"`
	Grade     Grade  `json:"grade,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// Plan is everything one probe run needs that it cannot discover from inside the job.
type Plan struct {
	Team          string   `json:"team"`
	ControllerURL string   `json:"controller_url"`
	ListenPort    int      `json:"listen_port"`
	Canary        string   `json:"canary"`
	PeerCanaries  []string `json:"peer_canaries,omitempty"`
	PeerRuns      []string `json:"peer_runs,omitempty"`
	Targets       []Target `json:"targets"`
}

// ReadPlan loads and validates a plan file.
func ReadPlan(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, fmt.Errorf("read plan: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return Plan{}, fmt.Errorf("parse plan %s: %w", path, err)
	}
	return p, p.Validate()
}

// Validate refuses a plan the probe could not judge, so a run never passes on a target it did not
// understand.
func (p Plan) Validate() error {
	var errs []error
	if p.ListenPort <= 0 || p.ListenPort > 65535 {
		errs = append(errs, fmt.Errorf("listen_port %d is outside 1-65535", p.ListenPort))
	}
	if !validName(p.Canary) {
		errs = append(errs, fmt.Errorf("canary %q must be a plain file name", p.Canary))
	}
	for _, c := range p.PeerCanaries {
		if !validName(c) {
			errs = append(errs, fmt.Errorf("peer canary %q must be a plain file name", c))
		}
	}
	if !strings.HasPrefix(p.ControllerURL, "https://") && !strings.HasPrefix(p.ControllerURL, "http://") {
		errs = append(errs, fmt.Errorf("controller_url %q must be an http(s) URL", p.ControllerURL))
	}
	for i, t := range p.Targets {
		if err := t.validate(); err != nil {
			errs = append(errs, fmt.Errorf("target %d (%s): %w", i, t.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (t Target) validate() error {
	if t.Item == "" || t.Name == "" {
		return errors.New("item and name are required")
	}
	if _, _, err := net.SplitHostPort(t.Address); err != nil {
		return fmt.Errorf("address %q: %w", t.Address, err)
	}
	switch t.Expect {
	case ExpectBlocked, ExpectOpen, ExpectObserved:
	case ExpectDenied:
		if t.Grade == "" {
			return errors.New("a denied target needs a grade to settle an answered connection")
		}
	default:
		return fmt.Errorf("unknown expect %q", t.Expect)
	}
	switch t.Grade {
	case "", GradeKubelet, GradeIMDS:
		return nil
	case GradeKubeAPI:
		if !validName(t.Namespace) {
			return errors.New("a kube API grade needs the namespace it asks about")
		}
		return nil
	default:
		return fmt.Errorf("unknown grade %q", t.Grade)
	}
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}
