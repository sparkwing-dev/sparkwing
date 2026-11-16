package isolationprobe

import (
	"net"
	"path/filepath"
	"strconv"
)

// Key identifies one result a probe run owes: where it ran, the checklist item, the probe and its
// target. The driver requires a result for every expected key, so a check that vanished fails the
// run instead of passing on what was reported.
type Key struct {
	Context string `json:"context"`
	Item    string `json:"item"`
	Probe   string `json:"probe"`
	Target  string `json:"target,omitempty"`
}

// Key returns the result's identity in the manifest.
func (r Result) Key() Key {
	return Key{Context: r.Context, Item: r.Item, Probe: r.Probe, Target: r.Target}
}

// Contexts lists every context a Cloud probe run reports from.
var Contexts = []string{ContextJob, ContextBridge, ContextPrivileged}

// safety: the checks and the manifest share these names, so renaming a check cannot drop it from
// coverage.
const (
	probeUIDMap        = "user namespace maps root away from host root"
	probeSeccomp       = "seccomp filter applied"
	probeMknod         = "block device node creation"
	probeNetAdmin      = "NET_ADMIN works inside the pod's namespace"
	probeDevices       = "block devices in /dev"
	probeMountAudit    = "mount audit"
	probeSockets       = "runtime sockets outside the pod's own"
	probeCanaries      = "peer canary files"
	probeRootfsCanary  = "rootfs canary written"
	probeProcesses     = "host PID namespace shows no node process"
	probeNetNS         = "host network is the pod's network namespace"
	probeSpoofGap      = "fence holds for a spoofed source address"
	probeSelfConnect   = "own listener answers on the pod address"
	probeCredentialEnv = "credential-named environment variable"
	probeOwnDocker     = "own Docker daemon answers"
	probeOwnNode       = "own node discovered"
)

type positiveCheck struct{ kind, name, target string }

const (
	checkDNS   = "dns"
	checkHTTPS = "https"
	checkTCP   = "tcp"
)

// safety: positive controls run in every context. The public ones prove egress works and the
// cluster name proves the allowed DNS path, so a refusal elsewhere in that context is the fence's.
var positiveChecks = []positiveCheck{
	{checkDNS, "public name resolves through cluster DNS", "github.com"},
	{checkDNS, "cluster name resolves through cluster DNS", "kubernetes.default.svc.cluster.local"},
	{checkHTTPS, "public HTTPS answers", "https://github.com/"},
	{checkTCP, "public non-web TCP port answers", "1.1.1.1:53"},
}

type fileCheck struct {
	path, probe string
	write       bool
}

// safety: write targets are opened and closed without a write. Permission is checked at open, so a
// successful open is the breach and nothing on the node changes.
var kernelFiles = []fileCheck{
	{"/proc/sys/kernel/core_pattern", "kernel file opens for writing", true},
	{"/proc/sys/kernel/modprobe", "kernel file opens for writing", true},
	{"/proc/sysrq-trigger", "kernel file opens for writing", true},
	{"/proc/sys/vm/drop_caches", "kernel file opens for writing", true},
	{"/sys/kernel/uevent_helper", "kernel file opens for writing", true},
	{"/proc/kcore", "kernel memory opens for reading", false},
	{"/dev/mem", "kernel memory opens for reading", false},
	{"/dev/kmem", "kernel memory opens for reading", false},
	{"/dev/port", "kernel memory opens for reading", false},
	{"/dev/kmsg", "kernel log opens for reading", false},
}

type mountKind int

const (
	mountHostLevel mountKind = iota
	mountRecorded
	mountControl
)

type mountTry struct {
	fstype, data, name string
	kind               mountKind
}

// safety: cgroup v1 is the release_agent route off the node, and debugfs, securityfs and tracefs
// expose kernel internals. bpffs is mountable inside a user namespace from Linux 6.9 and grants
// nothing without bpf(), which the syscall checks judge, so its outcome is recorded. tmpfs is the
// control that proves SYS_ADMIN works inside the namespace.
var mountTries = []mountTry{
	{"cgroup", "none,name=isolation-probe", "cgroup v1 hierarchy mounts", mountHostLevel},
	{"debugfs", "", "debugfs mounts", mountHostLevel},
	{"securityfs", "", "securityfs mounts", mountHostLevel},
	{"tracefs", "", "tracefs mounts", mountHostLevel},
	{"bpf", "", "bpffs mounts inside the pod's namespace", mountRecorded},
	{"tmpfs", "size=1m", "tmpfs mounts inside the pod's namespace", mountControl},
}

// safety: majors 259 and 202 are the NVMe and Xen block devices an EC2 node's disks use.
var blockDevices = []struct{ major, minor int }{{259, 0}, {202, 0}}

func deviceTarget(major, minor int) string { return strconv.Itoa(major) + ":" + strconv.Itoa(minor) }

// safety: these are the calls the DinD seccomp profile refuses, in syscallTries' order, which a
// test holds to this list.
var syscallNames = []string{
	"bpf", "perf_event_open", "userfaultfd", "kexec_load", "kexec_file_load", "init_module", "finit_module",
	"delete_module", "open_by_handle_at", "fanotify_init", "keyctl", "add_key", "request_key", "io_uring_setup",
	"io_uring_enter", "io_uring_register", "clone3",
}

func markerBases(where string) []string {
	if where == ContextPrivileged {
		return []string{"/host", "/proc/1/root"}
	}
	return []string{"/"}
}

func hostKeys(where string) []Key {
	var keys []Key
	add := func(item, probe, target string) {
		keys = append(keys, Key{Context: where, Item: item, Probe: probe, Target: target})
	}
	add("C3", probeUIDMap, "/proc/self/uid_map")
	add("C12", probeSeccomp, "/proc/self/status")
	for _, f := range kernelFiles {
		add("C12", f.probe, f.path)
	}
	for _, m := range mountTries {
		add("C12", m.name, m.fstype)
	}
	for _, d := range blockDevices {
		add("C10", probeMknod, deviceTarget(d.major, d.minor))
	}
	add("C12", probeNetAdmin, "lo")
	for _, name := range syscallNames {
		add("C12", "seccomp refuses "+name, name)
	}
	add("C10", probeDevices, "/dev")
	add("C10", probeMountAudit, "")
	for _, base := range markerBases(where) {
		add("C10", "node markers under "+base, base)
	}
	add("C9", probeSockets, "")
	add("C10", probeCanaries, "")
	if where == ContextPrivileged {
		add("C12", probeProcesses, "/proc")
		add("C11", probeNetNS, "/proc/self/ns/net")
		add("C12", probeSpoofGap, "")
	}
	return keys
}

// Expected lists every result a Cloud probe run owes for its plan: each target and positive
// control from every context, the job container's own checks, and the host checks in the job and
// privileged containers. ready is what the run announced, runID names the run, and extra adds the
// checks only the SDK wrapper makes. A run that did not discover its node owes that gap instead of
// the node's ports.
func Expected(plan Plan, ready Ready, runID string, extra ...Key) []Key {
	var keys []Key
	targets := append(append([]Target(nil), plan.Targets...), ownNodeTargets(ready.NodeIP)...)
	for _, where := range Contexts {
		for _, c := range positiveChecks {
			keys = append(keys, Key{Context: where, Item: "C6", Probe: c.name, Target: c.target})
		}
		for _, t := range targets {
			keys = append(keys, Key{Context: where, Item: t.Item, Probe: t.Name, Target: t.Address})
		}
	}
	job := func(item, probe, target string) {
		keys = append(keys, Key{Context: ContextJob, Item: item, Probe: probe, Target: target})
	}
	if ready.NodeIP == "" {
		job("C8", probeOwnNode, "")
	}
	for _, t := range ownPodDockerTargets(ready.PodIPs) {
		job(t.Item, t.Name, t.Address)
	}
	for _, ip := range ready.PodIPs {
		job("C9", probeSelfConnect, net.JoinHostPort(ip, strconv.Itoa(plan.ListenPort)))
	}
	for _, rt := range controllerRoutes(plan, runID) {
		job("C3", rt.name, rt.method+" "+rt.path)
	}
	job("C10", probeRootfsCanary, "")
	job("C3", probeCredentialEnv, "")
	job("C3", "credential file "+filepath.Base(serviceAccountToken), serviceAccountToken)
	if ready.Home != "" {
		job("C3", "credential file .netrc", filepath.Join(ready.Home, ".netrc"))
	}
	job("C9", probeOwnDocker, "docker.sock")
	keys = append(keys, hostKeys(ContextJob)...)
	keys = append(keys, hostKeys(ContextPrivileged)...)
	return append(keys, extra...)
}

// Missing returns the expected keys no result answers, in their expected order.
func Missing(expected []Key, results []Result) []Key {
	have := make(map[Key]bool, len(results))
	for _, r := range results {
		have[r.Key()] = true
	}
	var missing []Key
	for _, k := range expected {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	return missing
}

// Valid reports whether a verdict is one the probe emits.
func (v Verdict) Valid() bool {
	switch v {
	case Pass, Breach, Inconclusive, Observed, NotProbed:
		return true
	default:
		return false
	}
}
