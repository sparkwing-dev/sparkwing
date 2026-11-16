package isolationprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Options configures one probe run inside a job.
type Options struct {
	// Role is RoleProbe, or RoleAnchor to keep the listener up for Hold after probing.
	Role     string
	PlanPath string
	// WorkDir holds this run's work directory: the canary, the DinD exchange files and mount
	// points. Docker resolves bind sources in the sidecar, so it must sit on the scratch volume
	// both share.
	WorkDir string
	// RunID is the run's own ID for the anonymous route probes; empty leaves them unprobed.
	RunID string
	Hold  time.Duration
	// Docker is the DinD socket path, and Exec runs the docker CLI against it; either one empty
	// leaves the DinD contexts unprobed.
	Docker string
	Exec   Exec
	// Emit receives every marker line.
	Emit func(line string)
	// Extra adds probes only the SDK wrapper can make.
	Extra func(ctx context.Context) []Result
}

// Run probes from the job container and from two DinD containers at once, emits every result and
// a summary, and reports whether the run passed. An anchor then holds its listener open for Hold
// or until ctx ends.
func Run(ctx context.Context, opts Options) (bool, error) {
	plan, err := ReadPlan(opts.PlanPath)
	if err != nil {
		return false, err
	}
	if opts.Role != RoleProbe && opts.Role != RoleAnchor {
		return false, fmt.Errorf("role %q must be %s or %s", opts.Role, RoleProbe, RoleAnchor)
	}
	// safety: the work directory is named for this run's canary, so the walk exempts exactly this
	// run's files and still searches a peer's directory on a shared volume.
	runDir := filepath.Join(opts.WorkDir, "run-"+plan.Canary)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(runDir, plan.Canary), nil, 0o600); err != nil {
		return false, fmt.Errorf("write canary: %w", err)
	}
	rootfs, removeRootfs := rootfsCanary(plan.Canary)
	defer removeRootfs()
	ln, err := listen(plan.ListenPort)
	if err != nil {
		return false, fmt.Errorf("listen on %d: %w", plan.ListenPort, err)
	}
	defer ln.Close() //nolint:errcheck // the listener closes with the run
	ips, netns := podIPs(), readNetNS()
	node, nodeErr := discoverNodeAddress(ctx, 3*time.Second)
	home, _ := os.UserHomeDir() //nolint:errcheck // an unknown home skips the .netrc check
	opts.Emit(FormatLine(ReadyMarker, Ready{PodIPs: ips, ListenPort: plan.ListenPort, NetNS: netns, NodeIP: node, Home: home}))

	n := newNetwork()
	self, _ := os.Hostname() //nolint:errcheck // an unknown pod name leaves the kube API grade unproven
	spec := kubeSpecFor(plan, self, findCredentials(os.Environ(), []string{"/"}, os.ReadFile, n.nodeRoleReadable(ctx, imdsBase)))
	n.kube = &spec
	plan.KubePods, plan.SelfPod = spec.pods, self
	targets := append(append([]Target(nil), plan.Targets...), ownNodeTargets(node)...)
	own := ownScratch(opts.Docker, runDir)
	var routes, dind []Result
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		routes = n.routeProbes(ctx, ContextJob, plan, opts.RunID, waitTicks(5*time.Second, 10*time.Minute))
	}()
	go func() {
		defer wg.Done()
		dind = dindProbes(ctx, opts, runDir, plan, targets, netns, own)
	}()
	results := []Result{rootfs}
	if nodeErr != nil {
		results = append(results, Result{Item: "C8", Context: ContextJob, Probe: probeOwnNode, Expect: ExpectObserved, Outcome: "not discovered: " + classifyDial(nodeErr), Verdict: NotProbed})
	}
	results = append(results, n.positive(ctx, ContextJob)...)
	results = append(results, n.selfConnect(ctx, ContextJob, ips, plan.ListenPort)...)
	results = append(results, n.probeAll(ctx, ContextJob, append(targets, ownPodDockerTargets(ips)...))...)
	results = append(results, credentialProbes(ContextJob, os.Environ(), home, os.ReadFile)...)
	results = append(results, hostProbes(hostOptions{where: ContextJob, workDir: runDir, podNetNS: netns, own: own, peerCanaries: plan.PeerCanaries})...)
	if opts.Extra != nil {
		results = append(results, opts.Extra(ctx)...)
	}
	wg.Wait()
	results = append(append(results, routes...), dind...)
	for _, r := range results {
		opts.Emit(FormatLine(ResultMarker, r))
	}
	sum := Summarize(results)
	opts.Emit(FormatLine(SummaryMarker, sum))
	if opts.Role == RoleAnchor {
		hold(ctx, opts.Hold)
	}
	return sum.OK, nil
}

// safety: a canary in the container's own root filesystem, outside scratch, lets a peer that can
// read node storage find this pod's files; it is removed when the run ends.
func rootfsCanary(name string) (Result, func()) {
	r := Result{Item: "C10", Context: ContextJob, Probe: probeRootfsCanary, Expect: ExpectOpen, Verdict: Pass}
	for _, dir := range []string{"/var/tmp", "/"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o600); err == nil {
			r.Outcome = "written under " + dir
			return r, func() { _ = os.Remove(path) } //nolint:errcheck // the container is discarded with the run
		}
	}
	r.Outcome, r.Verdict = "root filesystem not writable", NotProbed
	return r, func() {}
}

func dindProbes(ctx context.Context, opts Options, runDir string, plan Plan, targets []Target, netns string, own []string) []Result {
	if opts.Docker == "" || opts.Exec == nil {
		return []Result{{Item: "C12", Context: ContextPrivileged, Probe: "DinD contexts", Expect: ExpectBlocked, Outcome: "no Docker socket", Verdict: NotProbed}}
	}
	exe, err := os.Executable()
	var data []byte
	if err == nil {
		data, err = os.ReadFile(exe)
	}
	if err != nil {
		return []Result{{Item: "C12", Context: ContextPrivileged, Probe: "probe image", Expect: ExpectOpen, Outcome: "executable unreadable", Verdict: Inconclusive}}
	}
	inner := plan
	inner.Targets = targets
	return dindContexts(ctx, dockerCLI{exec: opts.Exec, host: "unix://" + opts.Docker}, runDir, inner, netns, own, data)
}

func hold(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// RunContext probes from inside a DinD container: the network targets from Docker's bridge or the
// pod's own network, and, in the privileged container, every host check. own lists the paths the
// pod owns as the job container sees them, which the privileged container finds under /host.
func RunContext(ctx context.Context, where string, plan Plan, podNetNS, workDir string, own []string) []Result {
	n := newNetwork()
	roots := []string{"/"}
	if where == ContextPrivileged {
		roots = append(roots, "/host", "/proc/1/root")
	}
	spec := kubeSpecFor(plan, plan.SelfPod, findCredentials(os.Environ(), roots, os.ReadFile, n.nodeRoleReadable(ctx, imdsBase)))
	n.kube = &spec
	results := n.positive(ctx, where)
	results = append(results, n.probeAll(ctx, where, plan.Targets)...)
	if where != ContextPrivileged {
		return results
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return append(results, Result{Item: "C12", Context: where, Probe: "host checks", Expect: ExpectBlocked, Outcome: "work directory unusable", Verdict: Inconclusive})
	}
	return append(results, hostProbes(hostOptions{where: where, workDir: workDir, podNetNS: podNetNS, own: own, peerCanaries: plan.PeerCanaries})...)
}

// Main runs the probe's command line: run probes a job, context probes from inside a DinD
// container. It returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: isolation-probe run|context [flags]")
		return 2
	}
	fl := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fl.SetOutput(stderr)
	planPath := fl.String("plan", PlanFile, "plan file")
	workDir := fl.String("work-dir", filepath.Join(os.TempDir(), "isolation-probe"), "scratch for the canary and DinD exchange")
	switch args[0] {
	case "run":
		role := fl.String("role", RoleProbe, "probe or anchor")
		runID := fl.String("run-id", "", "this run's ID, for the anonymous route probes")
		holdFor := fl.Duration("hold", 0, "how long an anchor keeps its listener up after probing")
		if err := fl.Parse(args[1:]); err != nil {
			return 2
		}
		ok, err := Run(ctx, Options{
			Role: *role, PlanPath: *planPath, WorkDir: *workDir, RunID: *runID, Hold: *holdFor,
			Emit: func(line string) { fmt.Fprintln(stdout, line) },
		})
		if err != nil {
			fmt.Fprintln(stderr, "isolation-probe:", err)
			return 2
		}
		if !ok {
			return 1
		}
		return 0
	case "context":
		where := fl.String("context", "", "context name")
		out := fl.String("out", "", "file for the NDJSON results")
		podNetNS := fl.String("pod-netns", "", "the job container's network namespace")
		ownList := fl.String("own", "", "comma-separated paths the pod owns, as the job container sees them")
		if err := fl.Parse(args[1:]); err != nil {
			return 2
		}
		var own []string
		for _, p := range strings.Split(*ownList, ",") {
			if p = strings.TrimSpace(p); p != "" {
				own = append(own, p)
			}
		}
		plan, err := ReadPlan(*planPath)
		if err == nil && *where != ContextBridge && *where != ContextPrivileged {
			err = fmt.Errorf("context %q must be %s or %s", *where, ContextBridge, ContextPrivileged)
		}
		if err != nil {
			fmt.Fprintln(stderr, "isolation-probe:", err)
			return 2
		}
		results := RunContext(ctx, *where, plan, *podNetNS, *workDir, own)
		var buf bytes.Buffer
		for _, r := range results {
			line, _ := json.Marshal(r) //nolint:errcheck // a Result always marshals
			buf.Write(append(line, '\n'))
		}
		if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
			fmt.Fprintln(stderr, "isolation-probe: write results:", errnoCategory(err))
			return 2
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: isolation-probe run|context [flags]")
		return 2
	}
}

func errnoCategory(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "no such directory"
	default:
		return "failed"
	}
}
