package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sparkwing-pipelines/isolationprobe"
	"sparkwing-pipelines/isolationprobe/fixture"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// CloudIsolationArgs names the deployment the conformance run attacks. The repository's
// sparkwing.yaml supplies staging's names, every address is either discovered from the cluster or
// passed at run time, and AnchorTimeout raises the anchors' job limit for one run.
type CloudIsolationArgs struct {
	TeamA         string `flag:"team-a" desc:"CLI profile of the first conformance team; the profile is named for its team"`
	TeamB         string `flag:"team-b" desc:"CLI profile of the second conformance team; the profile is named for its team"`
	ControllerURL string `flag:"controller-url" desc:"Controller origin the probes call without credentials; must be staging's"`
	KubeContext   string `flag:"kube-context" desc:"kubectl context with read access to the cluster"`
	ControllerNS  string `flag:"controller-namespace" desc:"Namespace of the controller, launcher and checkout service; must be staging's"`
	JobNamespace  string `flag:"job-namespace" desc:"Namespace the launcher creates Cloud Jobs in; must be staging's"`
	InfraNodePool string `flag:"infra-node-pool" desc:"Karpenter NodePool of the controller's nodes"`
	IngressNS     string `flag:"ingress-namespaces" desc:"Comma-separated namespaces of the ingress proxies in front of the controller"`
	Database      string `flag:"database" desc:"Controller database endpoint as host:port"`
	VPCResolver   string `flag:"vpc-resolver" desc:"The VPC's Route 53 Resolver address, passed at run time so it stays out of the repository"`
	PublicContext string `flag:"public-api-context" desc:"kubeconfig context whose server is the cluster's public API endpoint"`
	AnchorTimeout string `flag:"anchor-timeout" desc:"Time limit of each anchor's probe job as a Go duration, from 20m to 1h; raise it when an anchor ends before the last run has probed it" default:"20m"`
	Evidence      string `flag:"evidence" desc:"Directory for the report and captures; a new directory under the system temporary directory when empty"`
}

// CloudIsolation attacks Cloud execution isolation from two teams and judges every refusal.
type CloudIsolation struct{ sparkwing.Base }

// ShortHelp describes the pipeline in one line.
func (CloudIsolation) ShortHelp() string {
	return "Hostile-job conformance: two staging Cloud teams probe each other, the cluster and their nodes; run before each release"
}

// Help describes what the pipeline does and needs.
func (CloudIsolation) Help() string {
	return "Checks that both team profiles point at staging and authenticate as their own teams, then uploads the " +
		"isolation-probe fixture as each team and runs it on staging Cloud in three phases: team B's anchor, then " +
		"team A probing it and anchoring, then team B again probing both. Each phase starts only after the previous " +
		"run's probes all reported and passed. Each probe job reaches for instance metadata over IPv4 and IPv6, the " +
		"controller's pods and services, cluster DNS, the database, the kube API's private and public endpoints, the " +
		"other runs' nodes, kubelets, listeners and Docker ports, and its own node and pod; checks its mounts, " +
		"devices, sockets, canaries and user namespace; and repeats from a DinD container on Docker's bridge and from " +
		"one with every privilege the sidecar grants, which also tries host kernel files, mounts, device nodes and the " +
		"syscalls the DinD profile refuses. Probes connect and close, open kernel files without writing, and grade an " +
		"unexpected answer with anonymous reads and dry runs only. While the runs are live the driver records each " +
		"run's pods, their nodes, every pod on those nodes and the NodeClaims, then writes REPORT.md, every probe " +
		"result and the captures to the evidence directory. It fails on any breach, any probe that proved nothing, any " +
		"result the plan owed but never got, and any node shared or fenced to another run. An anchor that ends " +
		"before the last run has probed it fails the run; --anchor-timeout raises the anchors' 20-minute job " +
		"limit for that run, up to an hour. The database and VPC resolver addresses are passed at run time. It " +
		"needs sparkwing, kubectl, git and Go on PATH, two team profiles with Cloud credits, and kubectl read access."
}

// Examples shows how to run the conformance check.
func (CloudIsolation) Examples() []sparkwing.Example {
	return []sparkwing.Example{{
		Comment: "Attack staging from the two conformance teams and keep the evidence",
		Command: `sparkwing run cloud-isolation -- --database "$STAGING_DATABASE" --vpc-resolver "$STAGING_VPC_RESOLVER" --evidence ~/evidence/cloud-isolation/$(date +%F)`,
	}}
}

// Plan declares the single conformance job.
func (CloudIsolation) Plan(_ context.Context, plan *sparkwing.Plan, in CloudIsolationArgs, run sparkwing.RunContext) error {
	sparkwing.Job(plan, "conformance", &cloudIsolationJob{args: in, runID: run.RunID}).Timeout(90 * time.Minute)
	return nil
}

// safety: anchors hold their listeners until just before their job limit and the driver cancels
// them once the last run has probed them; a peer gone early fails its item. Every command carries
// its own deadline, and cleanup runs on a fresh bounded context.
const (
	anchorMargin   = 2 * time.Minute
	readyTimeout   = 15 * time.Minute
	probeTimeout   = 25 * time.Minute
	endTimeout     = 5 * time.Minute
	pollInterval   = 10 * time.Second
	commandTimeout = 2 * time.Minute
	buildTimeout   = 10 * time.Minute
	cleanupTimeout = 30 * time.Second
)

// safety: the conformance run attacks staging and nothing else, so the controller and both
// namespaces must be staging's own before any request leaves the driver.
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
		{"--infra-node-pool", in.InfraNodePool},
		{"--ingress-namespaces", in.IngressNS},
		{"--database", in.Database},
		{"--vpc-resolver", in.VPCResolver},
	} {
		if strings.TrimSpace(f.value) == "" {
			errs = append(errs, fmt.Errorf("%s is required", f.name))
		}
	}
	if in.TeamA != "" && in.TeamA == in.TeamB {
		errs = append(errs, errors.New("--team-a and --team-b must name different teams"))
	}
	staging := defaultDeployConfig().Staging
	if in.ControllerURL != "" && strings.TrimRight(in.ControllerURL, "/") != staging.ControllerURL {
		errs = append(errs, fmt.Errorf("--controller-url %s is not staging's %s; the conformance run attacks staging only", in.ControllerURL, staging.ControllerURL))
	}
	if in.ControllerNS != "" && in.ControllerNS != staging.Namespace {
		errs = append(errs, fmt.Errorf("--controller-namespace %s is not staging's %s", in.ControllerNS, staging.Namespace))
	}
	if want := staging.Namespace + "-cloud-jobs"; in.JobNamespace != "" && in.JobNamespace != want {
		errs = append(errs, fmt.Errorf("--job-namespace %s is not staging's %s", in.JobNamespace, want))
	}
	if in.Database != "" {
		if host, port, err := net.SplitHostPort(in.Database); err != nil || host == "" || port == "" {
			errs = append(errs, fmt.Errorf("--database %q must be host:port", in.Database))
		}
	}
	if in.VPCResolver != "" && net.ParseIP(in.VPCResolver) == nil {
		errs = append(errs, fmt.Errorf("--vpc-resolver %q must be an IP address", in.VPCResolver))
	}
	if _, err := in.anchorTimeout(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// safety: the anchors' limit moves only by an explicit per-run flag, from the fixture's default up
// to its cap, so a longer hostile hold is always a choice made for that run.
func (in CloudIsolationArgs) anchorTimeout() (time.Duration, error) {
	if in.AnchorTimeout == "" {
		return fixture.JobTimeout, nil
	}
	d, err := time.ParseDuration(in.AnchorTimeout)
	if err != nil {
		return 0, fmt.Errorf("--anchor-timeout: %w", err)
	}
	if d < fixture.JobTimeout || d > fixture.MaxJobTimeout {
		return 0, fmt.Errorf("--anchor-timeout %s must be from %s to %s", d, fixture.JobTimeout, fixture.MaxJobTimeout)
	}
	return d, nil
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
		d := &isolationDriver{args: j.args, repo: sparkwing.WorkDir(), tmp: tmp, evidence: evidence, session: j.runID, tools: realIsolationTools()}
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
	args          CloudIsolationArgs
	repo          string
	tmp           string
	evidence      string
	session       string
	tools         isolationTools
	runs          []*isolationRun
	controllerPod string
	seen          *placementLog
	alive         map[string]bool
	gaps          []string
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

func (d *isolationDriver) run(ctx context.Context, timeout time.Duration, c isolationCommand) (string, int, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return d.tools.run(cctx, c)
}

func (d *isolationDriver) cmdWithin(ctx context.Context, timeout time.Duration, dir string, env map[string]string, name string, args ...string) (string, error) {
	out, code, err := d.run(ctx, timeout, isolationCommand{Dir: dir, Env: env, Name: name, Args: args})
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("%s %s: exit %d: %s", name, strings.Join(args, " "), code, strings.TrimSpace(lastLines(out, 3)))
	}
	return out, nil
}

func (d *isolationDriver) cmd(ctx context.Context, dir string, env map[string]string, name string, args ...string) (string, error) {
	return d.cmdWithin(ctx, commandTimeout, dir, env, name, args...)
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
	d.seen, d.alive, d.gaps = newPlacementLog(), map[string]bool{}, nil
	if err := d.step(ctx, "check that both profiles reach staging as their own teams", func() error { return d.preflight(ctx) }); err != nil {
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
	d.runs = []*isolationRun{
		{Label: "B1", Team: d.args.TeamB, Role: isolationprobe.RoleAnchor},
		{Label: "A1", Team: d.args.TeamA, Role: isolationprobe.RoleAnchor, Peers: []string{"B1"}},
		{Label: "B2", Team: d.args.TeamB, Role: isolationprobe.RoleProbe, Peers: []string{"A1", "B1"}},
	}
	stopped := d.sequence(ctx, static, template)
	return d.finish(ctx, stopped)
}

// safety: each run starts only after the run before it reported every result it owed and none of
// them failed, so a breach or a probe that proved nothing ends the sequence before another hostile
// job starts.
func (d *isolationDriver) sequence(ctx context.Context, static []isolationprobe.Target, template string) error {
	peers := map[string]peerFacts{}
	for _, r := range d.runs {
		r.Plan = d.plan(r, static, peers)
		if err := d.saveJSON("plans/"+r.Label+".json", r.Plan); err != nil {
			return err
		}
		if err := d.step(ctx, "trigger "+r.Label+" as "+r.Team+" ("+r.Role+")", func() (err error) {
			r.RunID, err = d.trigger(ctx, template, r)
			return err
		}); err != nil {
			return err
		}
		if r.Role == isolationprobe.RoleAnchor {
			if err := d.step(ctx, "wait for "+r.Label+" to announce its listener", func() (err error) {
				peers[r.Label], err = d.awaitReady(ctx, r)
				return err
			}); err != nil {
				return err
			}
			d.statusAt(ctx, r, "ready")
		}
		if err := d.step(ctx, "wait for "+r.Label+"'s probes to report", func() error { return d.awaitSummary(ctx, r) }); err != nil {
			return err
		}
		d.statusAt(ctx, r, "probed")
		if err := d.step(ctx, "check everything "+r.Label+" owed before the next phase", func() error { return d.gate(ctx, r) }); err != nil {
			return err
		}
		if r.Role == isolationprobe.RoleProbe {
			if err := d.awaitEnd(ctx, r, endTimeout); err != nil {
				d.tools.say(ctx, err.Error())
			}
		}
	}
	return nil
}

// safety: every phase transition, the last one included, waits on everything the evidence already
// shows: the run's own results, each peer it aimed at still live, the placement in every
// observation so far and any evidence that went missing. Any problem ends the sequence before
// another hostile job starts.
func (d *isolationDriver) gate(ctx context.Context, r *isolationRun) error {
	var problems []string
	for _, label := range r.Peers {
		i := slices.IndexFunc(d.runs, func(p *isolationRun) bool { return p.Label == label })
		if i < 0 {
			continue
		}
		peer := d.runs[i]
		if err := d.status(ctx, peer); err != nil {
			return err
		}
		live := !terminal(peer.Status) && d.seen.probeRunning(label)
		if prior, ok := d.alive[label]; ok {
			live = live && prior
		}
		d.alive[label] = live
		if !live {
			problems = append(problems, fmt.Sprintf("peer %s was not running after %s probed it", label, r.Label))
		}
	}
	if err := d.checkRun(ctx, r); err != nil {
		problems = append(problems, err.Error())
	}
	records, nodes := d.records()
	placementFindings(d.args.JobNamespace, records, nodes, true, func(item, format string, args ...any) {
		problems = append(problems, item+" "+fmt.Sprintf(format, args...))
	})
	history := d.historyFailures()
	for _, item := range slices.Sorted(maps.Keys(history)) {
		for _, f := range history[item] {
			problems = append(problems, item+" "+f)
		}
	}
	problems = append(problems, d.gaps...)
	switch len(problems) {
	case 0:
		return nil
	case 1:
		return errors.New(problems[0])
	default:
		return fmt.Errorf("%s, and %d more problems", problems[0], len(problems)-1)
	}
}

func (d *isolationDriver) plan(r *isolationRun, static []isolationprobe.Target, peers map[string]peerFacts) isolationprobe.Plan {
	plan := isolationprobe.Plan{
		Team: r.Team, ControllerURL: d.args.ControllerURL, ListenPort: fixtureListenPort,
		Canary: d.canaryName(r), Targets: append([]isolationprobe.Target(nil), static...),
		JobNamespace: d.args.JobNamespace, KubeNamespaces: slices.Compact([]string{d.args.JobNamespace, d.args.ControllerNS}),
		KubePods: []isolationprobe.PodRef{{Namespace: d.args.ControllerNS, Name: d.controllerPod}},
	}
	for _, label := range r.Peers {
		p := peers[label]
		plan.PeerCanaries = append(plan.PeerCanaries, p.Canary)
		plan.PeerRuns = append(plan.PeerRuns, p.RunID)
		plan.Targets = append(plan.Targets, peerTargets(p)...)
		plan.KubePods = append(plan.KubePods, isolationprobe.PodRef{Namespace: d.args.JobNamespace, Name: p.PodName})
	}
	return plan
}

// safety: canaries carry the conformance run's own ID, so a file left by an earlier conformance run
// cannot stand in for this one's.
func (d *isolationDriver) canaryName(r *isolationRun) string {
	name := "isolation-canary-"
	if d.session != "" {
		name += d.session + "-"
	}
	return name + r.Label + "-" + r.Team
}

type profileRecord struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
}

type tokenRecord struct {
	Team string `json:"team"`
}

type preflightRecord struct {
	Profile    string   `json:"profile"`
	Controller string   `json:"controller"`
	Teams      []string `json:"teams"`
}

// safety: a profile pointed anywhere but staging, or authenticating as another team, would start
// hostile jobs where they do not belong, so both are checked before the first upload.
func (d *isolationDriver) preflight(ctx context.Context) error {
	out, err := d.sw(ctx, "profiles", "list", "-o", "json")
	if err != nil {
		return err
	}
	profiles, err := decodeRecords[profileRecord](out)
	if err != nil {
		return fmt.Errorf("profiles list: %w", err)
	}
	controllers := map[string]string{}
	for _, p := range profiles {
		controllers[p.Name] = p.Controller
	}
	var records []preflightRecord
	var errs []error
	for _, team := range []string{d.args.TeamA, d.args.TeamB} {
		controller, ok := controllers[team]
		if !ok {
			errs = append(errs, fmt.Errorf("no CLI profile named %s", team))
			continue
		}
		if strings.TrimRight(controller, "/") != strings.TrimRight(d.args.ControllerURL, "/") {
			errs = append(errs, fmt.Errorf("profile %s points at %s, not %s", team, controller, d.args.ControllerURL))
			continue
		}
		tokensOut, err := d.sw(ctx, "tokens", "list", "--profile", team, "-o", "json")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tokens, err := decodeRecords[tokenRecord](tokensOut)
		if err != nil {
			errs = append(errs, fmt.Errorf("tokens list for %s: %w", team, err))
			continue
		}
		teams := map[string]bool{}
		for _, t := range tokens {
			if t.Team != "" {
				teams[t.Team] = true
			}
		}
		names := slices.Sorted(maps.Keys(teams))
		if len(names) != 1 || names[0] != team {
			errs = append(errs, fmt.Errorf("profile %s authenticates as team %v, want %s", team, names, team))
		}
		records = append(records, preflightRecord{Profile: team, Controller: controller, Teams: names})
	}
	if err := d.saveJSON("preflight.json", records); err != nil {
		return err
	}
	return errors.Join(errs...)
}

func decodeRecords[T any](out string) ([]T, error) {
	var records []T
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var r T
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			return records, nil
		} else if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
}

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
		in          = targetInputs{Namespace: d.args.ControllerNS, Database: d.args.Database, VPCResolver: d.args.VPCResolver}
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
		load(&infra, "get", "nodes", "-l", "karpenter.sh/nodepool="+d.args.InfraNodePool),
		load(&kubeList[json.RawMessage]{}, "get", "nodeclaims"),
		load(&kubeList[json.RawMessage]{}, "get", "daemonsets", "-A"),
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
	// safety: placement is recorded through these reads while the runs are live, so a permission
	// they need fails here, before any hostile job starts.
	if len(infra.Items) > 0 {
		if err := load(&kubeList[json.RawMessage]{}, "get", "pods", "-A", "--field-selector", "spec.nodeName="+infra.Items[0].Metadata.Name); err != nil {
			return nil, err
		}
	}
	in.ControllerPods, in.ControllerServices, in.DNSPods, in.DNSServices = pods.Items, services.Items, dnsPods.Items, dnsServices.Items
	in.APIEndpoints, in.InfraNodes = endpoints.Items, infra.Items
	d.controllerPod = gradedPod(in.ControllerPods)
	return staticTargets(in)
}

// safety: the kube API grade tries to rewrite a real controller pod, so the first running pod on the
// pod network, by name, is chosen the same way on every run.
func gradedPod(pods []kubePod) string {
	var names []string
	for _, p := range pods {
		if p.Status.Phase == "Running" && !p.Spec.HostNetwork && p.Status.PodIP != "" {
			names = append(names, p.Metadata.Name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
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
	if _, err := d.cmdWithin(ctx, buildTimeout, "", nil, "sparkwing", "pipeline", "new", "--pipeline", fixture.Pipeline, "--template", "minimal", "-C", dir, "-o", "json"); err != nil {
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
		if _, err := d.cmdWithin(ctx, buildTimeout, module, goEnv, "go", args...); err != nil {
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

func (d *isolationDriver) trigger(ctx context.Context, template string, r *isolationRun) (string, error) {
	checkout := filepath.Join(d.tmp, "checkout-"+r.Label)
	if err := os.CopyFS(checkout, os.DirFS(template)); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r.Plan, "", " ")
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
	limit, hold := fixture.JobTimeout, time.Duration(0)
	if r.Role == isolationprobe.RoleAnchor {
		if limit, err = d.args.anchorTimeout(); err != nil {
			return "", err
		}
		hold = limit - anchorMargin
	}
	out, err := d.cmdWithin(ctx, buildTimeout, "", nil, "sparkwing", "pipeline", "trigger", fixture.Pipeline, "-C", checkout, "--profile", r.Team,
		"--runner", "cloud", "--working-tree", "--detach", "-o", "json", "--", "--role", r.Role, "--hold", hold.String(), "--timeout", limit.String())
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

// safety: runs status exits 3 while a run is live, so its document is read whatever the exit
// code, and only a document naming a status counts. Each document is kept as it arrived.
func (d *isolationDriver) status(ctx context.Context, r *isolationRun) error {
	out, code, err := d.run(ctx, commandTimeout, isolationCommand{Name: "sparkwing", Args: []string{"runs", "status", "--run", r.RunID, "--profile", r.Team, "--exit-zero", "-o", "json"}})
	if err != nil {
		return err
	}
	if err := d.save("runs/"+r.Label+"-status.json", []byte(out)); err != nil {
		return err
	}
	if err := d.appendFile("runs/"+r.Label+"-status.ndjson", []byte(out)); err != nil {
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

// safety: each milestone keeps the run's raw status and the placement as last observed, so every
// phase's evidence survives the launcher's teardown.
func (d *isolationDriver) statusAt(ctx context.Context, r *isolationRun, milestone string) {
	err := d.status(ctx, r)
	if err == nil {
		var data []byte
		if data, err = os.ReadFile(filepath.Join(d.evidence, "runs", r.Label+"-status.json")); err == nil {
			err = d.save("runs/"+r.Label+"-status-"+milestone+".json", data)
		}
	}
	if err == nil {
		latest := d.seen.latest()
		err = d.saveJSON("cluster/phases/"+r.Label+"-"+milestone+".json", observationRecord{Sample: len(d.seen.samples), Pods: latest.podDocs, Nodes: latest.nodeDocs, OnNode: latest.nodePods})
	}
	if err != nil {
		d.gaps = append(d.gaps, r.Label+" evidence at "+milestone+" was not kept: "+err.Error())
	}
}

func terminal(status string) bool {
	return status == "success" || status == "failed" || status == "cancelled" || status == "dry_run"
}

var errNoLogs = errors.New("run logs unavailable")

func (d *isolationDriver) logs(ctx context.Context, r *isolationRun) (isolationprobe.Log, string, error) {
	out, err := d.sw(ctx, "runs", "logs", "--run", r.RunID, "--profile", r.Team, "--job", isolationprobe.ProbeJob, "--no-events", "-o", "plain")
	if err != nil {
		return isolationprobe.Log{}, out, fmt.Errorf("%w: %w", errNoLogs, err)
	}
	log, err := isolationprobe.ParseLog(out)
	return log, out, err
}

// safety: logs that have not arrived yet are waited for, but a log that arrived and does not
// parse ends the wait, because a garbled result line must not be skipped.
func (d *isolationDriver) readLog(ctx context.Context, r *isolationRun) (isolationprobe.Log, bool, error) {
	log, _, err := d.logs(ctx, r)
	if errors.Is(err, errNoLogs) {
		return log, false, nil
	}
	return log, err == nil, err
}

func (d *isolationDriver) poll(ctx context.Context, r *isolationRun, budget time.Duration, what string, check func() (bool, error)) error {
	deadline := d.tools.now().Add(budget)
	for {
		if err := d.observe(ctx); err != nil {
			return err
		}
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
		log, ok, err := d.readLog(ctx, r)
		if err != nil || !ok || len(log.Ready) == 0 {
			return false, err
		}
		if p := d.seen.probePod(r.Label); p != nil && p.Status.PodIP != "" && p.Status.HostIP != "" {
			facts = peerFacts{Label: r.Label, Team: r.Team, RunID: r.RunID, PodName: p.Metadata.Name, PodIP: p.Status.PodIP, HostIP: p.Status.HostIP, ListenPort: log.Ready[0].ListenPort, Canary: r.Plan.Canary}
			return true, nil
		}
		return false, nil
	})
	return facts, err
}

func (d *isolationDriver) awaitSummary(ctx context.Context, r *isolationRun) error {
	return d.poll(ctx, r, probeTimeout, "report its results", func() (bool, error) {
		log, ok, err := d.readLog(ctx, r)
		return ok && len(log.Summaries) > 0, err
	})
}

func (d *isolationDriver) awaitEnd(ctx context.Context, r *isolationRun, budget time.Duration) error {
	return d.poll(ctx, r, budget, "finish", func() (bool, error) { return terminal(r.Status), nil })
}

// safety: a run's results are judged the moment they are complete, against everything its plan
// owed, so the sequence stops before the next hostile job if anything failed or went missing.
func (d *isolationDriver) checkRun(ctx context.Context, r *isolationRun) error {
	log, raw, err := d.logs(ctx, r)
	if saveErr := d.save("runs/"+r.Label+"-probe.log", []byte(raw)); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return err
	}
	r.Results, r.Ready, r.Summary = log.Results, log.Ready, log.Summaries
	findings := probeFindings(*r)
	if len(findings) == 0 {
		return nil
	}
	return fmt.Errorf("%d failed or missing checks, first: %s", len(findings), findings[0].text)
}

type runPod struct {
	kubePod
	JobID string
}

// safety: the launcher's award binding ties each Kubernetes Job to one run and job, and a pod
// counts for a run only when its controller reference is that Job's UID and its Job label agrees, so
// a label naming the Job is never enough.
func podsOfRun(jobs []kubeJob, pods []kubePod, runID string) []runPod {
	type runJob struct{ nodeID, uid string }
	byName := map[string]runJob{}
	for _, j := range jobs {
		var b awardBinding
		if json.Unmarshal([]byte(j.Metadata.Annotations[awardBindingAnnotation]), &b) == nil && b.RunID == runID && j.Metadata.UID != "" {
			byName[j.Metadata.Name] = runJob{nodeID: b.NodeID, uid: j.Metadata.UID}
		}
	}
	var out []runPod
	for _, p := range pods {
		j, named := byName[p.Metadata.Labels[jobPodLabel]]
		owner, owned := p.Metadata.controller()
		if named && owned && owner.Kind == "Job" && owner.UID == j.uid {
			out = append(out, runPod{kubePod: p, JobID: j.nodeID})
		}
	}
	return out
}

// safety: the launcher deletes a run's Jobs when the run ends, and a later observation can look
// healthy after an unsafe one, so every observation is kept exactly as seen and judged on its own.
// Nothing recorded is replaced: samples only append, and a Job's run, a DaemonSet's namespace or a
// node's NodeClaim is kept, by UID or node, from the first time it is seen.
type placementLog struct {
	samples    []placementSample
	jobRun     map[string]string
	daemonSets map[string]string
	claims     map[string]json.RawMessage
}

// safety: one sample is one poll's view of each run's pods, the nodes they use and every pod on
// those nodes, decoded for judging and raw for the evidence.
type placementSample struct {
	pods     map[string][]runPod
	podDocs  map[string][]json.RawMessage
	nodes    map[string]kubeNode
	nodeDocs map[string]json.RawMessage
	onNode   map[string][]kubePod
	nodePods map[string][]json.RawMessage
}

func newPlacementLog() *placementLog {
	return &placementLog{jobRun: map[string]string{}, daemonSets: map[string]string{}, claims: map[string]json.RawMessage{}}
}

func (l *placementLog) latest() placementSample {
	if len(l.samples) == 0 {
		return placementSample{}
	}
	return l.samples[len(l.samples)-1]
}

// safety: a pod counts for a peer's listener only while the latest observation still shows it
// running, since an earlier record of a deleted pod keeps the phase it had.
func (l *placementLog) probePod(label string) *runPod {
	for _, p := range l.latest().pods[label] {
		if p.JobID == isolationprobe.ProbeJob {
			return &p
		}
	}
	return nil
}

func (l *placementLog) probeRunning(label string) bool {
	p := l.probePod(label)
	return p != nil && p.Status.Phase == "Running"
}

func (l *placementLog) usedNodes() []string {
	used := map[string]bool{}
	for _, s := range l.samples {
		for _, pods := range s.pods {
			for _, p := range pods {
				if p.Spec.NodeName != "" {
					used[p.Spec.NodeName] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(used))
}

// safety: an occupant counts as a DaemonSet's only when its controller is a DaemonSet the cluster
// lists in the pod's namespace, and as a run's only when its controller is one of that run's Jobs,
// both by UID. Anyone able to create pods could copy a listed UID, so this trusts controller-written
// metadata, which kubeAPIProof makes the run earn; a capacity placeholder is never exempt.
func (l *placementLog) nodePod(p kubePod, jobNamespace string) nodePod {
	np := nodePod{Namespace: p.Metadata.Namespace, Name: p.Metadata.Name}
	owner, owned := p.Metadata.controller()
	switch {
	case !owned:
	case owner.Kind == "DaemonSet":
		// bug: a pod copying a listed DaemonSet's UID is exempt whatever it runs; its image and
		// command are not compared with the DaemonSet's template.
		ns, known := l.daemonSets[owner.UID]
		np.DaemonSet = known && ns == p.Metadata.Namespace
	case owner.Kind == "Job" && p.Metadata.Namespace == jobNamespace:
		// bug: a pod copying a run's Job UID without the Job label is exempt here but skipped by
		// podsOfRun, so its spec is never checked; membership is decided in two places.
		np.Run = l.jobRun[owner.UID]
	}
	return np
}

func decodeRawList[T any](raw string) ([]T, []json.RawMessage, error) {
	var l kubeList[json.RawMessage]
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return nil, nil, err
	}
	items := make([]T, len(l.Items))
	for i, item := range l.Items {
		if err := json.Unmarshal(item, &items[i]); err != nil {
			return nil, nil, err
		}
	}
	return items, l.Items, nil
}

// safety: list calls never fail for an object that is gone, so an observation after a node or Job
// is deleted simply shows it absent, and the earlier samples still hold it.
func (d *isolationDriver) observe(ctx context.Context) error {
	jobsOut, err := d.kubectl(ctx, "get", "jobs", "-n", d.args.JobNamespace, "-o", "json")
	if err != nil {
		return err
	}
	podsOut, err := d.kubectl(ctx, "get", "pods", "-n", d.args.JobNamespace, "-o", "json")
	if err != nil {
		return err
	}
	jobs, err := decodeList[kubeJob](jobsOut)
	if err != nil {
		return err
	}
	pods, podDocs, err := decodeRawList[kubePod](podsOut)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		var b awardBinding
		if _, known := d.seen.jobRun[j.Metadata.UID]; !known && j.Metadata.UID != "" && json.Unmarshal([]byte(j.Metadata.Annotations[awardBindingAnnotation]), &b) == nil && b.RunID != "" {
			d.seen.jobRun[j.Metadata.UID] = b.RunID
		}
	}
	doc := map[string]json.RawMessage{}
	for i, p := range pods {
		doc[p.Metadata.Name] = podDocs[i]
	}
	sample := placementSample{pods: map[string][]runPod{}, podDocs: map[string][]json.RawMessage{}}
	for _, r := range d.runs {
		if r.RunID == "" {
			continue
		}
		for _, p := range podsOfRun(jobs, pods, r.RunID) {
			sample.pods[r.Label] = append(sample.pods[r.Label], p)
			sample.podDocs[r.Label] = append(sample.podDocs[r.Label], doc[p.Metadata.Name])
		}
	}
	d.seen.samples = append(d.seen.samples, sample)
	if used := d.seen.usedNodes(); len(used) > 0 {
		if err := d.observeNodes(ctx, used, &d.seen.samples[len(d.seen.samples)-1]); err != nil {
			return err
		}
	}
	return d.appendObservation(d.seen.latest())
}

func (d *isolationDriver) observeNodes(ctx context.Context, used []string, sample *placementSample) error {
	nodesOut, err := d.kubectl(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	nodes, nodeDocs, err := decodeRawList[kubeNode](nodesOut)
	if err != nil {
		return err
	}
	dsOut, err := d.kubectl(ctx, "get", "daemonsets", "-A", "-o", "json")
	if err != nil {
		return err
	}
	daemonSets, err := decodeList[kubeDaemonSet](dsOut)
	if err != nil {
		return err
	}
	for _, ds := range daemonSets {
		if _, known := d.seen.daemonSets[ds.Metadata.UID]; !known && ds.Metadata.UID != "" {
			d.seen.daemonSets[ds.Metadata.UID] = ds.Metadata.Namespace
		}
	}
	sample.nodes, sample.nodeDocs = map[string]kubeNode{}, map[string]json.RawMessage{}
	sample.onNode, sample.nodePods = map[string][]kubePod{}, map[string][]json.RawMessage{}
	for i, n := range nodes {
		if slices.Contains(used, n.Metadata.Name) {
			sample.nodes[n.Metadata.Name], sample.nodeDocs[n.Metadata.Name] = n, nodeDocs[i]
		}
	}
	for _, name := range used {
		out, err := d.kubectl(ctx, "get", "pods", "-A", "--field-selector", "spec.nodeName="+name, "-o", "json")
		if err != nil {
			return err
		}
		pods, docs, err := decodeRawList[kubePod](out)
		if err != nil {
			return err
		}
		sample.onNode[name], sample.nodePods[name] = pods, docs
	}
	missing := slices.ContainsFunc(used, func(name string) bool { return d.seen.claims[name] == nil })
	if !missing {
		return nil
	}
	claimsOut, err := d.kubectl(ctx, "get", "nodeclaims", "-o", "json")
	if err != nil {
		return err
	}
	claims, claimDocs, err := decodeRawList[kubeNodeClaim](claimsOut)
	if err != nil {
		return err
	}
	for i, c := range claims {
		if slices.Contains(used, c.Status.NodeName) && d.seen.claims[c.Status.NodeName] == nil {
			d.seen.claims[c.Status.NodeName] = claimDocs[i]
		}
	}
	return nil
}

type observationRecord struct {
	Sample int                          `json:"sample"`
	Pods   map[string][]json.RawMessage `json:"pods"`
	Nodes  map[string]json.RawMessage   `json:"nodes,omitempty"`
	OnNode map[string][]json.RawMessage `json:"pods_on_node,omitempty"`
}

// safety: the evidence file only grows, one line per observation, so no later sample can replace
// the record of an earlier one.
func (d *isolationDriver) appendObservation(s placementSample) error {
	line, err := json.Marshal(observationRecord{Sample: len(d.seen.samples), Pods: s.podDocs, Nodes: s.nodeDocs, OnNode: s.nodePods})
	if err != nil {
		return err
	}
	return d.appendFile("cluster/observations.ndjson", append(line, '\n'))
}

func (d *isolationDriver) appendFile(name string, data []byte) error {
	path := filepath.Join(d.evidence, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	return errors.Join(werr, f.Close())
}

type kubeDaemonSet struct {
	Metadata kubeMeta `json:"metadata"`
}

type kubeNodeClaim struct {
	Metadata kubeMeta `json:"metadata"`
	Status   struct {
		NodeName string `json:"nodeName"`
	} `json:"status"`
}

// safety: the end of the sequence still captures the cluster, cancels every live run on a fresh
// bounded context and writes the report, so a stopped or failed sequence leaves the same evidence
// as a passing one.
func (d *isolationDriver) finish(ctx context.Context, stopped error) error {
	cleanup := context.WithoutCancel(ctx)
	if err := d.observe(cleanup); err != nil {
		d.gaps = append(d.gaps, "final placement observation: "+err.Error())
	}
	for _, c := range []struct {
		name string
		args []string
	}{
		{"networkpolicies", []string{"get", "networkpolicies", "-n", d.args.JobNamespace}},
		{"admission-bindings", []string{"get", "validatingadmissionpolicybindings"}},
	} {
		out, err := d.kubectl(cleanup, append(c.args, "-o", "json")...)
		if err == nil {
			err = d.save("cluster/"+c.name+".json", []byte(out))
		}
		if err != nil {
			d.gaps = append(d.gaps, "capture "+c.name+": "+err.Error())
		}
	}
	for _, r := range d.runs {
		if r.RunID == "" {
			continue
		}
		if !terminal(r.Status) {
			if _, err := d.cmdWithin(cleanup, cleanupTimeout, "", nil, "sparkwing", "runs", "cancel", "--run", r.RunID, "--profile", r.Team, "-o", "json"); err != nil {
				d.gaps = append(d.gaps, "cancel "+r.Label+": "+err.Error())
			}
			if err := d.awaitEnd(cleanup, r, endTimeout); err != nil {
				d.gaps = append(d.gaps, err.Error())
			}
		}
		d.statusAt(cleanup, r, "end")
		if log, raw, err := d.logs(cleanup, r); err == nil {
			r.Results, r.Ready, r.Summary = log.Results, log.Ready, log.Summaries
			if err := d.save("runs/"+r.Label+"-probe.log", []byte(raw)); err != nil {
				d.gaps = append(d.gaps, "save "+r.Label+" log: "+err.Error())
			}
		} else if r.Summary == nil {
			d.gaps = append(d.gaps, "read "+r.Label+" log: "+err.Error())
		}
	}
	return d.judge(ctx, stopped)
}

func isolationPodOf(p runPod) isolationPod {
	return isolationPod{
		Name: p.Metadata.Name, Job: p.JobID, Node: p.Spec.NodeName, PodIP: p.Status.PodIP, HostIP: p.Status.HostIP,
		Phase: p.Status.Phase, Team: p.Metadata.Labels["sparkwing.dev/team"], Spec: podSpecFindings(p.kubePod),
	}
}

func (d *isolationDriver) isolationNodeOf(n kubeNode, pods []kubePod) isolationNode {
	node := isolationNode{
		Name: n.Metadata.Name, IPs: n.internalIPs(), TeamNode: n.Metadata.Labels[teamNodeLabel], UsedTeam: n.Metadata.Labels[usedTeamNodeLabel],
		UsedRun: n.Metadata.Labels[usedRunNodeLabel], Pool: n.Metadata.Labels[poolNamespaceLabel], NodePool: n.Metadata.Labels[nodePoolLabel],
		NodeClass: n.Metadata.Labels[nodeClassLabel], Claimed: d.seen.claims[n.Metadata.Name] != nil,
	}
	for _, p := range pods {
		node.Pods = append(node.Pods, d.seen.nodePod(p, d.args.JobNamespace))
	}
	return node
}

// safety: one observation is judged alone, with each started run's pods as they were then and only
// the nodes and occupants that observation caught.
func (d *isolationDriver) sampleState(s placementSample) ([]isolationRun, map[string]isolationNode) {
	runs := make([]isolationRun, 0, len(d.runs))
	for _, r := range d.runs {
		rec := isolationRun{Label: r.Label, Team: r.Team, RunID: r.RunID}
		for _, p := range s.pods[r.Label] {
			rec.Pods = append(rec.Pods, isolationPodOf(p))
		}
		runs = append(runs, rec)
	}
	nodes := map[string]isolationNode{}
	for name, n := range s.nodes {
		nodes[name] = d.isolationNodeOf(n, s.onNode[name])
	}
	return runs, nodes
}

// safety: the whole record judges each run's pods as last seen, each node as last seen and every
// pod ever seen on a node; the per-sample history judges everything earlier.
func (d *isolationDriver) records() ([]isolationRun, map[string]isolationNode) {
	pods := map[string]map[string]runPod{}
	nodes := map[string]kubeNode{}
	occupants := map[string]map[string]kubePod{}
	for _, s := range d.seen.samples {
		for label, ps := range s.pods {
			if pods[label] == nil {
				pods[label] = map[string]runPod{}
			}
			for _, p := range ps {
				pods[label][p.Metadata.Name] = p
			}
		}
		for name, n := range s.nodes {
			nodes[name] = n
		}
		for name, ps := range s.onNode {
			if occupants[name] == nil {
				occupants[name] = map[string]kubePod{}
			}
			for _, p := range ps {
				occupants[name][p.Metadata.Namespace+"/"+p.Metadata.Name] = p
			}
		}
	}
	records := make([]isolationRun, 0, len(d.runs))
	for _, r := range d.runs {
		rec := *r
		rec.Pods = nil
		for _, name := range slices.Sorted(maps.Keys(pods[r.Label])) {
			rec.Pods = append(rec.Pods, isolationPodOf(pods[r.Label][name]))
		}
		records = append(records, rec)
	}
	judged := map[string]isolationNode{}
	for name, n := range nodes {
		var ps []kubePod
		for _, key := range slices.Sorted(maps.Keys(occupants[name])) {
			ps = append(ps, occupants[name][key])
		}
		judged[name] = d.isolationNodeOf(n, ps)
	}
	return records, judged
}

// safety: every observation is judged as it was seen, so an unsafe fence, pod spec or occupant
// stays a failure even when a later observation looks healthy.
func (d *isolationDriver) historyFailures() map[string][]string {
	fails := map[string][]string{}
	for i, s := range d.seen.samples {
		runs, nodes := d.sampleState(s)
		placementFindings(d.args.JobNamespace, runs, nodes, false, func(item, format string, args ...any) {
			fails[item] = append(fails[item], fmt.Sprintf("observation %d: ", i+1)+fmt.Sprintf(format, args...))
		})
	}
	return fails
}

func (d *isolationDriver) judge(ctx context.Context, stopped error) error {
	records, nodes := d.records()
	eval := withFailures(evaluateIsolation(d.args.JobNamespace, records, nodes, d.alive), d.historyFailures())
	if stopped != nil {
		d.gaps = append(d.gaps, "the sequence stopped: "+stopped.Error())
	}
	if len(d.gaps) > 0 {
		eval = withFailures(eval, map[string][]string{"C13": d.gaps})
	}
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
	errs := []error{
		d.saveJSON("runs.json", records), d.saveJSON("evaluation.json", eval), d.saveJSON("cluster/nodes.json", nodes),
		d.save("results.ndjson", []byte(ndjson.String())), d.save("REPORT.md", []byte(isolationReport(d.args, records, eval, d.gaps))),
	}
	errs = append(errs, d.saveJSON("cluster/nodeclaims.json", sortedDocs(d.seen.claims)))
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if err := d.checksums(); err != nil {
		return err
	}
	report := filepath.Join(d.evidence, "REPORT.md")
	d.tools.say(ctx, "report: "+report)
	if !eval.OK {
		var failed []string
		for _, it := range eval.Items {
			if !it.OK {
				failed = append(failed, it.Item)
			}
		}
		return fmt.Errorf("cloud isolation failed %s; see %s", strings.Join(failed, ", "), report)
	}
	return nil
}

func sortedDocs(docs map[string]json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(docs))
	for _, k := range slices.Sorted(maps.Keys(docs)) {
		out = append(out, docs[k])
	}
	return out
}

// safety: failures found outside the final record, in earlier observations or in evidence that went
// missing, join the items they undermine, so neither ever reads as a pass.
func withFailures(eval isolationEvaluation, extra map[string][]string) isolationEvaluation {
	fails := map[string][]string{}
	for _, it := range eval.Items {
		fails[it.Item] = append(fails[it.Item], it.Failures...)
	}
	for item, f := range extra {
		fails[itemOf(item)] = append(fails[itemOf(item)], f...)
	}
	return verdict(fails)
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

// safety: no surface the run can reach makes these checks, so every report lists them as gaps.
var staticGaps = []string{
	"Each team's credit balance has no CLI surface, so funding is checked only by the runs starting.",
}

func isolationReport(args CloudIsolationArgs, runs []isolationRun, eval isolationEvaluation, gaps []string) string {
	var b strings.Builder
	verdict := "PASS"
	if !eval.OK {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "# Cloud isolation conformance: %s\n\n", verdict)
	limit := args.AnchorTimeout
	if d, err := args.anchorTimeout(); err == nil {
		limit = d.String()
	}
	fmt.Fprintf(&b, "Controller %s, job namespace %s, teams %s and %s, anchor job limit %s. Every result is in results.ndjson.\n\n", args.ControllerURL, args.JobNamespace, args.TeamA, args.TeamB, limit)
	if trusted, reason := kubeAPIProof(runs); trusted {
		b.WriteString("Placement trusts the owner references that cluster controllers write on each pod. That trust holds only " +
			"because the kube API grade passed in every run here. From inside each hostile job, anonymously and with every " +
			"credential the job found, every dry-run pod create in the job and controller namespaces and every dry-run patch " +
			"and update of a pod's owner references and annotations was refused, or the API was out of the job's reach.\n\n")
	} else {
		fmt.Fprintf(&b, "**Placement is UNTRUSTED:** %s. The placement checks trust owner references that cluster controllers "+
			"write, and that trust holds only when the kube API grade passes in the same run, so C2 fails.\n\n", reason)
	}
	b.WriteString("| Run | Team | Role | Run ID | Status | Probe pod | Node | Pod IP | Node IP |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range runs {
		pod := probePod(r)
		if pod == nil {
			pod = &isolationPod{}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", r.Label, r.Team, r.Role, r.RunID, r.Status, pod.Name, pod.Node, pod.PodIP, pod.HostIP)
	}
	b.WriteString("\n## Items\n\n| Item | Verdict | pass | BREACH | INCONCLUSIVE | observed | not-probed |\n|---|---|---|---|---|---|---|\n")
	counts := map[string]map[isolationprobe.Verdict]int{}
	for _, r := range runs {
		for _, res := range r.Results {
			item := itemOf(res.Item)
			if counts[item] == nil {
				counts[item] = map[isolationprobe.Verdict]int{}
			}
			counts[item][res.Verdict]++
		}
	}
	for _, it := range eval.Items {
		mark := "pass"
		if !it.OK {
			mark = "FAIL"
		}
		c := counts[it.Item]
		fmt.Fprintf(&b, "| %s %s | %s | %d | %d | %d | %d | %d |\n", it.Item, it.Title, mark,
			c[isolationprobe.Pass], c[isolationprobe.Breach], c[isolationprobe.Inconclusive], c[isolationprobe.Observed], c[isolationprobe.NotProbed])
	}
	b.WriteString("\n## Failures\n\n")
	failed := false
	for _, it := range eval.Items {
		for _, f := range it.Failures {
			failed = true
			fmt.Fprintf(&b, "- **%s** %s\n", it.Item, f)
		}
	}
	if !failed {
		b.WriteString("None.\n")
	}
	b.WriteString("\n## Gaps (not-probed)\n\nThese vectors were not tested; none of them counts as a pass.\n\n")
	for _, g := range staticGaps {
		fmt.Fprintf(&b, "- %s\n", g)
	}
	for _, r := range runs {
		for _, res := range r.Results {
			if res.Verdict == isolationprobe.NotProbed {
				fmt.Fprintf(&b, "- %s %s: %s %s: %s\n", r.Label, res.Context, res.Probe, res.Target, res.Outcome)
			}
		}
	}
	for _, g := range gaps {
		fmt.Fprintf(&b, "- %s\n", g)
	}
	return b.String()
}

func init() {
	sparkwing.Register[CloudIsolationArgs]("CloudIsolation", func() sparkwing.Pipeline[CloudIsolationArgs] { return &CloudIsolation{} })
}
