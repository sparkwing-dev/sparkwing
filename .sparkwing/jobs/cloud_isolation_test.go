package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sparkwing-pipelines/isolationprobe"
	"sparkwing-pipelines/isolationprobe/fixture"
	"strings"
	"sync"
	"testing"
	"time"
)

const isoJobNS = "sparkwing-staging-cloud-jobs"

func TestCloudIsolationPipelineIsRegisteredAndBounded(t *testing.T) {
	plan := inspectPipeline(t, "CloudIsolation", nil)
	if len(plan.Jobs) != 1 || plan.Jobs[0].ID != "conformance" {
		t.Fatalf("jobs = %v, want [conformance]", nodeIDs(plan.Jobs))
	}
	if got := time.Duration(plan.Jobs[0].TimeoutMS) * time.Millisecond; got != 90*time.Minute {
		t.Fatalf("timeout = %s, want 90m", got)
	}
}

func isoArgs() CloudIsolationArgs {
	return CloudIsolationArgs{
		TeamA: "stg-iso-a", TeamB: "stg-iso-b", ControllerURL: "https://staging.sparkwing.dev", KubeContext: "staging-read",
		PublicContext: "staging-read", ControllerNS: "sparkwing-staging", JobNamespace: isoJobNS, InfraNodePool: "infra-pool",
		IngressNS: "ingress", VPCResolver: "10.9.0.2", Database: "db.example.invalid:5432",
	}
}

// The driver attacks staging and nothing else, and refuses a deployment it could not attack whole.
func TestCloudIsolationArgsRefuseAnythingButAllOfStaging(t *testing.T) {
	if err := isoArgs().validate(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		edit func(*CloudIsolationArgs)
		want string
	}{
		"one team":                 {func(a *CloudIsolationArgs) { a.TeamB = a.TeamA }, "different teams"},
		"production controller":    {func(a *CloudIsolationArgs) { a.ControllerURL = "https://api.sparkwing.dev" }, "staging only"},
		"production namespace":     {func(a *CloudIsolationArgs) { a.ControllerNS = "sparkwing" }, "--controller-namespace sparkwing is not staging's"},
		"production job namespace": {func(a *CloudIsolationArgs) { a.JobNamespace = "sparkwing-cloud-jobs" }, "--job-namespace sparkwing-cloud-jobs is not staging's"},
		"no database":              {func(a *CloudIsolationArgs) { a.Database = "" }, "--database is required"},
		"database without a port":  {func(a *CloudIsolationArgs) { a.Database = "db.example.invalid" }, "host:port"},
		"no resolver":              {func(a *CloudIsolationArgs) { a.VPCResolver = "" }, "--vpc-resolver is required"},
		"resolver by name":         {func(a *CloudIsolationArgs) { a.VPCResolver = "resolver.example.invalid" }, "IP address"},
		"no ingress":               {func(a *CloudIsolationArgs) { a.IngressNS = "" }, "--ingress-namespaces is required"},
		"no infra pool":            {func(a *CloudIsolationArgs) { a.InfraNodePool = "" }, "--infra-node-pool is required"},
		"no endpoint":              {func(a *CloudIsolationArgs) { a.PublicContext = "" }, "--public-api-context is required"},
		"anchor limit lowered":     {func(a *CloudIsolationArgs) { a.AnchorTimeout = "10m" }, "--anchor-timeout 10m0s must be from 20m0s to 1h0m0s"},
		"anchor limit past cap":    {func(a *CloudIsolationArgs) { a.AnchorTimeout = "2h" }, "--anchor-timeout 2h0m0s must be from"},
		"anchor limit by name":     {func(a *CloudIsolationArgs) { a.AnchorTimeout = "longer" }, "--anchor-timeout:"},
	} {
		args := isoArgs()
		c.edit(&args)
		if err := args.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The launcher's node label for a team, as staging's operator command reported it for the two
// conformance teams.
func TestTeamNodeValueMatchesTheLaunchersLabel(t *testing.T) {
	for team, want := range map[string]string{
		"stg-iso-a": "sha256-12b4ca1d7a17c11fa74f8e92198fd45757748a55",
		"stg-iso-b": "sha256-0ff2918b8bbd7a07e62061e0acf41e3d342ed64d",
	} {
		if got := teamNodeValue(isoJobNS, team); got != want {
			t.Errorf("%s: %s, want %s", team, got, want)
		}
	}
}

// The launcher fences a node to one run with "run-" and the first 20 bytes of the SHA-256 of
// namespace/run; these vectors pin that formula.
func TestRunNodeValueMatchesTheLaunchersFence(t *testing.T) {
	for run, want := range map[string]string{
		"run-1": "run-3e21626e7741180f09c7ffefeab3e8d92081cb54",
		"run-2": "run-b4ed4e149c8ebf2540e643f3a33cc2a6d2f69537",
	} {
		if got := runNodeValue(isoJobNS, run); got != want {
			t.Errorf("%s: %s, want %s", run, got, want)
		}
	}
}

func isoPod(name, ip string, ready bool, ports ...int) kubePod {
	var p kubePod
	p.Metadata.Name = name
	p.Status.PodIP, p.Status.Phase = ip, "Running"
	status := "False"
	if ready {
		status = "True"
	}
	p.Status.Conditions = append(p.Status.Conditions, struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}{"Ready", status})
	var c kubeContainer
	for _, port := range ports {
		c.Ports = append(c.Ports, struct {
			ContainerPort int    `json:"containerPort"`
			Protocol      string `json:"protocol"`
		}{port, "TCP"})
	}
	p.Spec.Containers = []kubeContainer{c}
	return p
}

func isoService(name, ip string, ports ...int) kubeService {
	var s kubeService
	s.Metadata.Name, s.Spec.ClusterIP = name, ip
	for _, port := range ports {
		s.Spec.Ports = append(s.Spec.Ports, struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		}{port, "TCP"})
	}
	return s
}

func isoNode(name string, ips ...string) kubeNode {
	var n kubeNode
	n.Metadata.Name = name
	for _, ip := range ips {
		n.Status.Addresses = append(n.Status.Addresses, struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		}{"InternalIP", ip})
	}
	return n
}

func isoInputs() targetInputs {
	var slice kubeEndpointSlice
	slice.Endpoints = append(slice.Endpoints, struct {
		Addresses []string `json:"addresses"`
	}{[]string{"10.9.12.12"}}, struct {
		Addresses []string `json:"addresses"`
	}{[]string{"10.9.12.13"}})
	return targetInputs{
		Namespace:          "sparkwing-staging",
		ControllerPods:     []kubePod{isoPod("controller", "10.9.10.10", true, 4343, 9091), isoPod("launcher", "10.9.10.11", false, 8080)},
		ControllerServices: []kubeService{isoService("controller", "172.30.10.1", 80)},
		DNSPods:            []kubePod{isoPod("coredns", "10.9.11.11", true, 53, 9153)},
		DNSServices:        []kubeService{isoService("kube-dns", "172.30.0.10", 53, 9153)},
		APIService:         isoService("kubernetes", "172.30.0.1", 443),
		APIEndpoints:       []kubeEndpointSlice{slice},
		InfraNodes:         []kubeNode{isoNode("infra-node-1", "10.9.13.13")},
		IngressPods:        []kubePod{isoPod("ingress", "10.9.14.14", true, 8000)},
		PublicAPI:          "api.example.invalid",
		Database:           "db.example.invalid:5432",
		VPCResolver:        "10.9.0.2",
	}
}

func TestStaticTargetsCoverEveryItemFromTheCluster(t *testing.T) {
	targets, err := staticTargets(isoInputs())
	if err != nil {
		t.Fatal(err)
	}
	plan := isolationprobe.Plan{
		ControllerURL: "https://staging.sparkwing.dev", ListenPort: 18080, Canary: "c", Targets: targets,
		JobNamespace: isoJobNS, KubeNamespaces: []string{isoJobNS, "sparkwing-staging"},
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	byAddr := map[string]isolationprobe.Target{}
	for _, tg := range targets {
		byAddr[tg.Address] = tg
	}
	kube := func(expect isolationprobe.Expect) isolationprobe.Target {
		return isolationprobe.Target{Item: "C7", Expect: expect, Listening: true, Grade: isolationprobe.GradeKubeAPI, Namespace: "sparkwing-staging"}
	}
	for addr, want := range map[string]isolationprobe.Target{
		"10.9.10.10:4343":         {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.9.10.11:8080":         {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: false},
		"172.30.10.1:80":          {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.9.11.11:9153":         {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"172.30.0.10:9153":        {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.9.13.13:10250":        {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true, Grade: isolationprobe.GradeKubelet},
		"10.9.13.13:10249":        {Item: "C6", Expect: isolationprobe.ExpectBlocked},
		"10.9.14.14:8000":         {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"db.example.invalid:5432": {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.9.0.2:53":             {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.100.0.1:443":          {Item: "C6", Expect: isolationprobe.ExpectObserved},
		"169.254.169.254:80":      {Item: "C4", Expect: isolationprobe.ExpectBlocked, Listening: true, Grade: isolationprobe.GradeIMDS},
		"[fd00:ec2::254]:80":      {Item: "C5", Expect: isolationprobe.ExpectBlocked, Grade: isolationprobe.GradeIMDS},
		"172.30.0.1:443":          kube(isolationprobe.ExpectBlocked),
		"10.9.12.13:443":          kube(isolationprobe.ExpectBlocked),
		"api.example.invalid:443": kube(isolationprobe.ExpectDenied),
	} {
		got, ok := byAddr[addr]
		if !ok || got.Item != want.Item || got.Expect != want.Expect || got.Listening != want.Listening || got.Grade != want.Grade || got.Namespace != want.Namespace {
			t.Errorf("%s = %+v, want %+v", addr, got, want)
		}
	}
	if _, ok := byAddr["10.9.11.11:53"]; ok {
		t.Error("cluster DNS's own port 53 is a target; the fence allows it")
	}
}

// Discovery that finds no members of a group the fence must hide fails before any run starts,
// rather than passing C6 or C7 without probing that group.
func TestStaticTargetsRefuseAMissingGroup(t *testing.T) {
	for name, edit := range map[string]func(*targetInputs){
		"controller pods":     func(in *targetInputs) { in.ControllerPods = nil },
		"controller services": func(in *targetInputs) { in.ControllerServices = nil },
		"DNS pods":            func(in *targetInputs) { in.DNSPods = nil },
		"DNS services":        func(in *targetInputs) { in.DNSServices = nil },
		"ingress":             func(in *targetInputs) { in.IngressPods = nil },
		"infra nodes":         func(in *targetInputs) { in.InfraNodes = nil },
		"database":            func(in *targetInputs) { in.Database = "" },
		"resolver":            func(in *targetInputs) { in.VPCResolver = "" },
		"API endpoints":       func(in *targetInputs) { in.APIEndpoints = nil },
		"public API":          func(in *targetInputs) { in.PublicAPI = "" },
		"kube namespace":      func(in *targetInputs) { in.Namespace = "" },
	} {
		in := isoInputs()
		edit(&in)
		if targets, err := staticTargets(in); err == nil {
			t.Errorf("%s missing: discovery passed with %d targets", name, len(targets))
		}
	}
}

func TestContextServerHostReadsTheRedactedKubeconfig(t *testing.T) {
	config := `{"contexts":[{"name":"staging-read","context":{"cluster":"arn:aws:eks:us-west-2:1:cluster/example","user":"u"}},{"name":"proxy","context":{"cluster":"proxy"}}],
"clusters":[{"name":"arn:aws:eks:us-west-2:1:cluster/example","cluster":{"server":"https://ABC.gr7.us-west-2.eks.amazonaws.com","certificate-authority-data":"DATA+OMITTED"}},{"name":"proxy","cluster":{"server":"https://proxy.example.invalid"}}]}`
	if host, err := contextServerHost(config, "staging-read"); err != nil || host != "ABC.gr7.us-west-2.eks.amazonaws.com" {
		t.Fatalf("host = %q, %v", host, err)
	}
	if _, err := contextServerHost(config, "missing"); err == nil {
		t.Fatal("an unknown context named a host")
	}
}

func isoJob(name, run, job, team string) kubeJob {
	var j kubeJob
	j.Metadata.Name, j.Metadata.UID = name, "uid-"+name
	binding, _ := json.Marshal(awardBinding{Kind: "work", Team: team, RunID: run, NodeID: job})
	j.Metadata.Annotations = map[string]string{awardBindingAnnotation: string(binding)}
	return j
}

func controlledBy(kind, uid string) []kubeOwner {
	controller := true
	return []kubeOwner{{Kind: kind, UID: uid, Controller: &controller}}
}

// A pod belongs to a run only through its controller reference to one of the run's Jobs, with its
// Job label agreeing; the label alone, or an owner reference to another object, is not enough.
func TestPodsOfRunFollowsTheAwardBinding(t *testing.T) {
	jobs := []kubeJob{isoJob("sw-1", "run-1", "probe", "stg-iso-a"), isoJob("sw-2", "run-1", "output-sibling", "stg-iso-a"), isoJob("sw-3", "run-2", "probe", "stg-iso-b")}
	var pods []kubePod
	for i, c := range []struct{ label, owner string }{
		{"sw-1", "uid-sw-1"},
		{"sw-2", "uid-sw-2"},
		{"sw-3", "uid-sw-3"},
		{"sw-unrelated", "uid-sw-unrelated"},
		{"sw-1", ""},
		{"sw-1", "uid-sw-3"},
	} {
		p := isoPod(fmt.Sprintf("pod-%d", i), fmt.Sprintf("10.9.0.%d", i), true)
		p.Metadata.Labels = map[string]string{jobPodLabel: c.label}
		if c.owner != "" {
			p.Metadata.OwnerReferences = controlledBy("Job", c.owner)
		}
		pods = append(pods, p)
	}
	got := podsOfRun(jobs, pods, "run-1")
	if len(got) != 2 || got[0].Metadata.Name != "pod-0" || got[0].JobID != "probe" || got[1].JobID != "output-sibling" {
		t.Fatalf("run-1 pods = %+v, want only the two pods each run-1 Job controls", got)
	}
}

func launcherPod() kubePod {
	var p kubePod
	f := false
	p.Spec.HostUsers, p.Spec.AutomountServiceAccountToken = &f, &f
	p.Spec.Volumes = []struct {
		Name     string           `json:"name"`
		HostPath *json.RawMessage `json:"hostPath"`
	}{{Name: "scratch"}, {Name: "bootstrap"}}
	var docker kubeContainer
	docker.Name = "docker"
	docker.SecurityContext = &struct {
		Privileged               *bool `json:"privileged"`
		AllowPrivilegeEscalation *bool `json:"allowPrivilegeEscalation"`
		ReadOnlyRootFilesystem   *bool `json:"readOnlyRootFilesystem"`
		Capabilities             *struct {
			Add  []string `json:"add"`
			Drop []string `json:"drop"`
		} `json:"capabilities"`
		SeccompProfile *struct {
			Type             string `json:"type"`
			LocalhostProfile string `json:"localhostProfile"`
		} `json:"seccompProfile"`
		ProcMount string `json:"procMount"`
	}{
		Capabilities: &struct {
			Add  []string `json:"add"`
			Drop []string `json:"drop"`
		}{Add: []string{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE"}},
		SeccompProfile: &struct {
			Type             string `json:"type"`
			LocalhostProfile string `json:"localhostProfile"`
		}{"Localhost", "profiles/sparkwing-dind.json"},
		ProcMount: "Unmasked",
	}
	p.Spec.InitContainers = []kubeContainer{docker}
	p.Spec.Containers = []kubeContainer{{Name: "run-node"}}
	return p
}

func TestPodSpecFindingsMatchTheLaunchersDesign(t *testing.T) {
	if got := podSpecFindings(launcherPod()); len(got) != 0 {
		t.Fatalf("launcher pod findings = %v, want none", got)
	}
	tr := true
	for name, edit := range map[string]func(*kubePod){
		"host users":   func(p *kubePod) { p.Spec.HostUsers = &tr },
		"token":        func(p *kubePod) { p.Spec.AutomountServiceAccountToken = nil },
		"host network": func(p *kubePod) { p.Spec.HostNetwork = true },
		"host path":    func(p *kubePod) { raw := json.RawMessage(`{"path":"/"}`); p.Spec.Volumes[0].HostPath = &raw },
		"extra caps": func(p *kubePod) {
			p.Spec.InitContainers[0].SecurityContext.Capabilities.Add = append(p.Spec.InitContainers[0].SecurityContext.Capabilities.Add, "SYS_MODULE")
		},
		"no seccomp":      func(p *kubePod) { p.Spec.InitContainers[0].SecurityContext.SeccompProfile = nil },
		"privileged":      func(p *kubePod) { p.Spec.InitContainers[0].SecurityContext.Privileged = &tr },
		"unmasked runner": func(p *kubePod) { p.Spec.Containers[0].SecurityContext = p.Spec.InitContainers[0].SecurityContext },
		"masked docker":   func(p *kubePod) { p.Spec.InitContainers[0].SecurityContext.ProcMount = "" },
	} {
		p := launcherPod()
		edit(&p)
		if got := podSpecFindings(p); len(got) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
}

type isoRunFixture struct {
	label, team, run, node, podIP, hostIP string
	peers                                 []string
}

var isoFixtures = []isoRunFixture{
	{"B1", "stg-iso-b", "run-1", "node-b1", "10.9.20.11", "10.9.20.1", nil},
	{"A1", "stg-iso-a", "run-2", "node-a1", "10.9.21.21", "10.9.21.1", []string{"B1"}},
	{"B2", "stg-iso-b", "run-3", "node-b2", "10.9.22.31", "10.9.22.1", []string{"A1", "B1"}},
}

func fixtureFor(key string) isoRunFixture {
	for _, fx := range isoFixtures {
		if fx.run == key || fx.label == key {
			return fx
		}
	}
	return isoRunFixture{}
}

func fixturePlan(fx isoRunFixture) isolationprobe.Plan {
	targets, err := staticTargets(isoInputs())
	if err != nil {
		panic(err)
	}
	plan := isolationprobe.Plan{
		Team: fx.team, ControllerURL: "https://staging.sparkwing.dev", ListenPort: fixtureListenPort, Canary: "isolation-canary-" + fx.label, Targets: targets,
		JobNamespace: isoJobNS, KubeNamespaces: []string{isoJobNS, "sparkwing-staging"}, KubePods: []isolationprobe.PodRef{{Namespace: "sparkwing-staging", Name: "controller"}},
	}
	for _, label := range fx.peers {
		p := fixtureFor(label)
		plan.PeerCanaries = append(plan.PeerCanaries, "isolation-canary-"+p.label)
		plan.PeerRuns = append(plan.PeerRuns, p.run)
		plan.Targets = append(plan.Targets, peerTargets(peerFacts{Label: p.label, Team: p.team, RunID: p.run, PodIP: p.podIP, HostIP: p.hostIP, ListenPort: fixtureListenPort})...)
	}
	return plan
}

func fixtureReady(fx isoRunFixture) isolationprobe.Ready {
	return isolationprobe.Ready{PodIPs: []string{fx.podIP}, ListenPort: fixtureListenPort, NetNS: "net:[1]", NodeIP: fx.hostIP, Home: "/home/runner"}
}

func fixtureResults(plan isolationprobe.Plan, ready isolationprobe.Ready, runID string) []isolationprobe.Result {
	var out []isolationprobe.Result
	for _, k := range isolationprobe.Expected(plan, ready, runID, fixture.SDKKeys()...) {
		out = append(out, isolationprobe.Result{Item: k.Item, Context: k.Context, Probe: k.Probe, Target: k.Target, Expect: isolationprobe.ExpectBlocked, Outcome: "timeout", Verdict: isolationprobe.Pass})
	}
	return out
}

func fixturePods(fx isoRunFixture) []isolationPod {
	return []isolationPod{
		{Name: fx.label + "-probe", Job: isolationprobe.ProbeJob, Node: fx.node, PodIP: fx.podIP, HostIP: fx.hostIP, Phase: "Running"},
		{Name: fx.label + "-sibling", Job: isolationprobe.SiblingJob, Node: fx.node},
		{Name: fx.label + "-allowed", Job: fixture.AllowedJob, Node: fx.node},
	}
}

// safety: every owed result is present and refused and each run has its own fenced node, so each
// shortfall case isolates exactly one gap.
func evalFixture() ([]isolationRun, map[string]isolationNode) {
	var runs []isolationRun
	nodes := map[string]isolationNode{}
	for _, fx := range isoFixtures {
		plan, ready := fixturePlan(fx), fixtureReady(fx)
		results := fixtureResults(plan, ready, fx.run)
		runs = append(runs, isolationRun{
			Label: fx.label, Team: fx.team, RunTeam: fx.team, RunID: fx.run, Peers: fx.peers, Plan: plan, Results: results,
			Ready: []isolationprobe.Ready{ready}, Summary: []isolationprobe.Summary{isolationprobe.Summarize(results)}, Pods: fixturePods(fx),
		})
		value := teamNodeValue(isoJobNS, fx.team)
		node := isolationNode{Name: fx.node, IPs: []string{fx.hostIP}, TeamNode: value, UsedTeam: value, UsedRun: runNodeValue(isoJobNS, fx.run), Pool: isoJobNS, Claimed: true}
		for _, p := range fixturePods(fx) {
			node.Pods = append(node.Pods, nodePod{Namespace: isoJobNS, Name: p.Name, Run: fx.run})
		}
		node.Pods = append(node.Pods, nodePod{Namespace: "kube-system", Name: "aws-node-" + fx.label, DaemonSet: true})
		nodes[fx.node] = node
	}
	return runs, nodes
}

func failedItems(e isolationEvaluation) []string {
	var out []string
	for _, it := range e.Items {
		if !it.OK {
			out = append(out, it.Item)
		}
	}
	return out
}

func liveAnchors() map[string]bool { return map[string]bool{"A1": true, "B1": true} }

func TestEvaluateIsolationPassesACompleteRefusedRun(t *testing.T) {
	runs, nodes := evalFixture()
	if e := evaluateIsolation(isoJobNS, runs, nodes, liveAnchors()); !e.OK {
		t.Fatalf("complete run failed: %+v", e)
	}
}

// Placement trusts controller-written owner metadata only because the kube API grade shows tenant
// jobs cannot create pods to forge it, so a kube API probe that failed, proved nothing or never
// reported leaves placement untrusted: C2 fails and the report header says why.
func TestPlacementTrustNeedsThePassingKubeAPIGrade(t *testing.T) {
	runs, nodes := evalFixture()
	report := isolationReport(isoArgs(), runs, evaluateIsolation(isoJobNS, runs, nodes, liveAnchors()), nil)
	if !strings.Contains(report, "That trust holds only because the kube API grade passed in every run here") {
		t.Fatalf("passing report header = %.500s; want the trust assumption stated", report)
	}
	kubeAPI := func(x isolationprobe.Result) bool { return x.Item == "C7" && x.Probe == "kube API public endpoint" }
	for name, c := range map[string]struct {
		edit  func([]isolationRun)
		items []string
	}{
		"a kube API probe that answered": {func(r []isolationRun) {
			i := slices.IndexFunc(r[1].Results, kubeAPI)
			r[1].Results[i].Verdict, r[1].Results[i].Outcome = isolationprobe.Breach, "connected; http-201"
			resummarize(&r[1])
		}, []string{"C2", "C7"}},
		"a kube API probe that proved nothing": {func(r []isolationRun) {
			i := slices.IndexFunc(r[0].Results, kubeAPI)
			r[0].Results[i].Verdict = isolationprobe.Inconclusive
			resummarize(&r[0])
		}, []string{"C2", "C7"}},
		"a kube API probe never reported": {func(r []isolationRun) {
			dropResult(&r[2], kubeAPI)
		}, []string{"C2", "C7"}},
		"a run that never probed": {func(r []isolationRun) {
			r[2].RunID, r[2].Results, r[2].Summary, r[2].Pods = "", nil, nil, nil
		}, []string{"C2", "C13"}},
		"a kube API probe that was skipped": {func(r []isolationRun) {
			i := slices.IndexFunc(r[0].Results, kubeAPI)
			r[0].Results[i].Verdict = isolationprobe.NotProbed
			resummarize(&r[0])
		}, []string{"C2"}},
		"a kube API probe only observed": {func(r []isolationRun) {
			i := slices.IndexFunc(r[2].Results, kubeAPI)
			r[2].Results[i].Verdict = isolationprobe.Observed
			resummarize(&r[2])
		}, []string{"C2"}},
	} {
		runs, nodes := evalFixture()
		c.edit(runs)
		e := evaluateIsolation(isoJobNS, runs, nodes, liveAnchors())
		if strings.Join(failedItems(e), ",") != strings.Join(c.items, ",") {
			t.Errorf("%s: failed %v, want %v", name, failedItems(e), c.items)
		}
		var c2 []string
		for _, it := range e.Items {
			if it.Item == "C2" {
				c2 = it.Failures
			}
		}
		if len(c2) != 1 || !strings.HasPrefix(c2[0], "placement is untrusted: ") {
			t.Errorf("%s: C2 failures = %q, want only the untrusted placement", name, c2)
		}
		if report := isolationReport(isoArgs(), runs, e, nil); !strings.Contains(report, "**Placement is UNTRUSTED:** ") {
			t.Errorf("%s: report header does not mark placement untrusted: %.500s", name, report)
		}
	}
}

func resummarize(r *isolationRun) {
	r.Summary = []isolationprobe.Summary{isolationprobe.Summarize(r.Results)}
}

func dropResult(r *isolationRun, match func(isolationprobe.Result) bool) {
	n := len(r.Results)
	r.Results = slices.DeleteFunc(r.Results, match)
	if len(r.Results) == n {
		panic("no result matched")
	}
	resummarize(r)
}

// Each way a run can fall short fails exactly the item it undermines, so no shortfall passes and
// none hides behind another.
func TestEvaluateIsolationFailsTheItemEachShortfallUndermines(t *testing.T) {
	for name, c := range map[string]struct {
		edit  func([]isolationRun, map[string]isolationNode, map[string]bool)
		items []string
	}{
		"a breach": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[1].Results = append(r[1].Results, isolationprobe.Result{Item: "C4", Context: "job", Probe: "IMDS", Verdict: isolationprobe.Breach})
			resummarize(&r[1])
		}, []string{"C4"}},
		"a probe that proved nothing": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Results = append(r[0].Results, isolationprobe.Result{Item: "C10", Context: "dind-privileged", Probe: "mount audit", Verdict: isolationprobe.Inconclusive})
			resummarize(&r[0])
		}, []string{"C10"}},
		"a missing context": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[2], func(x isolationprobe.Result) bool { return x.Context == isolationprobe.ContextPrivileged })
		}, []string{"C2", "C3", "C4", "C5", "C6", "C7", "C8", "C9", "C10", "C11", "C12"}},
		"the service-account token check": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[0], func(x isolationprobe.Result) bool { return x.Probe == "credential file token" })
		}, []string{"C3"}},
		"an anonymous route": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[2], func(x isolationprobe.Result) bool { return x.Probe == "anonymous peer-run probe output" })
		}, []string{"C3"}},
		"an SDK read": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[1], func(x isolationprobe.Result) bool { return x.Probe == fixture.ProbeSDKAllowed })
		}, []string{"C3"}},
		"one syscall": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[1], func(x isolationprobe.Result) bool {
				return x.Context == isolationprobe.ContextPrivileged && x.Probe == "seccomp refuses io_uring_setup"
			})
		}, []string{"C12"}},
		"the cluster DNS control": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[0], func(x isolationprobe.Result) bool {
				return x.Context == isolationprobe.ContextBridge && x.Target == "kubernetes.default.svc.cluster.local"
			})
		}, []string{"C6"}},
		"one IPv6 path": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[0], func(x isolationprobe.Result) bool {
				return x.Context == isolationprobe.ContextBridge && x.Target == "[fd00:ec2::23]:80"
			})
		}, []string{"C5"}},
		"the database": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			dropResult(&r[2], func(x isolationprobe.Result) bool {
				return x.Context == isolationprobe.ContextJob && x.Probe == "controller database"
			})
		}, []string{"C6"}},
		"a shared node": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[2].Pods[0].Node = "node-a1"
		}, []string{"C2", "C8"}},
		"another team's node label": {func(_ []isolationRun, n map[string]isolationNode, _ map[string]bool) {
			node := n["node-a1"]
			node.TeamNode = teamNodeValue(isoJobNS, "stg-iso-b")
			n["node-a1"] = node
		}, []string{"C2"}},
		"one run's fence on another run's node": {func(_ []isolationRun, n map[string]isolationNode, _ map[string]bool) {
			node := n["node-b2"]
			node.UsedRun = n["node-b1"].UsedRun
			n["node-b2"] = node
		}, []string{"C2"}},
		"unique fences for unrelated runs": {func(_ []isolationRun, n map[string]isolationNode, _ map[string]bool) {
			for k, node := range n {
				node.UsedRun = "unrelated-" + k
				n[k] = node
			}
		}, []string{"C2"}},
		"a node outside the staging pool": {func(_ []isolationRun, n map[string]isolationNode, _ map[string]bool) {
			node := n["node-b1"]
			node.Pool = "sparkwing-cloud-jobs"
			n["node-b1"] = node
		}, []string{"C2"}},
		"another run's pod on a node": {func(_ []isolationRun, n map[string]isolationNode, _ map[string]bool) {
			node := n["node-b1"]
			node.Pods = append(node.Pods, nodePod{Namespace: isoJobNS, Name: "stranger", Run: "run-9"})
			n["node-b1"] = node
		}, []string{"C2"}},
		"a peer gone before it was probed": {func(_ []isolationRun, _ map[string]isolationNode, a map[string]bool) {
			a["B1"] = false
		}, []string{"C9"}},
		"the wrong team": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[1].RunTeam = "stg-iso-b"
		}, []string{"C1"}},
		"own node misidentified": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Ready[0].NodeIP = "10.9.99.1"
		}, []string{"C8"}},
		"a pod spec drift": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Pods[0].Spec = []string{"hostUsers is not false"}
		}, []string{"C3"}},
		"no summary": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Summary = nil
		}, []string{"C13"}},
		"a summary that disagrees": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Summary[0].Pass++
		}, []string{"C13"}},
		"an invalid verdict": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Results[0].Verdict = "PASS"
			resummarize(&r[0])
		}, []string{"C13"}},
		"a run that never ran": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[2].RunID, r[2].Results, r[2].Summary, r[2].Pods = "", nil, nil, nil
		}, []string{"C2", "C13"}},
		"no NodeClaim": {func(_ []isolationRun, n map[string]isolationNode, _ map[string]bool) {
			node := n["node-a1"]
			node.Claimed = false
			n["node-a1"] = node
		}, []string{"C13"}},
	} {
		runs, nodes := evalFixture()
		alive := liveAnchors()
		c.edit(runs, nodes, alive)
		e := evaluateIsolation(isoJobNS, runs, nodes, alive)
		if e.OK || strings.Join(failedItems(e), ",") != strings.Join(c.items, ",") {
			t.Errorf("%s: failed %v, want %v", name, failedItems(e), c.items)
		}
	}
}

type isoClusterFake struct {
	t          *testing.T
	mu         sync.Mutex
	triggered  []string
	cancelled  map[string]bool
	breach     map[string]bool
	omit       map[string]bool
	garble     map[string]bool
	logExit    map[string]int
	statusN    map[string]int
	commands   []string
	plans      map[string]isolationprobe.Plan
	teardown   bool
	deny       string
	limits     []string
	controller map[string]string
	whoami     map[string]string
}

func (f *isoClusterFake) run(_ context.Context, c isolationCommand) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := c.Name + " " + strings.Join(c.Args, " ")
	f.commands = append(f.commands, line)
	args := c.Args
	switch c.Name {
	case "git", "go":
		return "", 0, nil
	case "kubectl":
		if args[0] == "config" {
			return `{"contexts":[{"name":"staging-read","context":{"cluster":"c"}}],"clusters":[{"name":"c","cluster":{"server":"https://api.example.invalid:443"}}]}`, 0, nil
		}
		return f.kubectl(args[2:])
	case "sparkwing":
		return f.sparkwing(args)
	}
	f.t.Errorf("unexpected command %s", line)
	return "", 1, nil
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func list(items any) string {
	data, _ := json.Marshal(map[string]any{"items": items})
	return string(data)
}

func (f *isoClusterFake) gone(fx isoRunFixture) bool {
	return f.cancelled[fx.run] || (f.teardown && fx.label == "B2" && f.statusN[fx.run] > 1)
}

func (f *isoClusterFake) started() []isoRunFixture { return isoFixtures[:len(f.triggered)] }

func (f *isoClusterFake) jobPods(fx isoRunFixture) []kubePod {
	var pods []kubePod
	for _, job := range []string{isolationprobe.ProbeJob, isolationprobe.SiblingJob, fixture.AllowedJob} {
		p := launcherPod()
		p.Metadata.Name, p.Metadata.Namespace = fx.label+"-"+job, isoJobNS
		p.Metadata.Labels = map[string]string{jobPodLabel: "sw-" + fx.run + "-" + job}
		p.Metadata.OwnerReferences = controlledBy("Job", "uid-sw-"+fx.run+"-"+job)
		p.Spec.NodeName, p.Status.HostIP, p.Status.Phase = fx.node, fx.hostIP, "Running"
		if job == isolationprobe.ProbeJob {
			p.Status.PodIP = fx.podIP
		}
		pods = append(pods, p)
	}
	return pods
}

func (f *isoClusterFake) kubectl(args []string) (string, int, error) {
	kind, ns := args[1], flagValue(args, "-n")
	if f.deny != "" && strings.Contains(strings.Join(args, " "), f.deny) {
		return "Error from server (Forbidden)", 1, nil
	}
	switch {
	case kind == "pods" && ns == "sparkwing-staging":
		return list(isoInputs().ControllerPods), 0, nil
	case kind == "services" && ns == "sparkwing-staging":
		return list(isoInputs().ControllerServices), 0, nil
	case kind == "pods" && ns == "kube-system":
		return list(isoInputs().DNSPods), 0, nil
	case kind == "services" && ns == "kube-system":
		return list(isoInputs().DNSServices), 0, nil
	case kind == "service":
		data, _ := json.Marshal(isoInputs().APIService)
		return string(data), 0, nil
	case kind == "endpointslices":
		return list(isoInputs().APIEndpoints), 0, nil
	case kind == "pods" && ns == "ingress":
		return list(isoInputs().IngressPods), 0, nil
	case kind == "nodes" && flagValue(args, "-l") != "":
		return list(isoInputs().InfraNodes), 0, nil
	case kind == "nodes":
		var nodes []kubeNode
		for _, fx := range f.started() {
			n := isoNode(fx.node, fx.hostIP)
			value := teamNodeValue(isoJobNS, fx.team)
			n.Metadata.Labels = map[string]string{teamNodeLabel: value, usedTeamNodeLabel: value, usedRunNodeLabel: runNodeValue(isoJobNS, fx.run), poolNamespaceLabel: isoJobNS}
			nodes = append(nodes, n)
		}
		return list(nodes), 0, nil
	case kind == "nodeclaims":
		var claims []kubeNodeClaim
		for _, fx := range f.started() {
			var c kubeNodeClaim
			c.Metadata.Name, c.Status.NodeName = "claim-"+fx.label, fx.node
			claims = append(claims, c)
		}
		return list(claims), 0, nil
	case kind == "jobs":
		var jobs []kubeJob
		for _, fx := range f.started() {
			if f.gone(fx) {
				continue
			}
			for _, job := range []string{isolationprobe.ProbeJob, isolationprobe.SiblingJob, fixture.AllowedJob} {
				jobs = append(jobs, isoJob("sw-"+fx.run+"-"+job, fx.run, job, fx.team))
			}
		}
		return list(jobs), 0, nil
	case kind == "pods" && ns == isoJobNS:
		var pods []kubePod
		for _, fx := range f.started() {
			if !f.gone(fx) {
				pods = append(pods, f.jobPods(fx)...)
			}
		}
		return list(pods), 0, nil
	case kind == "pods" && flagValue(args, "--field-selector") != "":
		node := strings.TrimPrefix(flagValue(args, "--field-selector"), "spec.nodeName=")
		var pods []kubePod
		for _, fx := range f.started() {
			if fx.node != node || f.gone(fx) {
				continue
			}
			pods = append(pods, f.jobPods(fx)...)
			ds := isoPod("aws-node-"+fx.label, fx.hostIP, true)
			ds.Metadata.Namespace = "kube-system"
			ds.Metadata.OwnerReferences = controlledBy("DaemonSet", "uid-ds-aws-node")
			pods = append(pods, ds)
		}
		return list(pods), 0, nil
	case kind == "daemonsets":
		var ds kubeDaemonSet
		ds.Metadata.Name, ds.Metadata.Namespace, ds.Metadata.UID = "aws-node", "kube-system", "uid-ds-aws-node"
		return list([]kubeDaemonSet{ds}), 0, nil
	case kind == "networkpolicies" || kind == "validatingadmissionpolicybindings":
		return `{"items":[]}`, 0, nil
	}
	f.t.Errorf("unexpected kubectl %v", args)
	return "", 1, nil
}

func (f *isoClusterFake) sparkwing(args []string) (string, int, error) {
	run := flagValue(args, "--run")
	switch {
	case args[0] == "profiles" && args[1] == "list":
		var b strings.Builder
		for _, team := range []string{"stg-iso-a", "stg-iso-b"} {
			controller := "https://staging.sparkwing.dev"
			if c, ok := f.controller[team]; ok {
				controller = c
			}
			fmt.Fprintf(&b, `{"name":%q,"controller":%q,"token":"swu_***","default":false}`+"\n", team, controller)
		}
		return b.String(), 0, nil
	case args[0] == "tokens" && args[1] == "list":
		team := flagValue(args, "--profile")
		if t, ok := f.whoami[team]; ok {
			team = t
		}
		return fmt.Sprintf(`{"kind":"member","prefix":"swu_x","team":%q,"scopes":["runs.read"]}`+"\n", team), 0, nil
	case args[0] == "pipeline" && args[1] == "new":
		dir := filepath.Join(flagValue(args, "-C"), ".sparkwing")
		for name, body := range map[string]string{"go.mod": "module scaffold\n", "go.sum": "", "main.go": "package main\n", "jobs/x.go": "package jobs\n", "README.md": ""} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
				return "", 1, err
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				return "", 1, err
			}
		}
		return "{}", 0, nil
	case args[0] == "pipeline" && args[1] == "trigger":
		fx := isoFixtures[len(f.triggered)]
		if flagValue(args, "--profile") != fx.team || flagValue(args, "--role") != map[bool]string{true: "probe", false: "anchor"}[fx.label == "B2"] {
			f.t.Errorf("trigger %v, want %s as %s", args, fx.label, fx.team)
		}
		plan, err := isolationprobe.ReadPlan(filepath.Join(flagValue(args, "-C"), isolationprobe.PlanFile))
		if err != nil {
			f.t.Errorf("%s plan: %v", fx.label, err)
		}
		if len(plan.PeerRuns) != len(fx.peers) {
			f.t.Errorf("%s plan peers = %v, want %d", fx.label, plan.PeerRuns, len(fx.peers))
		}
		if f.plans == nil {
			f.plans = map[string]isolationprobe.Plan{}
		}
		f.plans[fx.run] = plan
		f.triggered = append(f.triggered, fx.run)
		f.limits = append(f.limits, fx.label+" timeout="+flagValue(args, "--timeout")+" hold="+flagValue(args, "--hold"))
		return `{"run_id":"` + fx.run + `"}`, 0, nil
	case args[0] == "runs" && args[1] == "status":
		f.statusN[run]++
		status := f.statusOf(run)
		code := 3
		if terminal(status) {
			code = 0
		}
		return `{"id":"` + run + `","team":"` + fixtureFor(run).team + `","status":"` + status + `"}`, code, nil
	case args[0] == "runs" && args[1] == "logs":
		fx := fixtureFor(run)
		if code, ok := f.logExit[fx.label]; ok {
			return "", code, nil
		}
		// safety: the real runs logs prints the records it has and exits 3 while the run is live.
		code := 3
		if terminal(f.statusOf(run)) {
			code = 0
		}
		return f.log(fx), code, nil
	case args[0] == "runs" && args[1] == "cancel":
		f.cancelled[run] = true
		return "{}", 0, nil
	}
	f.t.Errorf("unexpected sparkwing %v", args)
	return "", 1, nil
}

func (f *isoClusterFake) statusOf(run string) string {
	switch {
	case f.cancelled[run]:
		return "cancelled"
	case fixtureFor(run).label == "B2" && f.statusN[run] > 1:
		return "success"
	}
	return "running"
}

func (f *isoClusterFake) log(fx isoRunFixture) string {
	ready := fixtureReady(fx)
	results := fixtureResults(f.plans[fx.run], ready, fx.run)
	if f.omit[fx.label] {
		results = slices.DeleteFunc(results, func(r isolationprobe.Result) bool { return r.Probe == "credential file token" })
	}
	if f.breach[fx.label] {
		results = append(results, isolationprobe.Result{Item: "C4", Context: "dind-bridge", Probe: "IMDS", Target: "169.254.169.254:80", Outcome: "connected", Verdict: isolationprobe.Breach})
	}
	var b strings.Builder
	b.WriteString("probe | " + isolationprobe.FormatLine(isolationprobe.ReadyMarker, ready) + "\n")
	if f.garble[fx.label] {
		b.WriteString("probe | " + isolationprobe.ResultMarker + "{not json\n")
	}
	for _, r := range results {
		b.WriteString("probe | " + isolationprobe.FormatLine(isolationprobe.ResultMarker, r) + "\n")
	}
	b.WriteString(isolationprobe.FormatLine(isolationprobe.SummaryMarker, isolationprobe.Summarize(results)) + "\n")
	return b.String()
}

func newIsoFake(t *testing.T) *isoClusterFake {
	return &isoClusterFake{t: t, cancelled: map[string]bool{}, statusN: map[string]int{}}
}

func driveFake(t *testing.T, fake *isoClusterFake) (string, error) {
	t.Helper()
	return driveFakeWith(t, fake, isoArgs())
}

func driveFakeWith(t *testing.T, fake *isoClusterFake, args CloudIsolationArgs) (string, error) {
	t.Helper()
	root, err := mainModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(0, 0)
	evidence := t.TempDir()
	d := &isolationDriver{args: args, repo: root, tmp: t.TempDir(), evidence: evidence, tools: isolationTools{
		run:  fake.run,
		wait: func(context.Context, time.Duration) error { clock = clock.Add(pollInterval); return nil },
		now:  func() time.Time { return clock },
		say:  func(context.Context, string) {},
	}}
	return evidence, d.conform(t.Context())
}

func driveWrapped(t *testing.T, wrap func(isolationCommand, string) string) (*isoClusterFake, string, error) {
	t.Helper()
	root, err := mainModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	fake, clock, evidence := newIsoFake(t), time.Unix(0, 0), t.TempDir()
	d := &isolationDriver{args: isoArgs(), repo: root, tmp: t.TempDir(), evidence: evidence, tools: isolationTools{
		run: func(ctx context.Context, c isolationCommand) (string, int, error) {
			out, code, err := fake.run(ctx, c)
			return wrap(c, out), code, err
		},
		wait: func(context.Context, time.Duration) error { clock = clock.Add(pollInterval); return nil },
		now:  func() time.Time { return clock },
		say:  func(context.Context, string) {},
	}}
	return fake, evidence, d.conform(t.Context())
}

func isNodeList(c isolationCommand) bool {
	return c.Name == "kubectl" && len(c.Args) > 3 && c.Args[3] == "nodes" && flagValue(c.Args, "-l") == ""
}

func isJobPodList(c isolationCommand) bool {
	return c.Name == "kubectl" && len(c.Args) > 3 && c.Args[3] == "pods" && flagValue(c.Args, "-n") == isoJobNS
}

func relabelNodes(out, fence string) string {
	var nodes kubeList[kubeNode]
	_ = json.Unmarshal([]byte(out), &nodes)
	for i := range nodes.Items {
		nodes.Items[i].Metadata.Labels[usedRunNodeLabel] = fence
	}
	return list(nodes.Items)
}

// The review's repro: each mandatory group must yield something to probe, so a group of objects
// that yields no target fails discovery.
func TestStaticTargetsRequireATargetFromEachGroup(t *testing.T) {
	for _, group := range []string{"controller pods", "controller services", "DNS pods", "DNS services", "ingress pods", "infra nodes"} {
		t.Run(group, func(t *testing.T) {
			in := isoInputs()
			switch group {
			case "controller pods":
				in.ControllerPods = []kubePod{{}}
			case "controller services":
				in.ControllerServices = []kubeService{{}}
			case "DNS pods":
				in.DNSPods = []kubePod{{}}
			case "DNS services":
				in.DNSServices = []kubeService{{}}
			case "ingress pods":
				in.IngressPods = []kubePod{{}}
			case "infra nodes":
				in.InfraNodes = []kubeNode{{}}
			}
			if targets, err := staticTargets(in); err == nil {
				t.Fatalf("accepted %s with no usable target; total=%d", group, len(targets))
			}
		})
	}
}

// The review's repro: a controller on its node's network cannot be probed as a pod, so the run
// fails instead of passing without that boundary.
func TestConformRefusesUntargetableControllerPods(t *testing.T) {
	_, _, err := driveWrapped(t, func(c isolationCommand, out string) string {
		if c.Name == "kubectl" && len(c.Args) > 3 && c.Args[3] == "pods" && flagValue(c.Args, "-n") == "sparkwing-staging" {
			var pods kubeList[kubePod]
			_ = json.Unmarshal([]byte(out), &pods)
			for i := range pods.Items {
				pods.Items[i].Spec.HostNetwork = true
			}
			return list(pods.Items)
		}
		return out
	})
	if err == nil || !strings.Contains(err.Error(), "discovery cannot probe controller namespace pods") {
		t.Fatalf("conform = %v, want discovery to refuse host-networked controller pods", err)
	}
}

// The review's repro: a wrong fence seen once while B1 is live stays a failure after the labels
// look healthy again.
func TestConformKeepsAnUnsafeLiveNodeObservation(t *testing.T) {
	seen := 0
	_, evidence, err := driveWrapped(t, func(c isolationCommand, out string) string {
		if isNodeList(c) {
			if seen++; seen == 1 {
				return relabelNodes(out, "unrelated-run")
			}
		}
		return out
	})
	if seen < 2 {
		t.Fatal("did not restore node after unsafe live observation")
	}
	report, _ := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
	if err == nil || !strings.Contains(string(report), `observation 1: B1 node node-b1 is fenced to "unrelated-run"`) {
		t.Fatalf("conform = %v, report = %.300s; want the first observation's wrong fence kept", err, report)
	}
}

// The review's repro: an unsafe pod spec seen once stays a failure after a safe replacement.
func TestConformKeepsAnUnsafeLivePodObservation(t *testing.T) {
	seen := 0
	_, _, err := driveWrapped(t, func(c isolationCommand, out string) string {
		if isJobPodList(c) {
			if seen++; seen == 1 {
				var pods kubeList[kubePod]
				_ = json.Unmarshal([]byte(out), &pods)
				for i := range pods.Items {
					bad := true
					pods.Items[i].Spec.HostUsers = &bad
				}
				return list(pods.Items)
			}
		}
		return out
	})
	if seen < 2 {
		t.Fatal("did not restore pod after unsafe live observation")
	}
	if err == nil || !strings.Contains(err.Error(), "C3") {
		t.Fatalf("conform = %v, want C3 from hostUsers=true seen live", err)
	}
}

// The review's repro: B1's broken fence stops the sequence before A1 starts.
func TestConformStopsOnBrokenPlacementBeforeTheNextPhase(t *testing.T) {
	fake, _, err := driveWrapped(t, func(c isolationCommand, out string) string {
		if isNodeList(c) {
			return relabelNodes(out, "unrelated-run")
		}
		return out
	})
	if err == nil {
		t.Fatal("invalid placement passed overall")
	}
	if len(fake.triggered) > 1 {
		t.Fatalf("triggered %v despite B1's broken run fence", fake.triggered)
	}
}

// The review's repro: B1 ending after A1 probed it stops the sequence before B2 starts.
func TestConformStopsOnADeadPeerBeforeTheNextPhase(t *testing.T) {
	triggered := 0
	fake, _, err := driveWrapped(t, func(c isolationCommand, out string) string {
		if c.Name == "sparkwing" && len(c.Args) > 1 {
			if c.Args[0] == "pipeline" && c.Args[1] == "trigger" {
				triggered++
			}
			if c.Args[0] == "runs" && c.Args[1] == "status" && flagValue(c.Args, "--run") == "run-1" && triggered >= 2 {
				return strings.Replace(out, `"status":"running"`, `"status":"cancelled"`, 1)
			}
		}
		return out
	})
	if err == nil {
		t.Fatal("dead peer passed overall")
	}
	if len(fake.triggered) > 2 {
		t.Fatalf("triggered B2 despite B1 already cancelled after A1; triggered=%v", fake.triggered)
	}
}

// The last phase is gated too: an anchor that ended before B2 finished probing fails the run at
// B2's gate, and the report names the gap.
func TestConformGatesTheLastPhaseOnItsPeers(t *testing.T) {
	triggered := 0
	_, evidence, err := driveWrapped(t, func(c isolationCommand, out string) string {
		if c.Name == "sparkwing" && len(c.Args) > 1 {
			if c.Args[0] == "pipeline" && c.Args[1] == "trigger" {
				triggered++
			}
			if c.Args[0] == "runs" && c.Args[1] == "status" && flagValue(c.Args, "--run") == "run-1" && triggered >= 3 {
				return strings.Replace(out, `"status":"running"`, `"status":"cancelled"`, 1)
			}
		}
		return out
	})
	report, _ := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
	if err == nil || !strings.Contains(string(report), "the sequence stopped: check everything B2 owed before the next phase: peer B1 was not running after B2 probed it") {
		t.Fatalf("conform = %v, report = %.600s; want B2's gate to stop the sequence on B1", err, report)
	}
}

func addToNode(out string, p kubePod, raw func(map[string]any)) string {
	data, _ := json.Marshal(p)
	var doc map[string]any
	_ = json.Unmarshal(data, &doc)
	if raw != nil {
		raw(doc)
	}
	data, _ = json.Marshal(doc)
	var result map[string]any
	_ = json.Unmarshal([]byte(out), &result)
	items, _ := result["items"].([]any)
	result["items"] = append(items, json.RawMessage(data))
	data, _ = json.Marshal(result)
	return string(data)
}

// The review's forgery repro: a foreign workload on B1's node fails C2 whatever capacity metadata
// it carries. Claimed capacity metadata never exempts a foreign executable or unsafe spec.
func TestConformRefusesForeignWorkloadsClaimingCapacity(t *testing.T) {
	for _, mode := range []string{"plain-foreign", "foreign-run", "forged-owner-run", "forged-owner-run-foreign-team", "forged-owner-run-unsafe-spec", "forged-run-other-namespace"} {
		t.Run(mode, func(t *testing.T) {
			_, evidence, err := driveWrapped(t, func(c isolationCommand, out string) string {
				if c.Name != "kubectl" || flagValue(c.Args, "--field-selector") != "spec.nodeName=node-b1" {
					return out
				}
				p := isoPod("foreign-workload", "10.9.20.12", true)
				p.Metadata.Namespace, p.Metadata.Labels, p.Metadata.Annotations = isoJobNS, map[string]string{}, map[string]string{}
				if mode != "plain-foreign" {
					run, team := "run-1", "stg-iso-b"
					if mode == "foreign-run" {
						run = "run-9"
					}
					if mode == "forged-owner-run-foreign-team" {
						team = "attacker-team"
					}
					p.Metadata.Labels["sparkwing.dev/capacity-request"] = "true"
					p.Metadata.Annotations["sparkwing.dev/capacity-demand"] = `{"binding":{"Team":"` + team + `","RunID":"` + run + `"}}`
				}
				if mode == "forged-run-other-namespace" {
					p.Metadata.Namespace = "foreign-namespace"
				}
				if mode == "forged-owner-run-unsafe-spec" {
					p.Spec.HostNetwork, p.Spec.HostPID = true, true
				}
				return addToNode(out, p, func(doc map[string]any) {
					spec, _ := doc["spec"].(map[string]any)
					spec["containers"] = []any{map[string]any{"name": "foreign-workload", "image": "attacker.example/foreign:fixture", "command": []string{"/bin/sh", "-c", "echo foreign; sleep 999"}}}
				})
			})
			report, _ := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
			if err == nil || !strings.Contains(string(report), "also held pod") {
				t.Fatalf("conform = %v, report = %.300s; want C2 for the foreign workload", err, report)
			}
		})
	}
}

func isoCapacityJSON(t *testing.T, run, team string) []byte {
	t.Helper()
	var pod map[string]any
	if err := json.Unmarshal([]byte(`{"spec":{"automountServiceAccountToken":false,"securityContext":{"runAsUser":65534,"runAsGroup":65534,"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"capacity","image":"runner@sha256:fixture","command":["/bin/sh","-c","trap 'exit 0' TERM; /usr/bin/sleep \"$1\" & wait","capacity"],"args":["1200"],"imagePullPolicy":"IfNotPresent","terminationMessagePath":"/dev/termination-log","terminationMessagePolicy":"File","resources":{"requests":{"cpu":"2","memory":"5529Mi","ephemeral-storage":"20Gi"},"limits":{"cpu":"2","memory":"5529Mi","ephemeral-storage":"20Gi"}},"securityContext":{"allowPrivilegeEscalation":false,"readOnlyRootFilesystem":true,"capabilities":{"drop":["ALL"]}}}]}}`), &pod); err != nil {
		t.Fatal(err)
	}
	b := awardBinding{Kind: "work", UnitID: "fixture-unit", Team: team, RunID: run, NodeID: "output-allowed", Generation: 1}
	binding, _ := json.Marshal(map[string]any{"binding": b})
	raw := fmt.Sprintf("%q/%q/%q/%q/%q/%d/%d/%q/%d", b.UnitID, b.Team, b.RunID, b.NodeID, b.Kind, b.Generation, b.Attempt, b.SourceNodeID, b.SourceGeneration)
	sum := sha256.Sum256([]byte(raw))
	pod["metadata"] = map[string]any{
		"name": "sw-capacity-" + hex.EncodeToString(sum[:24]), "namespace": isoJobNS,
		"annotations": map[string]string{"sparkwing.dev/capacity-demand": string(binding)},
		"labels":      map[string]string{"sparkwing.dev/capacity-request": "true", "app.kubernetes.io/managed-by": "sparkwing-launcher", "sparkwing.dev/team": teamNodeValue(isoJobNS, team)},
	}
	data, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func isoCapacityDriver() isolationDriver {
	runner := launcherPod()
	runner.Spec.Containers[0].Image = "runner@sha256:fixture"
	runner.Metadata.OwnerReferences = controlledBy("Job", "uid-own-job")
	d := isolationDriver{args: isoArgs(), runs: []*isolationRun{{RunID: "run-1", Team: "stg-iso-b"}}, seen: newPlacementLog()}
	d.seen.jobRun["uid-own-job"] = "run-1"
	d.seen.samples = []placementSample{{pods: map[string][]runPod{"B1": {{kubePod: runner}}}}}
	return d
}

func TestPlacementAcceptsOnlyTheRunsInertCapacityPod(t *testing.T) {
	for _, mode := range []string{"own", "foreign-team", "foreign-run", "wrong-name", "unsafe-command", "host-network", "different-image", "privileged"} {
		t.Run(mode, func(t *testing.T) {
			run, team := "run-1", "stg-iso-b"
			if mode == "foreign-team" {
				team = "stg-iso-a"
			}
			if mode == "foreign-run" {
				run = "run-9"
			}
			var pod kubePod
			if err := json.Unmarshal(isoCapacityJSON(t, run, team), &pod); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "wrong-name":
				pod.Metadata.Name += "x"
			case "unsafe-command":
				pod.Spec.Containers[0].Command = []string{"attacker"}
			case "host-network":
				pod.Spec.HostNetwork = true
			case "different-image":
				pod.Spec.Containers[0].Image = "attacker@sha256:fixture"
			case "privileged":
				yes := true
				pod.Spec.Containers[0].SecurityContext.Privileged = &yes
			}
			d := isoCapacityDriver()
			node := d.isolationNodeOf(isoNode("node-b1"), []kubePod{pod})
			want := ""
			if mode == "own" {
				want = "run-1"
			}
			if node.Pods[0].Run != want {
				t.Fatalf("capacity occupant run=%q, want %q", node.Pods[0].Run, want)
			}
		})
	}
}

func TestNodeCaptureResolvesAJobCreatedDuringTheObservation(t *testing.T) {
	fake := newIsoFake(t)
	fake.triggered = []string{"run-1"}
	d := isolationDriver{args: isoArgs(), seen: newPlacementLog(), evidence: t.TempDir(), tools: isolationTools{run: fake.run}}
	sample := placementSample{}
	if err := d.observeNodes(t.Context(), []string{isoFixtures[0].node}, &sample); err != nil {
		t.Fatal(err)
	}
	uid := "uid-sw-run-1-" + isolationprobe.ProbeJob
	if d.seen.jobRun[uid] != "run-1" {
		t.Fatalf("new Job owner %q remains unresolved: %v", uid, d.seen.jobRun)
	}
}

func TestCapacityPlacementRejectsAdditionalExecutionPathsFromRawJSON(t *testing.T) {
	for _, c := range []struct{ name, where, field, value string }{
		{"postStart exec", "container", "lifecycle", `{"postStart":{"exec":{"command":["/bin/sh","-c","echo extra code"]}}}`},
		{"preStop exec", "container", "lifecycle", `{"preStop":{"exec":{"command":["/bin/sh","-c","echo extra code"]}}}`},
		{"liveness exec", "container", "livenessProbe", `{"exec":{"command":["/bin/sh","-c","echo extra code"]}}`},
		{"readiness exec", "container", "readinessProbe", `{"exec":{"command":["/bin/sh","-c","echo extra code"]}}`},
		{"startup exec", "container", "startupProbe", `{"exec":{"command":["/bin/sh","-c","echo extra code"]}}`},
		{"HTTP probe", "container", "livenessProbe", `{"httpGet":{"path":"/","port":8080}}`},
		{"ephemeral container", "pod", "ephemeralContainers", `[{"name":"debug","image":"attacker","command":["/bin/sh"],"targetContainerName":"capacity"}]`},
		{"init container", "pod", "initContainers", `[{"name":"init","image":"attacker","command":["/bin/sh"]}]`},
		{"environment injection", "container", "env", `[{"name":"LD_PRELOAD","value":"/tmp/extra.so"}]`},
		{"environment source", "container", "envFrom", `[{"configMapRef":{"name":"extra"}}]`},
		{"device plugin resource", "container", "resources", `{"requests":{"example.com/device":"1"}}`},
		{"resource claim", "container", "resources", `{"claims":[{"name":"extra"}]}`},
		{"container root override", "container.securityContext", "runAsUser", `0`},
		{"container group override", "container.securityContext", "runAsGroup", `0`},
		{"container nonroot override", "container.securityContext", "runAsNonRoot", `false`},
		{"alternate runtime", "pod", "runtimeClassName", `"extra-runtime"`},
		{"shared process namespace", "pod", "shareProcessNamespace", `true`},
		{"pod sysctls", "pod.securityContext", "sysctls", `[{"name":"kernel.shm_rmid_forced","value":"1"}]`},
		{"future container path", "container", "futureExecutionHandler", `{"exec":{"command":["/bin/sh"]}}`},
		{"future pod path", "pod", "futureExecutionHandler", `{"exec":{"command":["/bin/sh"]}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal(isoCapacityJSON(t, "run-1", "stg-iso-b"), &raw); err != nil {
				t.Fatal(err)
			}
			spec := raw["spec"].(map[string]any)
			container := spec["containers"].([]any)[0].(map[string]any)
			target := spec
			switch c.where {
			case "container":
				target = container
			case "container.securityContext":
				target = container["securityContext"].(map[string]any)
			case "pod.securityContext":
				target = spec["securityContext"].(map[string]any)
			}
			var value any
			if err := json.Unmarshal([]byte(c.value), &value); err != nil {
				t.Fatal(err)
			}
			target[c.field] = value
			data, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var pod kubePod
			if err := json.Unmarshal(data, &pod); err != nil {
				t.Fatal(err)
			}
			d := isoCapacityDriver()
			if got := d.isolationNodeOf(isoNode("node-b1"), []kubePod{pod}).Pods[0].Run; got != "" {
				t.Fatalf("extra execution path was exempted as run %q", got)
			}
		})
	}
}

// Claimed ownership is not ownership: a pod naming one of the run's Jobs by label, or claiming a
// DaemonSet that the cluster does not list, is a foreign occupant.
func TestConformRefusesOccupantsWithClaimedOwners(t *testing.T) {
	for name, forge := range map[string]func(*kubePod){
		"Job label without the Job": func(p *kubePod) {
			p.Metadata.Namespace = isoJobNS
			p.Metadata.Labels = map[string]string{jobPodLabel: "sw-run-1-probe"}
		},
		"Job label and a made-up owner": func(p *kubePod) {
			p.Metadata.Namespace = isoJobNS
			p.Metadata.Labels = map[string]string{jobPodLabel: "sw-run-1-probe"}
			p.Metadata.OwnerReferences = controlledBy("Job", "uid-made-up")
		},
		"a DaemonSet the cluster does not list": func(p *kubePod) {
			p.Metadata.Namespace = "kube-system"
			p.Metadata.OwnerReferences = controlledBy("DaemonSet", "uid-ds-made-up")
		},
		"a listed DaemonSet in another namespace": func(p *kubePod) {
			p.Metadata.Namespace = isoJobNS
			p.Metadata.OwnerReferences = controlledBy("DaemonSet", "uid-ds-aws-node")
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := driveWrapped(t, func(c isolationCommand, out string) string {
				if c.Name != "kubectl" || flagValue(c.Args, "--field-selector") != "spec.nodeName=node-b1" {
					return out
				}
				p := isoPod("claimed-occupant", "10.9.20.13", true)
				forge(&p)
				return addToNode(out, p, nil)
			})
			if err == nil || !strings.Contains(err.Error(), "C2") {
				t.Fatalf("conform = %v, want C2 for an occupant whose owner is only claimed", err)
			}
		})
	}
}

func driveIsolation(t *testing.T, breach bool) (*isoClusterFake, string, error) {
	t.Helper()
	fake := newIsoFake(t)
	if breach {
		fake.breach = map[string]bool{"B2": true}
	}
	evidence, err := driveFake(t, fake)
	return fake, evidence, err
}

func TestConformRunsThreePhasesAndKeepsTheEvidence(t *testing.T) {
	fake, evidence, err := driveIsolation(t, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.triggered, ",") != "run-1,run-2,run-3" || !fake.cancelled["run-1"] || !fake.cancelled["run-2"] || fake.cancelled["run-3"] {
		t.Fatalf("triggered %v, cancelled %v", fake.triggered, fake.cancelled)
	}
	report, err := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
	if err != nil || !strings.HasPrefix(string(report), "# Cloud isolation conformance: PASS") || !strings.Contains(string(report), "## Gaps (not-probed)") ||
		!strings.Contains(string(report), "trust holds only because the kube API grade passed") {
		t.Fatalf("report = %.300s, %v", report, err)
	}
	for _, name := range []string{
		"preflight.json", "targets.json", "plans/B2.json", "runs/A1-probe.log", "runs/B1-status.json", "runs/B1-status-ready.json",
		"runs/B1-status-probed.json", "runs/B1-status-end.json", "cluster/phases/B1-ready.json", "cluster/phases/B2-probed.json",
		"results.ndjson", "evaluation.json", "runs/B1-status.ndjson", "cluster/observations.ndjson",
		"cluster/nodes.json", "cluster/nodeclaims.json", "cluster/networkpolicies.json", "SHA256SUMS",
	} {
		if _, err := os.Stat(filepath.Join(evidence, name)); err != nil {
			t.Errorf("evidence %s: %v", name, err)
		}
	}
	for _, c := range fake.commands {
		if strings.HasPrefix(c, "kubectl ") && !strings.HasPrefix(c, "kubectl --context staging-read get ") && !strings.HasPrefix(c, "kubectl config view") {
			t.Errorf("kubectl call outside read-only gets: %s", c)
		}
	}
}

// runs logs exits 3 with the records it has while a run is live, so the anchor's markers are read
// during its hold. Any other nonzero exit or a marker that does not parse stops the wait at once
// instead of polling until the budget runs out.
func TestConformReadsTheLogsOfALiveRun(t *testing.T) {
	fake := newIsoFake(t)
	if _, err := driveFake(t, fake); err != nil || strings.Join(fake.triggered, ",") != "run-1,run-2,run-3" {
		t.Fatalf("conform = %v, triggered %v; want every phase run from live logs", err, fake.triggered)
	}
	for name, c := range map[string]struct {
		edit func(*isoClusterFake)
		want string
	}{
		"a refused read":   {func(f *isoClusterFake) { f.logExit = map[string]int{"B1": 4} }, "exited 4"},
		"a garbled marker": {func(f *isoClusterFake) { f.garble = map[string]bool{"B1": true} }, strings.TrimSpace(isolationprobe.ResultMarker)},
	} {
		fake := newIsoFake(t)
		c.edit(fake)
		evidence, err := driveFake(t, fake)
		report, rerr := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
		stop := "the sequence stopped: wait for B1 to announce its listener: "
		if err == nil || rerr != nil || !strings.Contains(string(report), stop) || strings.Contains(string(report), " within ") || len(fake.triggered) != 1 {
			t.Errorf("%s: conform = %v, triggered %v, report = %.900s, %v; want an immediate stop", name, err, fake.triggered, report, rerr)
			continue
		}
		if line := string(report)[strings.Index(string(report), stop):]; !strings.Contains(line[:strings.IndexByte(line+"\n", '\n')], c.want) {
			t.Errorf("%s: stop reason %.300q does not name %q", name, line, c.want)
		}
	}
}

// A probe pod counts as running only while the latest observation still shows it; the record it
// leaves behind keeps the phase it last had.
func TestProbeRunningNeedsTheLatestObservation(t *testing.T) {
	l := newPlacementLog()
	var p runPod
	p.Metadata.Name, p.JobID, p.Status.Phase = "B1-probe", isolationprobe.ProbeJob, "Running"
	l.samples = append(l.samples, placementSample{pods: map[string][]runPod{"B1": {p}}})
	if !l.probeRunning("B1") {
		t.Fatal("a running probe pod in the latest observation is not running")
	}
	l.samples = append(l.samples, placementSample{pods: map[string][]runPod{}})
	if l.probeRunning("B1") {
		t.Fatal("a probe pod gone from the latest observation still counts as running")
	}
	if len(l.samples[0].pods["B1"]) != 1 {
		t.Fatal("a later observation replaced an earlier one")
	}
}

// The launcher deletes a run's Jobs and pods when the run ends; placement recorded while the runs
// were live still judges every run.
func TestConformJudgesPlacementRecordedBeforeTeardown(t *testing.T) {
	fake := newIsoFake(t)
	fake.teardown = true
	if _, err := driveFake(t, fake); err != nil {
		t.Fatalf("healthy runs failed once the launcher tore them down: %v", err)
	}
}

func TestConformFailsOnABreachAndStillWritesTheReport(t *testing.T) {
	_, evidence, err := driveIsolation(t, true)
	if err == nil || !strings.Contains(err.Error(), "C4") {
		t.Fatalf("conform = %v, want a C4 failure", err)
	}
	report, rerr := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
	if rerr != nil || !strings.HasPrefix(string(report), "# Cloud isolation conformance: FAIL") || !strings.Contains(string(report), "B2 dind-bridge BREACH: IMDS") {
		t.Fatalf("report = %.600s, %v", report, rerr)
	}
}

// A breach or a missing result in an anchor stops the sequence before another hostile job starts,
// cancels the anchor and still reports.
func TestConformStopsAtTheFirstRunThatFails(t *testing.T) {
	for name, edit := range map[string]func(*isoClusterFake){
		"a breach":         func(f *isoClusterFake) { f.breach = map[string]bool{"B1": true} },
		"a missing result": func(f *isoClusterFake) { f.omit = map[string]bool{"B1": true} },
	} {
		fake := newIsoFake(t)
		edit(fake)
		evidence, err := driveFake(t, fake)
		if err == nil || len(fake.triggered) != 1 || !fake.cancelled["run-1"] {
			t.Errorf("%s: conform = %v, triggered %v, cancelled %v", name, err, fake.triggered, fake.cancelled)
			continue
		}
		report, rerr := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
		if rerr != nil || !strings.Contains(string(report), "A1 was not run") || !strings.Contains(string(report), "the sequence stopped") {
			t.Errorf("%s: report = %.600s, %v", name, report, rerr)
		}
	}
}

// Each plan aims the kube API grade at the job and controller namespaces, at a controller pod and at
// every peer's probe pod; the job adds its own pod when it runs.
func TestPlansAimTheKubeAPIGradeAtEveryNamespaceAndPod(t *testing.T) {
	fake, _, err := driveIsolation(t, false)
	if err != nil {
		t.Fatal(err)
	}
	for run, want := range map[string]string{
		"run-1": "sparkwing-staging/controller",
		"run-2": "sparkwing-staging/controller " + isoJobNS + "/B1-probe",
		"run-3": "sparkwing-staging/controller " + isoJobNS + "/A1-probe " + isoJobNS + "/B1-probe",
	} {
		plan := fake.plans[run]
		var pods []string
		for _, p := range plan.KubePods {
			pods = append(pods, p.Namespace+"/"+p.Name)
		}
		if strings.Join(pods, " ") != want || plan.JobNamespace != isoJobNS || strings.Join(plan.KubeNamespaces, ",") != isoJobNS+",sparkwing-staging" {
			t.Errorf("%s plan aims the kube API grade at pods %v in %v (job namespace %q), want %s", run, pods, plan.KubeNamespaces, plan.JobNamespace, want)
		}
	}
}

// The anchors keep the fixture's 20-minute limit unless the run raises it, and only the anchors get
// the raised limit; each anchor holds until two minutes before its limit.
func TestConformGivesOnlyTheAnchorsTheRaisedLimit(t *testing.T) {
	fake := newIsoFake(t)
	if _, err := driveFake(t, fake); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.limits, "; "); got != "B1 timeout=20m0s hold=18m0s; A1 timeout=20m0s hold=18m0s; B2 timeout=20m0s hold=0s" {
		t.Fatalf("default limits = %s", got)
	}
	raised := newIsoFake(t)
	args := isoArgs()
	args.AnchorTimeout = "35m"
	evidence, err := driveFakeWith(t, raised, args)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(raised.limits, "; "); got != "B1 timeout=35m0s hold=33m0s; A1 timeout=35m0s hold=33m0s; B2 timeout=20m0s hold=0s" {
		t.Fatalf("raised limits = %s", got)
	}
	if report, err := os.ReadFile(filepath.Join(evidence, "REPORT.md")); err != nil || !strings.Contains(string(report), "anchor job limit 35m0s") {
		t.Fatalf("report does not name the raised limit: %v", err)
	}
}

// A read the driver needs to record placement fails during discovery, before any run starts.
func TestConformChecksPlacementReadsBeforeAnyRun(t *testing.T) {
	for _, denied := range []string{"get nodeclaims", "--field-selector", "get daemonsets"} {
		fake := newIsoFake(t)
		fake.deny = denied
		if _, err := driveFake(t, fake); err == nil || len(fake.triggered) != 0 {
			t.Errorf("%s denied: conform = %v, triggered %v", denied, err, fake.triggered)
		}
	}
}

// Both profiles must point at staging and authenticate as their own teams before any upload.
func TestConformRefusesAProfileOffStagingOrAsAnotherTeam(t *testing.T) {
	for name, edit := range map[string]func(*isoClusterFake){
		"production controller": func(f *isoClusterFake) {
			f.controller = map[string]string{"stg-iso-a": "https://api.sparkwing.dev"}
		},
		"another team's token": func(f *isoClusterFake) { f.whoami = map[string]string{"stg-iso-b": "stg-iso-a"} },
	} {
		fake := newIsoFake(t)
		edit(fake)
		if _, err := driveFake(t, fake); err == nil || len(fake.triggered) != 0 {
			t.Errorf("%s: conform = %v, triggered %v", name, err, fake.triggered)
		}
	}
}
