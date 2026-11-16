package jobs

import (
	"fmt"
	"slices"
	"sort"
	"sparkwing-pipelines/isolationprobe"
	"strings"
)

type isolationRun struct {
	Label   string                   `json:"label"`
	Team    string                   `json:"team"`
	Role    string                   `json:"role"`
	RunID   string                   `json:"run_id"`
	Status  string                   `json:"status"`
	RunTeam string                   `json:"run_team"`
	Peers   []string                 `json:"peers"`
	Results []isolationprobe.Result  `json:"-"`
	Ready   []isolationprobe.Ready   `json:"ready"`
	Summary []isolationprobe.Summary `json:"summary"`
	Pods    []isolationPod           `json:"pods"`
}

type isolationPod struct {
	Name   string   `json:"name"`
	Job    string   `json:"job"`
	Node   string   `json:"node"`
	PodIP  string   `json:"pod_ip"`
	HostIP string   `json:"host_ip"`
	Phase  string   `json:"phase"`
	Team   string   `json:"team"`
	Spec   []string `json:"spec_findings,omitempty"`
}

type isolationNode struct {
	Name     string   `json:"name"`
	IPs      []string `json:"ips"`
	TeamNode string   `json:"team_node"`
	UsedTeam string   `json:"used_team"`
	UsedRun  string   `json:"used_run"`
}

type itemVerdict struct {
	Item     string   `json:"item"`
	Title    string   `json:"title"`
	OK       bool     `json:"ok"`
	Failures []string `json:"failures,omitempty"`
}

type isolationEvaluation struct {
	OK    bool          `json:"ok"`
	Items []itemVerdict `json:"items"`
}

var isolationItems = []struct{ id, title string }{
	{"C1", "Two staging teams"},
	{"C2", "A dedicated node per team and run"},
	{"C3", "The launcher builds the designed pod"},
	{"C4", "IPv4 metadata"},
	{"C5", "IPv6 metadata"},
	{"C6", "The 10/8 range, including the controller pod IP"},
	{"C7", "The kube API, private and public"},
	{"C8", "Another team's node and kubelet"},
	{"C9", "Other pods' Docker daemons"},
	{"C10", "The node's filesystem outside scratch"},
	{"C11", "The host network"},
	{"C12", "DinD capabilities and /proc"},
}

// safety: every context must report the probes that settle its items, so a run that skipped a
// context, crashed early or lost lines fails coverage instead of passing on what it did report.
var requiredProbes = map[string][]string{
	isolationprobe.ContextJob: {
		"user namespace maps root away from host root", "seccomp filter applied", "mount audit", "IMDS",
		"IMDS over IPv6", "kube API service", "kube API public endpoint", "own listener answers on the pod address",
		"own Docker daemon answers", "runtime sockets outside scratch", "peer canary files", "block devices in /dev",
		"credential-named environment variable",
	},
	isolationprobe.ContextBridge: {"public HTTPS answers", "IMDS", "IMDS over IPv6", "kube API service"},
	isolationprobe.ContextPrivileged: {
		"user namespace maps root away from host root", "seccomp filter applied", "mount audit", "IMDS",
		"node markers under /host", "node markers under /proc/1/root", "host PID namespace shows no node process",
		"host network is the pod's network namespace", "kernel file opens for writing", "cgroup v1 hierarchy mounts",
		"tmpfs mounts inside the pod's namespace", "NET_ADMIN works inside the pod's namespace", "seccomp refuses bpf",
		"block device node creation",
	},
}

func evaluateIsolation(namespace string, runs []isolationRun, nodes map[string]isolationNode, anchorsAlive map[string]bool) isolationEvaluation {
	fails := map[string][]string{}
	add := func(item, format string, args ...any) {
		fails[item] = append(fails[item], fmt.Sprintf(format, args...))
	}

	teams := map[string]bool{}
	for _, r := range runs {
		teams[r.Team] = true
		if r.RunTeam != "" && r.RunTeam != r.Team {
			add("C1", "%s ran as team %s, not %s", r.Label, r.RunTeam, r.Team)
		}
	}
	if len(teams) != 2 {
		add("C1", "runs used %d teams, want 2", len(teams))
	}

	nodeRun, fenceRun := map[string]string{}, map[string]string{}
	for _, r := range runs {
		probe := probePod(r)
		if probe == nil {
			add("C2", "%s has no probe pod in %s", r.Label, namespace)
			continue
		}
		for _, p := range r.Pods {
			if other, ok := nodeRun[p.Node]; ok && other != r.Label {
				add("C2", "%s and %s share node %s", other, r.Label, p.Node)
			}
			nodeRun[p.Node] = r.Label
			n, ok := nodes[p.Node]
			want := teamNodeValue(namespace, r.Team)
			switch {
			case !ok:
				add("C2", "%s pod %s runs on unknown node %s", r.Label, p.Name, p.Node)
			case n.TeamNode != want || n.UsedTeam != want:
				add("C2", "%s node %s carries team labels %q/%q, want %q", r.Label, p.Node, n.TeamNode, n.UsedTeam, want)
			case n.UsedRun == "":
				add("C2", "%s node %s has no used-run fence", r.Label, p.Node)
			case fenceRun[n.UsedRun] != "" && fenceRun[n.UsedRun] != r.Label:
				add("C2", "%s and %s carry the same used-run fence", fenceRun[n.UsedRun], r.Label)
			default:
				fenceRun[n.UsedRun] = r.Label
			}
			for _, s := range p.Spec {
				add("C3", "%s pod %s: %s", r.Label, p.Name, s)
			}
		}
		if len(r.Ready) == 0 {
			add("C9", "%s never announced its listener", r.Label)
			continue
		}
		ready := r.Ready[0]
		if !slices.Contains(ready.PodIPs, probe.PodIP) {
			add("C11", "%s reported pod addresses %v, but its pod is %s", r.Label, ready.PodIPs, probe.PodIP)
		}
		if probe.PodIP == probe.HostIP || slices.Contains(ready.PodIPs, probe.HostIP) {
			add("C11", "%s runs with its node's address %s", r.Label, probe.HostIP)
		}
		if ready.NodeIP != "" {
			if n, ok := nodes[probe.Node]; !ok || !slices.Contains(n.IPs, ready.NodeIP) {
				add("C8", "%s discovered %s as its node, but it runs on %s", r.Label, ready.NodeIP, probe.Node)
			}
		}
	}

	for _, r := range runs {
		if len(r.Summary) == 0 {
			add("C12", "%s reported no summary; its probes did not finish", r.Label)
		}
		seen := map[string]map[string]bool{}
		for _, res := range r.Results {
			if seen[res.Context] == nil {
				seen[res.Context] = map[string]bool{}
			}
			seen[res.Context][res.Probe] = true
			if res.Verdict.Fails() {
				add(itemOf(res.Item), "%s %s %s: %s %s (%s)", r.Label, res.Context, res.Verdict, res.Probe, res.Target, res.Outcome)
			}
		}
		for ctx, names := range requiredProbes {
			for _, name := range names {
				if !seen[ctx][name] {
					add(requiredItem(name), "%s %s never reported %q", r.Label, ctx, name)
				}
			}
		}
		for _, peer := range r.Peers {
			for _, ctx := range []string{isolationprobe.ContextJob, isolationprobe.ContextBridge, isolationprobe.ContextPrivileged} {
				if !seenPeerListener(r.Results, ctx, peer) {
					add("C9", "%s %s never probed peer %s's listener", r.Label, ctx, peer)
				}
			}
			if !anchorsAlive[peer] {
				add("C9", "peer %s was not running after %s probed it, so its listener proves nothing", peer, r.Label)
			}
		}
	}

	eval := isolationEvaluation{OK: true}
	for _, it := range isolationItems {
		v := itemVerdict{Item: it.id, Title: it.title, OK: len(fails[it.id]) == 0, Failures: fails[it.id]}
		sort.Strings(v.Failures)
		eval.OK = eval.OK && v.OK
		eval.Items = append(eval.Items, v)
	}
	return eval
}

func probePod(r isolationRun) *isolationPod {
	for i := range r.Pods {
		if r.Pods[i].Job == isolationprobe.ProbeJob {
			return &r.Pods[i]
		}
	}
	return nil
}

func seenPeerListener(results []isolationprobe.Result, ctx, peer string) bool {
	for _, r := range results {
		if r.Context == ctx && strings.HasPrefix(r.Probe, "peer "+peer+" ") && strings.HasSuffix(r.Probe, " listener") {
			return true
		}
	}
	return false
}

func itemOf(item string) string {
	for _, it := range isolationItems {
		if it.id == item {
			return item
		}
	}
	return "C12"
}

func requiredItem(probe string) string {
	switch {
	case strings.Contains(probe, "IPv6"):
		return "C5"
	case strings.HasPrefix(probe, "IMDS"):
		return "C4"
	case strings.Contains(probe, "kube API"):
		return "C7"
	case strings.Contains(probe, "listener") || strings.Contains(probe, "Docker") || strings.Contains(probe, "sockets"):
		return "C9"
	case strings.Contains(probe, "mount audit") || strings.Contains(probe, "markers") || strings.Contains(probe, "canary") || strings.Contains(probe, "block"):
		return "C10"
	case strings.Contains(probe, "network namespace"):
		return "C11"
	case strings.Contains(probe, "public"):
		return "C6"
	case strings.Contains(probe, "user namespace") || strings.Contains(probe, "credential"):
		return "C3"
	default:
		return "C12"
	}
}

// safety: the pod spec is checked as the launcher submitted it, so a drift between the code and
// the deployed launcher shows here even when every runtime probe passes.
func podSpecFindings(p kubePod) []string {
	var out []string
	if p.Spec.HostUsers == nil || *p.Spec.HostUsers {
		out = append(out, "hostUsers is not false")
	}
	if p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken {
		out = append(out, "a service-account token is mounted")
	}
	if p.Spec.HostNetwork || p.Spec.HostPID || p.Spec.HostIPC {
		out = append(out, "a host namespace is shared")
	}
	for _, v := range p.Spec.Volumes {
		if v.HostPath != nil {
			out = append(out, "hostPath volume "+v.Name)
		}
	}
	for _, c := range append(slices.Clone(p.Spec.InitContainers), p.Spec.Containers...) {
		sc := c.SecurityContext
		if sc != nil && sc.Privileged != nil && *sc.Privileged {
			out = append(out, c.Name+" is privileged")
		}
		if c.Name != "docker" {
			if sc != nil && sc.ProcMount == "Unmasked" {
				out = append(out, c.Name+" mounts /proc unmasked")
			}
			if sc != nil && sc.Capabilities != nil && len(sc.Capabilities.Add) > 0 {
				out = append(out, c.Name+" adds capabilities "+strings.Join(sc.Capabilities.Add, ","))
			}
			continue
		}
		var caps []string
		if sc != nil && sc.Capabilities != nil {
			caps = slices.Sorted(slices.Values(sc.Capabilities.Add))
		}
		if strings.Join(caps, ",") != "NET_ADMIN,SYS_ADMIN,SYS_PTRACE" {
			out = append(out, "docker adds capabilities "+strings.Join(caps, ","))
		}
		if sc == nil || sc.SeccompProfile == nil || sc.SeccompProfile.Type != "Localhost" || sc.SeccompProfile.LocalhostProfile != "profiles/sparkwing-dind.json" {
			out = append(out, "docker lacks the Localhost DinD seccomp profile")
		}
	}
	return out
}
