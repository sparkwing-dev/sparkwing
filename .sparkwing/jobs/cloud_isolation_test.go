package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sparkwing-pipelines/isolationprobe"
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
		TeamA: "stg-iso-a", TeamB: "stg-iso-b", ControllerURL: "https://staging.sparkwing.dev", KubeContext: "kikd-prod",
		PublicContext: "kikd-prod", ControllerNS: "sparkwing-staging", JobNamespace: isoJobNS, InfraNodePool: "staging-infra",
		IngressNS: "traefik", VPCResolver: "10.0.0.2", Database: "db.internal:5432",
	}
}

func TestCloudIsolationArgsRefuseAnIncompleteDeployment(t *testing.T) {
	if err := isoArgs().validate(); err != nil {
		t.Fatal(err)
	}
	same := isoArgs()
	same.TeamB = same.TeamA
	empty := CloudIsolationArgs{}
	for name, c := range map[string]struct {
		args CloudIsolationArgs
		want string
	}{
		"one team":     {same, "different teams"},
		"no targets":   {empty, "--job-namespace is required"},
		"no endpoint ": {empty, "--public-api-context is required"},
	} {
		if err := c.args.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
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
	}{[]string{"10.0.62.116"}}, struct {
		Addresses []string `json:"addresses"`
	}{[]string{"10.0.71.108"}})
	return targetInputs{
		ControllerPods:     []kubePod{isoPod("controller", "10.0.59.130", true, 4343, 9091), isoPod("launcher", "10.0.59.131", false, 8080)},
		ControllerServices: []kubeService{isoService("controller", "172.20.10.1", 80)},
		DNSPods:            []kubePod{isoPod("coredns", "10.0.62.166", true, 53, 9153)},
		DNSServices:        []kubeService{isoService("kube-dns", "172.20.0.10", 53, 9153)},
		APIService:         isoService("kubernetes", "172.20.0.1", 443),
		APIEndpoints:       []kubeEndpointSlice{slice},
		InfraNodes:         []kubeNode{isoNode("ip-10-0-51-209", "10.0.51.209")},
		IngressPods:        []kubePod{isoPod("traefik", "10.0.62.161", true, 8000)},
		PublicAPI:          "example.gr7.us-west-2.eks.amazonaws.com",
		Database:           "db.internal:5432",
		VPCResolver:        "10.0.0.2",
	}
}

func TestStaticTargetsCoverEveryItemFromTheCluster(t *testing.T) {
	targets, err := staticTargets(isoInputs())
	if err != nil {
		t.Fatal(err)
	}
	plan := isolationprobe.Plan{ControllerURL: "https://staging.sparkwing.dev", ListenPort: 18080, Canary: "c", Targets: targets}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	byAddr := map[string]isolationprobe.Target{}
	for _, tg := range targets {
		byAddr[tg.Address] = tg
	}
	for addr, want := range map[string]isolationprobe.Target{
		"10.0.59.130:4343":   {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.0.59.131:8080":   {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: false},
		"172.20.10.1:80":     {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.0.62.166:9153":   {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"172.20.0.10:9153":   {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.0.51.209:10250":  {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true, Grade: isolationprobe.GradeKubelet},
		"10.0.62.161:8000":   {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"db.internal:5432":   {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.0.0.2:53":        {Item: "C6", Expect: isolationprobe.ExpectBlocked, Listening: true},
		"10.100.0.1:443":     {Item: "C6", Expect: isolationprobe.ExpectBlocked},
		"169.254.169.254:80": {Item: "C4", Expect: isolationprobe.ExpectBlocked, Listening: true, Grade: isolationprobe.GradeIMDS},
		"[fd00:ec2::254]:80": {Item: "C5", Expect: isolationprobe.ExpectBlocked, Grade: isolationprobe.GradeIMDS},
		"172.20.0.1:443":     {Item: "C7", Expect: isolationprobe.ExpectBlocked, Listening: true, Grade: isolationprobe.GradeKubeAPI},
		"10.0.71.108:443":    {Item: "C7", Expect: isolationprobe.ExpectBlocked, Listening: true, Grade: isolationprobe.GradeKubeAPI},
		"example.gr7.us-west-2.eks.amazonaws.com:443": {Item: "C7", Expect: isolationprobe.ExpectDenied, Listening: true, Grade: isolationprobe.GradeKubeAPI},
	} {
		got, ok := byAddr[addr]
		if !ok || got.Item != want.Item || got.Expect != want.Expect || got.Listening != want.Listening || got.Grade != want.Grade {
			t.Errorf("%s = %+v, want %+v", addr, got, want)
		}
	}
	if _, ok := byAddr["10.0.62.166:53"]; ok {
		t.Error("cluster DNS's own port 53 is a target; the fence allows it")
	}
	noEndpoints := isoInputs()
	noEndpoints.APIEndpoints = nil
	if _, err := staticTargets(noEndpoints); err == nil {
		t.Error("discovery passed without the API's private endpoints")
	}
	noPublic := isoInputs()
	noPublic.PublicAPI = ""
	if _, err := staticTargets(noPublic); err == nil {
		t.Error("discovery passed without the public API endpoint")
	}
}

func TestContextServerHostReadsTheRedactedKubeconfig(t *testing.T) {
	config := `{"contexts":[{"name":"kikd-prod","context":{"cluster":"arn:aws:eks:us-west-2:1:cluster/kikd-prod","user":"u"}},{"name":"proxy","context":{"cluster":"proxy"}}],
"clusters":[{"name":"arn:aws:eks:us-west-2:1:cluster/kikd-prod","cluster":{"server":"https://ABC.gr7.us-west-2.eks.amazonaws.com","certificate-authority-data":"DATA+OMITTED"}},{"name":"proxy","cluster":{"server":"https://proxy.example.ts.net"}}]}`
	if host, err := contextServerHost(config, "kikd-prod"); err != nil || host != "ABC.gr7.us-west-2.eks.amazonaws.com" {
		t.Fatalf("host = %q, %v", host, err)
	}
	if _, err := contextServerHost(config, "missing"); err == nil {
		t.Fatal("an unknown context named a host")
	}
}

func isoJob(name, run, job, team string) kubeJob {
	var j kubeJob
	j.Metadata.Name = name
	binding, _ := json.Marshal(awardBinding{Kind: "work", Team: team, RunID: run, NodeID: job})
	j.Metadata.Annotations = map[string]string{awardBindingAnnotation: string(binding)}
	return j
}

func TestPodsOfRunFollowsTheAwardBinding(t *testing.T) {
	jobs := []kubeJob{isoJob("sw-1", "run-1", "probe", "stg-iso-a"), isoJob("sw-2", "run-1", "output-sibling", "stg-iso-a"), isoJob("sw-3", "run-2", "probe", "stg-iso-b")}
	var pods []kubePod
	for i, job := range []string{"sw-1", "sw-2", "sw-3", "sw-unrelated"} {
		p := isoPod(fmt.Sprintf("pod-%d", i), fmt.Sprintf("10.0.0.%d", i), true)
		p.Metadata.Labels = map[string]string{jobPodLabel: job}
		pods = append(pods, p)
	}
	got := podsOfRun(jobs, pods, "run-1")
	if len(got) != 2 || got[0].JobID != "probe" || got[1].JobID != "output-sibling" {
		t.Fatalf("run-1 pods = %+v", got)
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
		Privileged   *bool `json:"privileged"`
		Capabilities *struct {
			Add []string `json:"add"`
		} `json:"capabilities"`
		SeccompProfile *struct {
			Type             string `json:"type"`
			LocalhostProfile string `json:"localhostProfile"`
		} `json:"seccompProfile"`
		ProcMount string `json:"procMount"`
	}{
		Capabilities: &struct {
			Add []string `json:"add"`
		}{[]string{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE"}},
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
	} {
		p := launcherPod()
		edit(&p)
		if got := podSpecFindings(p); len(got) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
}

func fullResults(label string, peers map[string]string) []isolationprobe.Result {
	var out []isolationprobe.Result
	for ctx, names := range requiredProbes {
		for _, name := range names {
			out = append(out, isolationprobe.Result{Item: requiredItem(name), Context: ctx, Probe: name, Verdict: isolationprobe.Pass})
		}
		for peer, team := range peers {
			out = append(out, isolationprobe.Result{Item: "C9", Context: ctx, Probe: "peer " + peer + " (" + team + ") listener", Verdict: isolationprobe.Pass})
		}
	}
	return out
}

type isoRunFixture struct {
	label, team, run, node, podIP, hostIP string
	peers                                 map[string]string
}

var isoFixtures = []isoRunFixture{
	{"B1", "stg-iso-b", "run-1", "node-b1", "10.0.10.11", "10.0.10.1", nil},
	{"A1", "stg-iso-a", "run-2", "node-a1", "10.0.20.21", "10.0.20.1", map[string]string{"B1": "stg-iso-b"}},
	{"B2", "stg-iso-b", "run-3", "node-b2", "10.0.30.31", "10.0.30.1", map[string]string{"A1": "stg-iso-a", "B1": "stg-iso-b"}},
}

func evalFixture() ([]isolationRun, map[string]isolationNode) {
	var runs []isolationRun
	nodes := map[string]isolationNode{}
	for _, f := range isoFixtures {
		var peers []string
		for p := range f.peers {
			peers = append(peers, p)
		}
		slices.Sort(peers)
		runs = append(runs, isolationRun{
			Label: f.label, Team: f.team, RunTeam: f.team, RunID: f.run, Peers: peers, Results: fullResults(f.label, f.peers),
			Ready:   []isolationprobe.Ready{{PodIPs: []string{f.podIP}, ListenPort: 18080, NodeIP: f.hostIP}},
			Summary: []isolationprobe.Summary{{Pass: 1, OK: true}},
			Pods:    []isolationPod{{Name: f.label + "-probe", Job: "probe", Node: f.node, PodIP: f.podIP, HostIP: f.hostIP}, {Name: f.label + "-sibling", Job: "output-sibling", Node: f.node}},
		})
		value := teamNodeValue(isoJobNS, f.team)
		nodes[f.node] = isolationNode{Name: f.node, IPs: []string{f.hostIP}, TeamNode: value, UsedTeam: value, UsedRun: "sha256-" + f.run}
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

func TestEvaluateIsolationPassesACompleteRefusedRun(t *testing.T) {
	runs, nodes := evalFixture()
	if e := evaluateIsolation(isoJobNS, runs, nodes, map[string]bool{"A1": true, "B1": true}); !e.OK {
		t.Fatalf("complete run failed: %+v", e)
	}
}

// Each way a run can fall short fails exactly the item it undermines.
func TestEvaluateIsolationFailsTheItemEachShortfallUndermines(t *testing.T) {
	alive := map[string]bool{"A1": true, "B1": true}
	for name, c := range map[string]struct {
		edit  func([]isolationRun, map[string]isolationNode, map[string]bool)
		items []string
	}{
		"a breach": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[1].Results = append(r[1].Results, isolationprobe.Result{Item: "C4", Context: "job", Probe: "IMDS", Verdict: isolationprobe.Breach})
		}, []string{"C4"}},
		"a probe that proved nothing": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Results = append(r[0].Results, isolationprobe.Result{Item: "C10", Context: "dind-privileged", Probe: "mount audit", Verdict: isolationprobe.Inconclusive})
		}, []string{"C10"}},
		"a missing context": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[2].Results = slices.DeleteFunc(r[2].Results, func(x isolationprobe.Result) bool { return x.Context == isolationprobe.ContextPrivileged })
		}, []string{"C3", "C4", "C9", "C10", "C11", "C12"}},
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
		"a peer gone before it was probed": {func(_ []isolationRun, _ map[string]isolationNode, a map[string]bool) {
			a["B1"] = false
		}, []string{"C9"}},
		"the wrong team": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[1].RunTeam = "stg-iso-b"
		}, []string{"C1"}},
		"own node misidentified": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Ready[0].NodeIP = "10.0.99.1"
		}, []string{"C8"}},
		"a pod spec drift": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Pods[0].Spec = []string{"hostUsers is not false"}
		}, []string{"C3"}},
		"no summary": {func(r []isolationRun, _ map[string]isolationNode, _ map[string]bool) {
			r[0].Summary = nil
		}, []string{"C12"}},
	} {
		runs, nodes := evalFixture()
		a := copyAlive(alive)
		c.edit(runs, nodes, a)
		e := evaluateIsolation(isoJobNS, runs, nodes, a)
		if e.OK || strings.Join(failedItems(e), ",") != strings.Join(c.items, ",") {
			t.Errorf("%s: failed %v, want %v", name, failedItems(e), c.items)
		}
	}
}

func copyAlive(m map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

type isoClusterFake struct {
	t         *testing.T
	mu        sync.Mutex
	triggered []string
	cancelled map[string]bool
	breach    bool
	statusN   map[string]int
	commands  []string
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
			return `{"contexts":[{"name":"kikd-prod","context":{"cluster":"c"}}],"clusters":[{"name":"c","cluster":{"server":"https://public.example:443"}}]}`, 0, nil
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

func (f *isoClusterFake) kubectl(args []string) (string, int, error) {
	kind, ns := args[1], flagValue(args, "-n")
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
	case kind == "pods" && ns == "traefik":
		return list(isoInputs().IngressPods), 0, nil
	case kind == "nodes" && flagValue(args, "-l") != "":
		return list(isoInputs().InfraNodes), 0, nil
	case kind == "nodes":
		var nodes []kubeNode
		for _, fx := range isoFixtures {
			n := isoNode(fx.node, fx.hostIP)
			value := teamNodeValue(isoJobNS, fx.team)
			n.Metadata.Labels = map[string]string{teamNodeLabel: value, usedTeamNodeLabel: value, usedRunNodeLabel: "sha256-" + fx.run}
			nodes = append(nodes, n)
		}
		return list(nodes), 0, nil
	case kind == "jobs":
		var jobs []kubeJob
		for _, fx := range isoFixtures[:len(f.triggered)] {
			jobs = append(jobs, isoJob("sw-"+fx.run, fx.run, "probe", fx.team))
		}
		return list(jobs), 0, nil
	case kind == "pods" && ns == isoJobNS:
		var pods []kubePod
		for _, fx := range isoFixtures[:len(f.triggered)] {
			p := launcherPod()
			p.Metadata.Name, p.Metadata.Labels = fx.label+"-probe", map[string]string{jobPodLabel: "sw-" + fx.run}
			p.Spec.NodeName, p.Status.PodIP, p.Status.HostIP, p.Status.Phase = fx.node, fx.podIP, fx.hostIP, "Running"
			if f.cancelled[fx.run] {
				p.Status.Phase = "Succeeded"
			}
			pods = append(pods, p)
		}
		return list(pods), 0, nil
	case kind == "networkpolicies" || kind == "validatingadmissionpolicybindings":
		return `{"items":[]}`, 0, nil
	}
	f.t.Errorf("unexpected kubectl %v", args)
	return "", 1, nil
}

func (f *isoClusterFake) sparkwing(args []string) (string, int, error) {
	run := flagValue(args, "--run")
	switch {
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
		f.triggered = append(f.triggered, fx.run)
		return `{"run_id":"` + fx.run + `"}`, 0, nil
	case args[0] == "runs" && args[1] == "status":
		f.statusN[run]++
		fx := fixtureFor(run)
		status := "running"
		switch {
		case f.cancelled[run]:
			status = "cancelled"
		case fx.label == "B2" && f.statusN[run] > 1:
			status = "success"
		}
		code := 3
		if terminal(status) {
			code = 0
		}
		return `{"id":"` + run + `","team":"` + fx.team + `","status":"` + status + `"}`, code, nil
	case args[0] == "runs" && args[1] == "logs":
		fx := fixtureFor(run)
		var b strings.Builder
		b.WriteString("probe | " + isolationprobe.FormatLine(isolationprobe.ReadyMarker, isolationprobe.Ready{PodIPs: []string{fx.podIP}, ListenPort: 18080, NodeIP: fx.hostIP}) + "\n")
		results := fullResults(fx.label, fx.peers)
		if f.breach && fx.label == "B2" {
			results = append(results, isolationprobe.Result{Item: "C4", Context: "dind-bridge", Probe: "IMDS", Target: "169.254.169.254:80", Outcome: "connected", Verdict: isolationprobe.Breach})
		}
		for _, r := range results {
			b.WriteString("probe | " + isolationprobe.FormatLine(isolationprobe.ResultMarker, r) + "\n")
		}
		b.WriteString(isolationprobe.FormatLine(isolationprobe.SummaryMarker, isolationprobe.Summarize(results)) + "\n")
		return b.String(), 0, nil
	case args[0] == "runs" && args[1] == "cancel":
		f.cancelled[run] = true
		return "{}", 0, nil
	}
	f.t.Errorf("unexpected sparkwing %v", args)
	return "", 1, nil
}

func fixtureFor(run string) isoRunFixture {
	for _, fx := range isoFixtures {
		if fx.run == run {
			return fx
		}
	}
	return isoRunFixture{}
}

func driveIsolation(t *testing.T, breach bool) (*isoClusterFake, string, error) {
	t.Helper()
	root, err := mainModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	fake := &isoClusterFake{t: t, cancelled: map[string]bool{}, statusN: map[string]int{}, breach: breach}
	clock := time.Unix(0, 0)
	evidence := t.TempDir()
	d := &isolationDriver{args: isoArgs(), repo: root, tmp: t.TempDir(), evidence: evidence, tools: isolationTools{
		run:  fake.run,
		wait: func(context.Context, time.Duration) error { clock = clock.Add(pollInterval); return nil },
		now:  func() time.Time { return clock },
		say:  func(context.Context, string) {},
	}}
	return fake, evidence, d.conform(t.Context())
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
	if err != nil || !strings.HasPrefix(string(report), "# Cloud isolation conformance: PASS") {
		t.Fatalf("report = %.200s, %v", report, err)
	}
	for _, name := range []string{"targets.json", "plans/B2.json", "runs/A1-probe.log", "results.ndjson", "evaluation.json", "cluster/pods-B1.json", "cluster/networkpolicies.json", "SHA256SUMS"} {
		if _, err := os.Stat(filepath.Join(evidence, name)); err != nil {
			t.Errorf("evidence %s: %v", name, err)
		}
	}
	for _, c := range fake.commands {
		if strings.HasPrefix(c, "kubectl ") && !strings.HasPrefix(c, "kubectl --context kikd-prod get ") && !strings.HasPrefix(c, "kubectl config view") {
			t.Errorf("kubectl call outside read-only gets: %s", c)
		}
	}
}

func TestConformFailsOnABreachAndStillWritesTheReport(t *testing.T) {
	_, evidence, err := driveIsolation(t, true)
	if err == nil || !strings.Contains(err.Error(), "C4") {
		t.Fatalf("conform = %v, want a C4 failure", err)
	}
	report, rerr := os.ReadFile(filepath.Join(evidence, "REPORT.md"))
	if rerr != nil || !strings.HasPrefix(string(report), "# Cloud isolation conformance: FAIL") || !strings.Contains(string(report), "B2 dind-bridge BREACH: IMDS") {
		t.Fatalf("report = %.400s, %v", report, rerr)
	}
}
