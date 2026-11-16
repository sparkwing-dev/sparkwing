package isolationprobe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Options configures one probe run inside a job.
type Options struct {
	// Role is RoleProbe, or RoleAnchor to keep the listener up for Hold after probing.
	Role     string
	PlanPath string
	// WorkDir is scratch for the canary, the DinD exchange files and mount points. Docker
	// resolves bind sources in the sidecar, so it must sit on the scratch volume both share.
	WorkDir string
	// RunID is the run's own ID for the anonymous route probes; empty leaves them unprobed.
	RunID string
	Hold  time.Duration
	// Docker is the Docker socket path; empty leaves the DinD contexts unprobed.
	Docker string
	// Emit receives every marker line.
	Emit func(line string)
	// Extra adds probes only the SDK wrapper can make.
	Extra func(ctx context.Context) []Result
}

// Run probes from the job container and then from two DinD containers, emits every result and a
// summary, and reports whether the run passed. An anchor then holds its listener open for Hold or
// until ctx ends.
func Run(ctx context.Context, opts Options) (bool, error) {
	plan, err := ReadPlan(opts.PlanPath)
	if err != nil {
		return false, err
	}
	if opts.Role != RoleProbe && opts.Role != RoleAnchor {
		return false, fmt.Errorf("role %q must be %s or %s", opts.Role, RoleProbe, RoleAnchor)
	}
	if err := os.MkdirAll(opts.WorkDir, 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(opts.WorkDir, plan.Canary), nil, 0o600); err != nil {
		return false, fmt.Errorf("write canary: %w", err)
	}
	ln, err := listen(plan.ListenPort)
	if err != nil {
		return false, fmt.Errorf("listen on %d: %w", plan.ListenPort, err)
	}
	defer ln.Close() //nolint:errcheck // the listener closes with the run
	ips, netns := podIPs(), readNetNS()
	node, nodeErr := discoverNodeAddress(ctx, 3*time.Second)
	opts.Emit(FormatLine(ReadyMarker, Ready{PodIPs: ips, ListenPort: plan.ListenPort, NetNS: netns, NodeIP: node}))

	n := newNetwork()
	targets := append(append([]Target(nil), plan.Targets...), ownNodeTargets(node)...)
	var results []Result
	if nodeErr != nil {
		results = append(results, Result{Item: "C8", Context: ContextJob, Probe: "own node discovered", Expect: ExpectObserved, Outcome: "not discovered: " + classifyDial(nodeErr), Verdict: NotProbed})
	}
	results = append(results, n.positive(ctx, ContextJob)...)
	results = append(results, n.selfConnect(ctx, ContextJob, ips, plan.ListenPort)...)
	results = append(results, n.probeAll(ctx, ContextJob, targets)...)
	results = append(results, n.routeProbes(ctx, ContextJob, plan, opts.RunID, waitTicks(5*time.Second, 10*time.Minute))...)
	home, _ := os.UserHomeDir() //nolint:errcheck // an unknown home skips the .netrc check
	results = append(results, credentialProbes(ContextJob, os.Environ(), home, os.ReadFile)...)
	results = append(results, hostProbes(hostOptions{
		where: ContextJob, workDir: opts.WorkDir, podNetNS: netns, markerBases: []string{"/"},
		walkSkip:     []string{"/proc", "/sys", "/dev", "/mnt", filepath.Dir(opts.WorkDir), opts.WorkDir},
		peerCanaries: plan.PeerCanaries, scanCanaries: true,
	})...)
	if opts.Extra != nil {
		results = append(results, opts.Extra(ctx)...)
	}
	if opts.Docker == "" {
		results = append(results, Result{Item: "C12", Context: ContextPrivileged, Probe: "DinD contexts", Expect: ExpectBlocked, Outcome: "no Docker socket", Verdict: NotProbed})
	} else {
		inner := plan
		inner.Targets = targets
		results = append(results, dindContexts(ctx, newDocker(opts.Docker), opts.WorkDir, inner, netns)...)
	}
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

func hold(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

type dindRun struct {
	name    string
	configs []hostConfig
}

// safety: the privileged container asks for everything the sidecar could grant; when dockerd
// refuses that, the refusal is recorded and smaller sets down to the sidecar's own privileges
// still run the checks.
func dindRuns(exchange string) []dindRun {
	binds := []string{exchange + ":/out", "/:/host:ro"}
	unconfined := []string{"seccomp=unconfined", "apparmor=unconfined"}
	return []dindRun{
		{name: ContextBridge, configs: []hostConfig{{Binds: []string{exchange + ":/out"}, NetworkMode: "bridge"}}},
		{name: ContextPrivileged, configs: []hostConfig{
			{Binds: binds, Privileged: true, PidMode: "host", NetworkMode: "host", IpcMode: "host", UTSMode: "host", CgroupnsMode: "host", CapAdd: []string{"ALL"}, SecurityOpt: unconfined},
			{Binds: binds, Privileged: true, PidMode: "host", NetworkMode: "host"},
			{Binds: binds, PidMode: "host", NetworkMode: "host", CapAdd: []string{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE"}, SecurityOpt: unconfined},
		}},
	}
}

func dindContexts(ctx context.Context, d *dockerClient, workDir string, plan Plan, netns string) []Result {
	fail := func(probe, outcome string) []Result {
		return []Result{{Item: "C12", Context: ContextPrivileged, Probe: probe, Expect: ExpectOpen, Outcome: outcome, Verdict: Inconclusive}}
	}
	if err := d.ping(ctx); err != nil {
		return fail("own Docker daemon answers", "no answer: "+classifyDial(err))
	}
	out := []Result{{Item: "C9", Context: ContextJob, Probe: "own Docker daemon answers", Target: "docker.sock", Expect: ExpectOpen, Outcome: "answered", Verdict: Pass}}
	exe, err := os.Executable()
	if err != nil {
		return append(out, fail("probe image", "executable path unknown")...)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return append(out, fail("probe image", "executable unreadable")...)
	}
	if ok, err := static(data); err != nil || !ok {
		return append(out, fail("probe image", "executable is not a static ELF")...)
	}
	exchange := filepath.Join(workDir, "exchange")
	if err := os.MkdirAll(exchange, 0o777); err != nil {
		return append(out, fail("probe image", "exchange directory: "+err.Error())...)
	}
	planData, err := json.Marshal(plan)
	if err == nil {
		err = os.WriteFile(filepath.Join(exchange, "plan.json"), planData, 0o644)
	}
	if err != nil {
		return append(out, fail("probe image", "exchange plan: "+err.Error())...)
	}
	tag := randomHex(6)
	ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err = d.importImage(ictx, probeBinary, tag, data)
	cancel()
	if err != nil {
		return append(out, fail("probe image", "import failed: "+err.Error())...)
	}
	tag = probeBinary + ":" + tag
	defer func() { _ = d.removeImage(context.WithoutCancel(ctx), tag) }() //nolint:errcheck // cleanup of a throwaway image
	for _, run := range dindRuns(exchange) {
		out = append(out, runContext(ctx, d, tag, exchange, netns, run)...)
	}
	return out
}

func runContext(ctx context.Context, d *dockerClient, tag, exchange, netns string, run dindRun) []Result {
	outFile := run.name + ".ndjson"
	cmd := []string{"/" + probeBinary, Command, "context", "--context", run.name, "--plan", "/out/plan.json", "--out", "/out/" + outFile, "--pod-netns", netns, "--work-dir", "/out/work-" + run.name}
	var notes []Result
	for i, host := range run.configs {
		id, err := d.create(ctx, run.name+"-"+randomHex(4), containerConfig{Image: tag, Cmd: cmd, HostConfig: host})
		if err == nil {
			if err = d.start(ctx, id); err == nil {
				return append(notes, collect(ctx, d, id, exchange, outFile, run.name)...)
			}
			_ = d.remove(context.WithoutCancel(ctx), id) //nolint:errcheck // cleanup of a container that never started
		}
		if i == len(run.configs)-1 {
			return append(notes, Result{Item: "C12", Context: run.name, Probe: "container starts", Expect: ExpectOpen, Outcome: "refused: " + err.Error(), Verdict: Inconclusive})
		}
		notes = append(notes, Result{Item: "C12", Context: run.name, Probe: "dockerd grants every requested privilege", Expect: ExpectObserved, Outcome: "refused: " + err.Error(), Verdict: Observed})
	}
	return notes
}

func collect(ctx context.Context, d *dockerClient, id, exchange, outFile, where string) []Result {
	defer func() { _ = d.remove(context.WithoutCancel(ctx), id) }() //nolint:errcheck // cleanup of a finished container
	wctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	code, err := d.wait(wctx, id)
	cancel()
	data, rerr := os.ReadFile(filepath.Join(exchange, outFile))
	results, perr := parseResultFile(data)
	if err != nil || rerr != nil || perr != nil || len(results) == 0 {
		logs, _ := d.logs(context.WithoutCancel(ctx), id) //nolint:errcheck // diagnosis only
		outcome := fmt.Sprintf("no results (exit %d): %s", code, lastLine(logs))
		return []Result{{Item: "C12", Context: where, Probe: "container reports results", Expect: ExpectOpen, Outcome: outcome, Verdict: Inconclusive}}
	}
	return results
}

func parseResultFile(data []byte) ([]Result, error) {
	var out []Result
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RunContext probes from inside a DinD container: the network targets from Docker's bridge or the
// pod's own network, and, in the privileged container, every host check.
func RunContext(ctx context.Context, where string, plan Plan, podNetNS, workDir string) []Result {
	n := newNetwork()
	results := n.positive(ctx, where)
	results = append(results, n.probeAll(ctx, where, plan.Targets)...)
	if where == ContextPrivileged {
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return append(results, Result{Item: "C12", Context: where, Probe: "host checks", Expect: ExpectBlocked, Outcome: "work directory: " + err.Error(), Verdict: Inconclusive})
		}
		results = append(results, hostProbes(hostOptions{
			where: where, workDir: workDir, privileged: true, podNetNS: podNetNS,
			markerBases:  []string{"/host", "/proc/1/root"},
			walkSkip:     []string{"/proc", "/sys", "/dev", "/out", "/host/proc", "/host/sys", "/host/dev", "/host/tmp"},
			peerCanaries: plan.PeerCanaries, scanCanaries: true,
		})...)
	}
	return results
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
		docker := fl.String("docker", "", "Docker socket for the DinD contexts")
		if err := fl.Parse(args[1:]); err != nil {
			return 2
		}
		ok, err := Run(ctx, Options{
			Role: *role, PlanPath: *planPath, WorkDir: *workDir, RunID: *runID, Hold: *holdFor, Docker: *docker,
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
		if err := fl.Parse(args[1:]); err != nil {
			return 2
		}
		plan, err := ReadPlan(*planPath)
		if err == nil && *where != ContextBridge && *where != ContextPrivileged {
			err = fmt.Errorf("context %q must be %s or %s", *where, ContextBridge, ContextPrivileged)
		}
		if err != nil {
			fmt.Fprintln(stderr, "isolation-probe:", err)
			return 2
		}
		results := RunContext(ctx, *where, plan, *podNetNS, *workDir)
		var buf bytes.Buffer
		for _, r := range results {
			line, _ := json.Marshal(r) //nolint:errcheck // a Result always marshals
			buf.Write(append(line, '\n'))
		}
		if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
			fmt.Fprintln(stderr, "isolation-probe:", errors.Join(errors.New("write results"), err))
			return 2
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: isolation-probe run|context [flags]")
		return 2
	}
}
