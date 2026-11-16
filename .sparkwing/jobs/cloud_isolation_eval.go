package jobs

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"sparkwing-pipelines/isolationprobe"
	"sparkwing-pipelines/isolationprobe/fixture"
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
	Plan    isolationprobe.Plan      `json:"-"`
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
	Name      string    `json:"name"`
	IPs       []string  `json:"ips"`
	TeamNode  string    `json:"team_node"`
	UsedTeam  string    `json:"used_team"`
	UsedRun   string    `json:"used_run"`
	Pool      string    `json:"pool_namespace"`
	NodePool  string    `json:"nodepool"`
	NodeClass string    `json:"nodeclass"`
	Claimed   bool      `json:"nodeclaim_captured"`
	Pods      []nodePod `json:"pods"`
}

// safety: a pod seen on a tested node is allowed there only as a DaemonSet's or as one of the
// node's own run's, which the launcher's award binding records.
type nodePod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	DaemonSet bool   `json:"daemonset,omitempty"`
	Run       string `json:"run,omitempty"`
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
	{"C13", "Every phase ran and was captured"},
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

	nodeRun := map[string]*isolationRun{}
	for i := range runs {
		r := &runs[i]
		if r.RunID == "" {
			add("C13", "%s was not run; the sequence stopped before it", r.Label)
			continue
		}
		judgePlacement(namespace, r, nodes, nodeRun, add)
		for _, f := range probeFindings(*r) {
			add(f.item, "%s", f.text)
		}
		for _, peer := range r.Peers {
			if !anchorsAlive[peer] {
				add("C9", "peer %s was not running after %s probed it, so its listener proves nothing", peer, r.Label)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(nodeRun)) {
		owner := nodeRun[name]
		for _, p := range nodes[name].Pods {
			if !p.DaemonSet && p.Run != owner.RunID {
				add("C2", "node %s of %s also held pod %s/%s", name, owner.Label, p.Namespace, p.Name)
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

// safety: a node passes only when it carries this staging pool's namespace, the run's own team
// labels and the used-run fence the launcher computes for this exact run, so a node fenced to any
// other run, team or pool fails even when each value is unique.
func judgePlacement(namespace string, r *isolationRun, nodes map[string]isolationNode, nodeRun map[string]*isolationRun, add func(string, string, ...any)) {
	probe := probePod(*r)
	if probe == nil {
		add("C2", "%s has no probe pod in %s", r.Label, namespace)
		return
	}
	team, fence := teamNodeValue(namespace, r.Team), runNodeValue(namespace, r.RunID)
	for _, p := range r.Pods {
		for _, s := range p.Spec {
			add("C3", "%s pod %s: %s", r.Label, p.Name, s)
		}
		if p.Node == "" {
			add("C2", "%s pod %s was never seen on a node", r.Label, p.Name)
			continue
		}
		if other, ok := nodeRun[p.Node]; ok && other.Label != r.Label {
			add("C2", "%s and %s share node %s", other.Label, r.Label, p.Node)
		}
		nodeRun[p.Node] = r
		n, ok := nodes[p.Node]
		switch {
		case !ok:
			add("C13", "%s pod %s ran on node %s, which was never captured", r.Label, p.Name, p.Node)
		case n.Pool != namespace:
			add("C2", "%s node %s belongs to pool namespace %q, want %q", r.Label, p.Node, n.Pool, namespace)
		case n.TeamNode != team || n.UsedTeam != team:
			add("C2", "%s node %s carries team labels %q/%q, want %q", r.Label, p.Node, n.TeamNode, n.UsedTeam, team)
		case n.UsedRun != fence:
			add("C2", "%s node %s is fenced to %q, want this run's %q", r.Label, p.Node, n.UsedRun, fence)
		}
		if ok && !n.Claimed {
			add("C13", "%s node %s has no captured NodeClaim", r.Label, p.Node)
		}
	}
	if len(r.Ready) == 0 {
		return
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

type finding struct{ item, text string }

// safety: a run passes only on the full result set its plan owes, every verdict one the probe
// emits and one summary that agrees with them, so a vanished probe, a context that never ran or a
// garbled log fails the run instead of passing on what it did report.
func probeFindings(r isolationRun) []finding {
	var out []finding
	add := func(item, format string, args ...any) {
		out = append(out, finding{item, fmt.Sprintf(format, args...)})
	}
	for _, res := range r.Results {
		switch {
		case !res.Verdict.Valid():
			add("C13", "%s %s reported verdict %q for %s", r.Label, res.Context, res.Verdict, res.Probe)
		case res.Verdict.Fails():
			add(itemOf(res.Item), "%s %s %s: %s %s (%s)", r.Label, res.Context, res.Verdict, res.Probe, res.Target, res.Outcome)
		}
	}
	switch {
	case len(r.Summary) == 0:
		add("C13", "%s reported no summary; its probes did not finish", r.Label)
	case len(r.Summary) > 1:
		add("C13", "%s reported %d summaries, want one", r.Label, len(r.Summary))
	case r.Summary[0] != isolationprobe.Summarize(r.Results):
		add("C13", "%s summary %+v disagrees with its %d results", r.Label, r.Summary[0], len(r.Results))
	}
	if len(r.Ready) == 0 {
		add("C13", "%s never announced its listener, so the checks it owes are unknown", r.Label)
		return out
	}
	type gap struct{ context, item string }
	missing := map[gap][]isolationprobe.Key{}
	var order []gap
	for _, k := range isolationprobe.Missing(isolationprobe.Expected(r.Plan, r.Ready[0], r.RunID, fixture.SDKKeys()...), r.Results) {
		g := gap{k.Context, itemOf(k.Item)}
		if missing[g] == nil {
			order = append(order, g)
		}
		missing[g] = append(missing[g], k)
	}
	for _, g := range order {
		first := missing[g][0]
		add(g.item, "%s %s never reported %d owed %s checks, first %q %s", r.Label, g.context, len(missing[g]), g.item, first.Probe, first.Target)
	}
	return out
}

func probePod(r isolationRun) *isolationPod {
	for i := range r.Pods {
		if r.Pods[i].Job == isolationprobe.ProbeJob {
			return &r.Pods[i]
		}
	}
	return nil
}

func itemOf(item string) string {
	for _, it := range isolationItems {
		if it.id == item {
			return item
		}
	}
	return "C13"
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
		if sc == nil || sc.ProcMount != "Unmasked" {
			out = append(out, "docker does not mount /proc unmasked as designed")
		}
		if sc == nil || sc.SeccompProfile == nil || sc.SeccompProfile.Type != "Localhost" || sc.SeccompProfile.LocalhostProfile != "profiles/sparkwing-dind.json" {
			out = append(out, "docker lacks the Localhost DinD seccomp profile")
		}
	}
	return out
}
