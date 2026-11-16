package isolationprobe

import (
	"slices"
	"testing"
)

func manifestPlan() Plan {
	return Plan{
		Team: "team-a", ControllerURL: "https://controller.example.invalid", ListenPort: 18080, Canary: "isolation-canary-a",
		PeerCanaries: []string{"isolation-canary-b"}, PeerRuns: []string{"run-peer"},
		Targets: []Target{
			{Item: "C4", Name: "IMDS", Address: "169.254.169.254:80", Expect: ExpectBlocked, Listening: true, Grade: GradeIMDS},
			{Item: "C6", Name: "controller pod", Address: "10.9.3.3:4343", Expect: ExpectBlocked, Listening: true},
		},
	}
}

func keySet(keys []Key) map[Key]bool {
	has := map[Key]bool{}
	for _, k := range keys {
		has[k] = true
	}
	return has
}

// Every target and positive control is owed from every context, the job container owes its own
// checks, and both host-check contexts owe the whole host manifest.
func TestExpectedCoversEveryContextAndCheck(t *testing.T) {
	ready := Ready{PodIPs: []string{"10.9.4.4"}, ListenPort: 18080, NodeIP: "10.9.5.5", Home: "/home/runner"}
	extra := Key{Context: ContextJob, Item: "C3", Probe: "SDK read"}
	has := keySet(Expected(manifestPlan(), ready, "run-own", extra))
	for _, where := range Contexts {
		for _, want := range []Key{
			{where, "C4", "IMDS", "169.254.169.254:80"},
			{where, "C6", "controller pod", "10.9.3.3:4343"},
			{where, "C8", "own node kube-proxy metrics", "10.9.5.5:10249"},
			{where, "C8", "own node kubelet", "10.9.5.5:10250"},
			{where, "C6", "cluster name resolves through cluster DNS", "kubernetes.default.svc.cluster.local"},
			{where, "C6", "public non-web TCP port answers", "1.1.1.1:53"},
		} {
			if !has[want] {
				t.Errorf("manifest lacks %+v", want)
			}
		}
	}
	for _, want := range []Key{
		{ContextJob, "C9", "own pod Docker TCP port", "127.0.0.1:2375"},
		{ContextJob, "C9", "own pod Docker TCP port", "10.9.4.4:2376"},
		{ContextJob, "C9", probeSelfConnect, "10.9.4.4:18080"},
		{ContextJob, "C9", probeOwnDocker, "docker.sock"},
		{ContextJob, "C3", "anonymous token list", "GET /api/v1/tokens"},
		{ContextJob, "C3", "anonymous peer-run probe output", "GET /api/v1/runs/run-peer/jobs/probe/output"},
		{ContextJob, "C3", "credential file token", serviceAccountToken},
		{ContextJob, "C3", "credential file .netrc", "/home/runner/.netrc"},
		{ContextJob, "C10", probeRootfsCanary, ""},
		{ContextJob, "C12", "seccomp refuses bpf", "bpf"},
		{ContextPrivileged, "C12", "seccomp refuses delete_module", "delete_module"},
		{ContextPrivileged, "C12", "bpffs mounts inside the pod's namespace", "bpf"},
		{ContextPrivileged, "C12", "kernel log opens for reading", "/dev/kmsg"},
		{ContextPrivileged, "C12", probeNetAdmin, "lo"},
		{ContextPrivileged, "C12", probeSpoofGap, ""},
		{ContextPrivileged, "C10", "node markers under /proc/1/root", "/proc/1/root"},
		{ContextPrivileged, "C10", probeCanaries, ""},
		extra,
	} {
		if !has[want] {
			t.Errorf("manifest lacks %+v", want)
		}
	}
	for _, absent := range []Key{
		{ContextBridge, "C12", "seccomp refuses bpf", "bpf"},
		{ContextJob, "C12", probeSpoofGap, ""},
		{ContextJob, "C8", probeOwnNode, ""},
	} {
		if has[absent] {
			t.Errorf("manifest owes %+v, which that context never reports", absent)
		}
	}
}

// A run that could not find its node owes that gap instead of the node's ports.
func TestExpectedOwesTheNodeDiscoveryGap(t *testing.T) {
	has := keySet(Expected(manifestPlan(), Ready{PodIPs: []string{"10.9.4.4"}}, "run-own"))
	if !has[Key{ContextJob, "C8", probeOwnNode, ""}] {
		t.Fatal("manifest does not owe the node discovery gap")
	}
	for k := range has {
		if k.Item == "C8" && k.Probe != probeOwnNode {
			t.Errorf("manifest owes %+v without a node", k)
		}
	}
}

func TestMissingNamesEachDroppedResult(t *testing.T) {
	keys := Expected(manifestPlan(), Ready{PodIPs: []string{"10.9.4.4"}, NodeIP: "10.9.5.5"}, "run-own")
	var results []Result
	for _, k := range keys {
		results = append(results, Result{Context: k.Context, Item: k.Item, Probe: k.Probe, Target: k.Target, Verdict: Pass})
	}
	if missing := Missing(keys, results); len(missing) != 0 {
		t.Fatalf("complete results miss %v", missing)
	}
	dropped := []int{3, len(keys) / 2, len(keys) - 1}
	var kept []Result
	for i, r := range results {
		if !slices.Contains(dropped, i) {
			kept = append(kept, r)
		}
	}
	missing := Missing(keys, kept)
	if len(missing) != len(dropped) {
		t.Fatalf("missing = %v, want the %d dropped keys", missing, len(dropped))
	}
	for i, d := range dropped {
		if missing[i] != keys[d] {
			t.Errorf("missing[%d] = %+v, want %+v", i, missing[i], keys[d])
		}
	}
}

func TestOnlyTheProbesVerdictsAreValid(t *testing.T) {
	for _, v := range []Verdict{Pass, Breach, Inconclusive, Observed, NotProbed} {
		if !v.Valid() {
			t.Errorf("%s is invalid", v)
		}
	}
	for _, v := range []Verdict{"", "PASS", "breach", "skipped"} {
		if v.Valid() {
			t.Errorf("%q is valid", v)
		}
	}
}
