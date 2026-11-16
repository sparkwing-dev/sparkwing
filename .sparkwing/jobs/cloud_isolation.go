package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sparkwing-pipelines/isolationprobe"
	"sparkwing-pipelines/isolationprobe/fixture"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// CloudIsolationArgs names the deployment the conformance run attacks. The repository's
// sparkwing.yaml supplies staging's values; every other target is discovered from the cluster.
type CloudIsolationArgs struct {
	TeamA         string `flag:"team-a" desc:"CLI profile of the first conformance team; the profile is named for its team"`
	TeamB         string `flag:"team-b" desc:"CLI profile of the second conformance team; the profile is named for its team"`
	ControllerURL string `flag:"controller-url" desc:"Controller origin the probes call without credentials"`
	KubeContext   string `flag:"kube-context" desc:"kubectl context with read access to the cluster"`
	ControllerNS  string `flag:"controller-namespace" desc:"Namespace of the controller, launcher and checkout service"`
	JobNamespace  string `flag:"job-namespace" desc:"Namespace the launcher creates Cloud Jobs in"`
	InfraNodePool string `flag:"infra-node-pool" desc:"Karpenter NodePool of the controller's nodes"`
	IngressNS     string `flag:"ingress-namespaces" desc:"Comma-separated namespaces of the ingress proxies in front of the controller"`
	Database      string `flag:"database" desc:"Controller database endpoint as host:port; unprobed when empty"`
	VPCResolver   string `flag:"vpc-resolver" desc:"The VPC's Route 53 Resolver address"`
	PublicContext string `flag:"public-api-context" desc:"kubeconfig context whose server is the cluster's public API endpoint"`
	Evidence      string `flag:"evidence" desc:"Directory for the report and captures; a new directory under the system temporary directory when empty"`
}

// CloudIsolation attacks Cloud execution isolation from two teams and judges every refusal.
type CloudIsolation struct{ sparkwing.Base }

// ShortHelp describes the pipeline in one line.
func (CloudIsolation) ShortHelp() string {
	return "Hostile-job conformance: two Cloud teams probe each other, the cluster and their nodes; run before each release"
}

// Help describes what the pipeline does and needs.
func (CloudIsolation) Help() string {
	return "Uploads the isolation-probe fixture as each of two conformance teams and runs it on Cloud in three " +
		"phases: team B's anchor, then team A probing it and anchoring, then team B again probing both. Each probe " +
		"job reaches for instance metadata over IPv4 and IPv6, the controller's pods and services, cluster DNS, the " +
		"database, the kube API's private and public endpoints, the other runs' nodes, kubelets, listeners and Docker " +
		"ports, and its own node; checks its mounts, devices, sockets and user namespace; and repeats from a DinD " +
		"container on Docker's bridge and from one with every privilege the sidecar grants, which also tries host " +
		"kernel files, mounts, device nodes and the syscalls the DinD profile refuses. Probes connect and close, open " +
		"kernel files without writing, and grade an unexpected answer with anonymous reads only. The driver reads the " +
		"cluster with kubectl to find targets, map each run to its pod and node, and check team node labels and the " +
		"pod spec, then writes REPORT.md, every probe result and the cluster captures to the evidence directory. It " +
		"fails on any breach, any probe that proved nothing, any missing probe and any shared node. It needs " +
		"sparkwing, kubectl, git and Go on PATH, two team profiles with Cloud credits, and kubectl read access."
}

// Examples shows how to run the conformance check.
func (CloudIsolation) Examples() []sparkwing.Example {
	return []sparkwing.Example{{
		Comment: "Attack staging from the two conformance teams and keep the evidence",
		Command: "sparkwing run cloud-isolation -- --evidence ~/evidence/cloud-isolation/$(date +%F)",
	}}
}

// Plan declares the single conformance job.
func (CloudIsolation) Plan(_ context.Context, plan *sparkwing.Plan, in CloudIsolationArgs, run sparkwing.RunContext) error {
	sparkwing.Job(plan, "conformance", &cloudIsolationJob{args: in, runID: run.RunID}).Timeout(90 * time.Minute)
	return nil
}

// safety: anchors hold their listeners as long as the probe job's timeout allows and the driver
// cancels them once the last run has probed them; a peer gone early fails its item.
const (
	anchorHold   = fixture.JobTimeout - 2*time.Minute
	readyTimeout = 15 * time.Minute
	probeTimeout = 25 * time.Minute
	pollInterval = 10 * time.Second
)

func (in CloudIsolationArgs) validate() error {
	var errs []error
	for _, f := range []struct{ name, value string }{
		{"--team-a", in.TeamA},
		{"--team-b", in.TeamB},
		{"--controller-url", in.ControllerURL},
		{"--kube-context", in.KubeContext},
		{"--controller-namespace", in.ControllerNS},
		{"--job-namespace", in.JobNamespace},
		{"--public-api-context", in.PublicContext},
	} {
		if strings.TrimSpace(f.value) == "" {
			errs = append(errs, fmt.Errorf("%s is required", f.name))
		}
	}
	if in.TeamA != "" && in.TeamA == in.TeamB {
		errs = append(errs, errors.New("--team-a and --team-b must name different teams"))
	}
	return errors.Join(errs...)
}

type cloudIsolationJob struct {
	sparkwing.Base
	args  CloudIsolationArgs
	runID string
}

func (j *cloudIsolationJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "conformance", func(ctx context.Context) error {
		tmp, err := os.MkdirTemp("", "cloud-isolation-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp) //nolint:errcheck // scratch for fixture checkouts
		evidence := j.args.Evidence
		if evidence == "" {
			evidence = filepath.Join(os.TempDir(), "cloud-isolation-"+j.runID)
		}
		d := &isolationDriver{args: j.args, repo: sparkwing.WorkDir(), tmp: tmp, evidence: evidence, tools: realIsolationTools()}
		return d.conform(ctx)
	}), nil
}

type isolationCommand struct {
	Dir  string
	Env  map[string]string
	Name string
	Args []string
}

type isolationTools struct {
	run  func(context.Context, isolationCommand) (string, int, error)
	wait func(context.Context, time.Duration) error
	now  func() time.Time
	say  func(context.Context, string)
}

func realIsolationTools() isolationTools {
	return isolationTools{
		run: func(ctx context.Context, c isolationCommand) (string, int, error) {
			res, err := sparkwing.Exec(ctx, c.Name, c.Args...).Dir(c.Dir).EnvMap(c.Env).Capture()
			var execErr *sparkwing.ExecError
			if errors.As(err, &execErr) {
				return execErr.Stdout, execErr.ExitCode, nil
			}
			if err != nil {
				return "", -1, err
			}
			return res.Stdout, 0, nil
		},
		wait: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		now: time.Now,
		say: func(ctx context.Context, msg string) { sparkwing.Info(ctx, "%s", msg) },
	}
}

type isolationDriver struct {
	args     CloudIsolationArgs
	repo     string
	tmp      string
	evidence string
	tools    isolationTools
}

// safety: the fixture's origin is a non-GitHub URL, so its identity comes from the URL and a team
// needs no GitHub App installation to upload it.
const fixtureOrigin = "https://cloud-isolation.sparkwing.invalid/isolation-probe.git"

const fixtureListenPort = 18080

func (d *isolationDriver) step(ctx context.Context, desc string, fn func() error) error {
	d.tools.say(ctx, desc)
	if err := fn(); err != nil {
		return fmt.Errorf("%s: %w", desc, err)
	}
	return nil
}

func (d *isolationDriver) cmd(ctx context.Context, dir string, env map[string]string, name string, args ...string) (string, error) {
	out, code, err := d.tools.run(ctx, isolationCommand{Dir: dir, Env: env, Name: name, Args: args})
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("%s %s: exit %d: %s", name, strings.Join(args, " "), code, strings.TrimSpace(lastLines(out, 3)))
	}
	return out, nil
}

func (d *isolationDriver) sw(ctx context.Context, args ...string) (string, error) {
	return d.cmd(ctx, "", nil, "sparkwing", args...)
}

func (d *isolationDriver) kubectl(ctx context.Context, args ...string) (string, error) {
	return d.cmd(ctx, "", nil, "kubectl", append([]string{"--context", d.args.KubeContext}, args...)...)
}

func (d *isolationDriver) save(name string, data []byte) error {
	path := filepath.Join(d.evidence, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (d *isolationDriver) saveJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return d.save(name, append(data, '\n'))
}

func (d *isolationDriver) conform(ctx context.Context) error {
	if err := d.args.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(d.evidence, 0o755); err != nil {
		return err
	}
	var static []isolationprobe.Target
	if err := d.step(ctx, "discover targets from the cluster", func() (err error) {
		if static, err = d.discover(ctx); err == nil {
			err = d.saveJSON("targets.json", static)
		}
		return err
	}); err != nil {
		return err
	}
	var template string
	if err := d.step(ctx, "scaffold the probe fixture and compile it against the published SDK", func() (err error) {
		template, err = d.fixtureTemplate(ctx)
		return err
	}); err != nil {
		return err
	}
	runs := []*isolationRun{
		{Label: "B1", Team: d.args.TeamB, Role: isolationprobe.RoleAnchor},
		{Label: "A1", Team: d.args.TeamA, Role: isolationprobe.RoleAnchor, Peers: []string{"B1"}},
		{Label: "B2", Team: d.args.TeamB, Role: isolationprobe.RoleProbe, Peers: []string{"A1", "B1"}},
	}
	var started []*isolationRun
	defer func() {
		for _, r := range started {
			saved := filepath.Join(d.evidence, "runs", r.Label+"-probe.log")
			if _, err := os.Stat(saved); errors.Is(err, os.ErrNotExist) {
				if _, raw, err := d.logs(context.WithoutCancel(ctx), r); err == nil {
					_ = d.save("runs/"+r.Label+"-probe.log", []byte(raw)) //nolint:errcheck // best-effort record of a run the driver stopped following
				}
			}
			if !terminal(r.Status) {
				_, _ = d.sw(context.WithoutCancel(ctx), "runs", "cancel", "--run", r.RunID, "--profile", r.Team, "-o", "json") //nolint:errcheck // best-effort cleanup of an abandoned run
			}
		}
	}()
	peers := map[string]peerFacts{}
	for _, r := range runs {
		plan := d.plan(r, static, peers)
		if err := d.saveJSON("plans/"+r.Label+".json", plan); err != nil {
			return err
		}
		if err := d.step(ctx, "trigger "+r.Label+" as "+r.Team+" ("+r.Role+")", func() (err error) {
			r.RunID, err = d.trigger(ctx, template, r, plan)
			return err
		}); err != nil {
			return err
		}
		started = append(started, r)
		if r.Role == isolationprobe.RoleAnchor {
			if err := d.step(ctx, "wait for "+r.Label+" to announce its listener", func() (err error) {
				peers[r.Label], err = d.awaitReady(ctx, r)
				return err
			}); err != nil {
				return err
			}
		}
	}
	final, anchors := runs[len(runs)-1], runs[:len(runs)-1]
	if err := d.step(ctx, "wait for "+final.Label+" to finish probing", func() error { return d.awaitEnd(ctx, final, probeTimeout) }); err != nil {
		return err
	}
	alive := map[string]bool{}
	for _, r := range anchors {
		if err := d.step(ctx, "read "+r.Label+"'s results", func() error { return d.awaitSummary(ctx, r) }); err != nil {
			return err
		}
		pods, err := d.runPods(ctx, r)
		if err != nil {
			return err
		}
		alive[r.Label] = probeRunning(pods)
	}
	if err := d.capture(ctx); err != nil {
		return err
	}
	for _, r := range anchors {
		if err := d.step(ctx, "cancel anchor "+r.Label, func() error {
			if _, err := d.sw(ctx, "runs", "cancel", "--run", r.RunID, "--profile", r.Team, "-o", "json"); err != nil {
				return err
			}
			return d.awaitEnd(ctx, r, readyTimeout)
		}); err != nil {
			return err
		}
	}
	return d.judge(ctx, runs, alive)
}

func (d *isolationDriver) plan(r *isolationRun, static []isolationprobe.Target, peers map[string]peerFacts) isolationprobe.Plan {
	plan := isolationprobe.Plan{
		Team: r.Team, ControllerURL: d.args.ControllerURL, ListenPort: fixtureListenPort,
		Canary: canaryName(r), Targets: append([]isolationprobe.Target(nil), static...),
	}
	for _, label := range r.Peers {
		p := peers[label]
		plan.PeerCanaries = append(plan.PeerCanaries, p.Canary)
		plan.PeerRuns = append(plan.PeerRuns, p.RunID)
		plan.Targets = append(plan.Targets, peerTargets(p)...)
	}
	return plan
}

func canaryName(r *isolationRun) string { return "isolation-canary-" + r.Label + "-" + r.Team }

func (d *isolationDriver) discover(ctx context.Context) ([]isolationprobe.Target, error) {
	load := func(into any, args ...string) error {
		out, err := d.kubectl(ctx, append(args, "-o", "json")...)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(out), into)
	}
	var (
		pods        kubeList[kubePod]
		services    kubeList[kubeService]
		dnsPods     kubeList[kubePod]
		dnsServices kubeList[kubeService]
		endpoints   kubeList[kubeEndpointSlice]
		infra       kubeList[kubeNode]
		in          = targetInputs{Database: d.args.Database, VPCResolver: d.args.VPCResolver}
	)
	config, err := d.cmd(ctx, "", nil, "kubectl", "config", "view", "-o", "json")
	if err != nil {
		return nil, err
	}
	if in.PublicAPI, err = contextServerHost(config, d.args.PublicContext); err != nil {
		return nil, err
	}
	errs := []error{
		load(&pods, "get", "pods", "-n", d.args.ControllerNS),
		load(&services, "get", "services", "-n", d.args.ControllerNS),
		load(&dnsPods, "get", "pods", "-n", "kube-system", "-l", "k8s-app=kube-dns"),
		load(&dnsServices, "get", "services", "-n", "kube-system", "-l", "k8s-app=kube-dns"),
		load(&in.APIService, "get", "service", "kubernetes", "-n", "default"),
		load(&endpoints, "get", "endpointslices", "-n", "default", "-l", "kubernetes.io/service-name=kubernetes"),
	}
	if d.args.InfraNodePool != "" {
		errs = append(errs, load(&infra, "get", "nodes", "-l", "karpenter.sh/nodepool="+d.args.InfraNodePool))
	}
	for _, ns := range strings.Split(d.args.IngressNS, ",") {
		if ns = strings.TrimSpace(ns); ns == "" {
			continue
		}
		var ingress kubeList[kubePod]
		errs = append(errs, load(&ingress, "get", "pods", "-n", ns))
		in.IngressPods = append(in.IngressPods, ingress.Items...)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	in.ControllerPods, in.ControllerServices, in.DNSPods, in.DNSServices = pods.Items, services.Items, dnsPods.Items, dnsServices.Items
	in.APIEndpoints, in.InfraNodes = endpoints.Items, infra.Items
	return staticTargets(in)
}

// safety: the fixture is the published SDK's own scaffold with this repository's probe packages
// copied in, compiled here before upload so an SDK drift fails before any Cloud run starts.
func (d *isolationDriver) fixtureTemplate(ctx context.Context) (string, error) {
	dir := filepath.Join(d.tmp, "template")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", fixtureOrigin}} {
		if err := d.git(ctx, dir, args...); err != nil {
			return "", err
		}
	}
	if _, err := d.sw(ctx, "pipeline", "new", "--pipeline", fixture.Pipeline, "--template", "minimal", "-C", dir, "-o", "json"); err != nil {
		return "", err
	}
	module := filepath.Join(dir, ".sparkwing")
	for _, scaffolded := range []string{"jobs", "main.go", "README.md"} {
		if err := os.RemoveAll(filepath.Join(module, scaffolded)); err != nil {
			return "", err
		}
	}
	src := filepath.Join(d.repo, ".sparkwing", "isolationprobe")
	for _, pkg := range []string{"", "fixture"} {
		if err := copyGoSources(filepath.Join(src, pkg), filepath.Join(module, "isolationprobe", pkg)); err != nil {
			return "", err
		}
	}
	main, err := os.ReadFile(filepath.Join(src, "cmd", "isolation-probe", "main.go"))
	if err != nil {
		return "", err
	}
	yaml := "pipelines:\n  - name: " + fixture.Pipeline + "\n    entrypoint: " + fixture.Entrypoint + "\n"
	if err := errors.Join(os.WriteFile(filepath.Join(module, "main.go"), main, 0o644), os.WriteFile(filepath.Join(module, "sparkwing.yaml"), []byte(yaml), 0o644)); err != nil {
		return "", err
	}
	goEnv := map[string]string{"GOWORK": "off", "GOFLAGS": "-mod=readonly", "CGO_ENABLED": "0"}
	for _, args := range [][]string{{"mod", "edit", "-module", "sparkwing-pipelines"}, {"build", "-o", os.DevNull, "."}, {"vet", "./..."}} {
		if _, err := d.cmd(ctx, module, goEnv, "go", args...); err != nil {
			return "", fmt.Errorf("fixture: %w", err)
		}
	}
	return dir, nil
}

func copyGoSources(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(to, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (d *isolationDriver) git(ctx context.Context, dir string, args ...string) error {
	_, err := d.cmd(ctx, dir, nil, "git", append([]string{"-c", "user.name=Sparkwing cloud-isolation", "-c", "user.email=cloud-isolation@sparkwing.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	return err
}

func (d *isolationDriver) trigger(ctx context.Context, template string, r *isolationRun, plan isolationprobe.Plan) (string, error) {
	checkout := filepath.Join(d.tmp, "checkout-"+r.Label)
	if err := copyTree(template, checkout); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(plan, "", " ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(checkout, isolationprobe.PlanFile), data, 0o644); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "isolation probe " + r.Label}} {
		if err := d.git(ctx, checkout, args...); err != nil {
			return "", err
		}
	}
	hold := "0s"
	if r.Role == isolationprobe.RoleAnchor {
		hold = anchorHold.String()
	}
	out, err := d.sw(ctx, "pipeline", "trigger", fixture.Pipeline, "-C", checkout, "--profile", r.Team,
		"--runner", "cloud", "--working-tree", "--detach", "-o", "json", "--", "--role", r.Role, "--hold", hold)
	if err != nil {
		return "", err
	}
	id := acceptedRunID(out)
	if id == "" {
		return "", fmt.Errorf("trigger printed no run_id: %q", out)
	}
	d.tools.say(ctx, r.Label+" is run "+id)
	return id, nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// safety: runs status exits 3 while a run is live, so its document is read whatever the exit
// code, and only a document naming a status counts.
func (d *isolationDriver) status(ctx context.Context, r *isolationRun) error {
	out, code, err := d.tools.run(ctx, isolationCommand{Name: "sparkwing", Args: []string{"runs", "status", "--run", r.RunID, "--profile", r.Team, "--exit-zero", "-o", "json"}})
	if err != nil {
		return err
	}
	var last struct {
		Status string `json:"status"`
		Team   string `json:"team"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var doc struct {
			Status string `json:"status"`
			Team   string `json:"team"`
		}
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("runs status for %s exited %d: %w", r.RunID, code, err)
		}
		if doc.Status != "" {
			last = doc
		}
	}
	if last.Status == "" {
		return fmt.Errorf("runs status for %s exited %d with no status", r.RunID, code)
	}
	r.Status, r.RunTeam = last.Status, last.Team
	return nil
}

func terminal(status string) bool {
	return status == "success" || status == "failed" || status == "cancelled" || status == "dry_run"
}

func (d *isolationDriver) logs(ctx context.Context, r *isolationRun) (isolationprobe.Log, string, error) {
	out, err := d.sw(ctx, "runs", "logs", "--run", r.RunID, "--profile", r.Team, "--job", isolationprobe.ProbeJob, "--no-events", "-o", "plain")
	if err != nil {
		return isolationprobe.Log{}, out, err
	}
	log, err := isolationprobe.ParseLog(out)
	return log, out, err
}

func (d *isolationDriver) poll(ctx context.Context, r *isolationRun, budget time.Duration, what string, check func() (bool, error)) error {
	deadline := d.tools.now().Add(budget)
	for {
		if done, err := check(); err != nil || done {
			return err
		}
		if err := d.status(ctx, r); err != nil {
			return err
		}
		if terminal(r.Status) {
			if done, err := check(); err != nil || done {
				return err
			}
			return fmt.Errorf("%s ended %s before it would %s", r.Label, r.Status, what)
		}
		if !d.tools.now().Before(deadline) {
			return fmt.Errorf("%s did not %s within %s", r.Label, what, budget)
		}
		if err := d.tools.wait(ctx, pollInterval); err != nil {
			return err
		}
	}
}

func (d *isolationDriver) awaitReady(ctx context.Context, r *isolationRun) (peerFacts, error) {
	var facts peerFacts
	err := d.poll(ctx, r, readyTimeout, "announce its listener", func() (bool, error) {
		log, _, lerr := d.logs(ctx, r)
		if announced := lerr == nil && len(log.Ready) > 0; !announced {
			return false, nil
		}
		pods, err := d.runPods(ctx, r)
		if err != nil {
			return false, err
		}
		for _, p := range pods {
			if p.JobID == isolationprobe.ProbeJob && p.Status.PodIP != "" && p.Status.HostIP != "" {
				facts = peerFacts{Label: r.Label, Team: r.Team, RunID: r.RunID, PodIP: p.Status.PodIP, HostIP: p.Status.HostIP, ListenPort: log.Ready[0].ListenPort, Canary: canaryName(r)}
				return true, nil
			}
		}
		return false, nil
	})
	return facts, err
}

func (d *isolationDriver) awaitSummary(ctx context.Context, r *isolationRun) error {
	return d.poll(ctx, r, probeTimeout, "report its results", func() (bool, error) {
		log, _, err := d.logs(ctx, r)
		return err == nil && len(log.Summaries) > 0, nil
	})
}

func (d *isolationDriver) awaitEnd(ctx context.Context, r *isolationRun, budget time.Duration) error {
	return d.poll(ctx, r, budget, "finish", func() (bool, error) { return terminal(r.Status), nil })
}

type runPod struct {
	kubePod
	JobID string
}

func probeRunning(pods []runPod) bool {
	for _, p := range pods {
		if p.JobID == isolationprobe.ProbeJob && p.Status.Phase == "Running" {
			return true
		}
	}
	return false
}

// safety: the launcher's award binding ties each Kubernetes Job to one run and job, and pods name
// their Job by label, so a pod counts for a run only through both.
func podsOfRun(jobs []kubeJob, pods []kubePod, runID string) []runPod {
	jobID := map[string]string{}
	for _, j := range jobs {
		var b awardBinding
		if json.Unmarshal([]byte(j.Metadata.Annotations[awardBindingAnnotation]), &b) == nil && b.RunID == runID {
			jobID[j.Metadata.Name] = b.NodeID
		}
	}
	var out []runPod
	for _, p := range pods {
		if id, ok := jobID[p.Metadata.Labels[jobPodLabel]]; ok {
			out = append(out, runPod{kubePod: p, JobID: id})
		}
	}
	return out
}

func (d *isolationDriver) runPods(ctx context.Context, r *isolationRun) ([]runPod, error) {
	jobsOut, err := d.kubectl(ctx, "get", "jobs", "-n", d.args.JobNamespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	podsOut, err := d.kubectl(ctx, "get", "pods", "-n", d.args.JobNamespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	jobs, err := decodeList[kubeJob](jobsOut)
	if err != nil {
		return nil, err
	}
	pods, err := decodeList[kubePod](podsOut)
	if err != nil {
		return nil, err
	}
	return podsOfRun(jobs, pods, r.RunID), nil
}

func (d *isolationDriver) capture(ctx context.Context) error {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"networkpolicies", []string{"get", "networkpolicies", "-n", d.args.JobNamespace}},
		{"admission-bindings", []string{"get", "validatingadmissionpolicybindings"}},
	} {
		out, err := d.kubectl(ctx, append(c.args, "-o", "json")...)
		if err != nil {
			return err
		}
		if err := d.save("cluster/"+c.name+".json", []byte(out)); err != nil {
			return err
		}
	}
	return nil
}

func (d *isolationDriver) judge(ctx context.Context, runs []*isolationRun, alive map[string]bool) error {
	nodes, err := d.nodes(ctx)
	if err != nil {
		return err
	}
	used := map[string]isolationNode{}
	records := make([]isolationRun, 0, len(runs))
	for _, r := range runs {
		log, raw, err := d.logs(ctx, r)
		if err != nil {
			return fmt.Errorf("read %s's results: %w", r.Label, err)
		}
		if err := d.save("runs/"+r.Label+"-probe.log", []byte(raw)); err != nil {
			return err
		}
		r.Results, r.Ready, r.Summary = log.Results, log.Ready, log.Summaries
		pods, err := d.runPods(ctx, r)
		if err != nil {
			return err
		}
		if err := d.saveJSON("cluster/pods-"+r.Label+".json", pods); err != nil {
			return err
		}
		r.Pods = nil
		for _, p := range pods {
			r.Pods = append(r.Pods, isolationPod{
				Name: p.Metadata.Name, Job: p.JobID, Node: p.Spec.NodeName, PodIP: p.Status.PodIP, HostIP: p.Status.HostIP,
				Phase: p.Status.Phase, Team: p.Metadata.Labels["sparkwing.dev/team"], Spec: podSpecFindings(p.kubePod),
			})
			if n, ok := nodes[p.Spec.NodeName]; ok {
				used[n.Name] = n
			}
		}
		records = append(records, *r)
	}
	eval := evaluateIsolation(d.args.JobNamespace, records, nodes, alive)
	var ndjson strings.Builder
	for _, r := range records {
		for _, res := range r.Results {
			line, err := json.Marshal(struct {
				Run string `json:"run"`
				isolationprobe.Result
			}{r.Label, res})
			if err != nil {
				return err
			}
			ndjson.Write(append(line, '\n'))
		}
	}
	if err := errors.Join(
		d.saveJSON("cluster/nodes.json", used), d.saveJSON("runs.json", records), d.saveJSON("evaluation.json", eval),
		d.save("results.ndjson", []byte(ndjson.String())), d.save("REPORT.md", []byte(isolationReport(d.args, records, eval))),
	); err != nil {
		return err
	}
	if err := d.checksums(); err != nil {
		return err
	}
	d.tools.say(ctx, "report: "+filepath.Join(d.evidence, "REPORT.md"))
	if !eval.OK {
		var failed []string
		for _, it := range eval.Items {
			if !it.OK {
				failed = append(failed, it.Item)
			}
		}
		return fmt.Errorf("cloud isolation failed %s; see %s", strings.Join(failed, ", "), filepath.Join(d.evidence, "REPORT.md"))
	}
	return nil
}

func (d *isolationDriver) nodes(ctx context.Context) (map[string]isolationNode, error) {
	out, err := d.kubectl(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}
	items, err := decodeList[kubeNode](out)
	if err != nil {
		return nil, err
	}
	nodes := map[string]isolationNode{}
	for _, n := range items {
		nodes[n.Metadata.Name] = isolationNode{
			Name: n.Metadata.Name, IPs: n.internalIPs(), TeamNode: n.Metadata.Labels[teamNodeLabel],
			UsedTeam: n.Metadata.Labels[usedTeamNodeLabel], UsedRun: n.Metadata.Labels[usedRunNodeLabel],
		}
	}
	return nodes, nil
}

func (d *isolationDriver) checksums() error {
	var lines []string
	err := filepath.WalkDir(d.evidence, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || e.Name() == "SHA256SUMS" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d.evidence, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+rel)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(lines)
	return d.save("SHA256SUMS", []byte(strings.Join(lines, "\n")+"\n"))
}

func isolationReport(args CloudIsolationArgs, runs []isolationRun, eval isolationEvaluation) string {
	var b strings.Builder
	verdict := "PASS"
	if !eval.OK {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "# Cloud isolation conformance: %s\n\n", verdict)
	fmt.Fprintf(&b, "Controller %s, job namespace %s, teams %s and %s.\n\n", args.ControllerURL, args.JobNamespace, args.TeamA, args.TeamB)
	b.WriteString("| Run | Team | Role | Run ID | Status | Probe pod | Node | Pod IP | Node IP |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range runs {
		pod := probePod(r)
		if pod == nil {
			pod = &isolationPod{}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", r.Label, r.Team, r.Role, r.RunID, r.Status, pod.Name, pod.Node, pod.PodIP, pod.HostIP)
	}
	b.WriteString("\n## Items\n\n")
	for _, it := range eval.Items {
		mark := "pass"
		if !it.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "- **%s %s: %s**\n", it.Item, it.Title, mark)
		for _, f := range it.Failures {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	b.WriteString("\n## Every probe\n\nEach line is one probe: run, context, verdict, probe, target and what happened. Outcomes never carry a value the probe read.\n")
	for _, it := range isolationItems {
		fmt.Fprintf(&b, "\n### %s %s\n\n", it.id, it.title)
		count := 0
		for _, r := range runs {
			for _, res := range r.Results {
				if itemOf(res.Item) != it.id {
					continue
				}
				count++
				fmt.Fprintf(&b, "- %s %s %s: %s", r.Label, res.Context, res.Verdict, res.Probe)
				if res.Target != "" {
					fmt.Fprintf(&b, " `%s`", res.Target)
				}
				fmt.Fprintf(&b, ": %s (%sms)\n", res.Outcome, strconv.FormatInt(res.Millis, 10))
			}
		}
		if count == 0 {
			b.WriteString("- no probe results; the item is judged from the cluster captures\n")
		}
	}
	return b.String()
}

func init() {
	sparkwing.Register[CloudIsolationArgs]("CloudIsolation", func() sparkwing.Pipeline[CloudIsolationArgs] { return &CloudIsolation{} })
}
