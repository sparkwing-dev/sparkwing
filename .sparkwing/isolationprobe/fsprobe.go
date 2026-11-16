package isolationprobe

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type mount struct {
	root, point, fstype, source string
}

// safety: mountinfo escapes spaces and other separators as octal, and an unparsable line is
// skipped rather than guessed at; the audit then fails on a mount it cannot classify.
func parseMountinfo(text string) []mount {
	var out []mount
	for _, line := range strings.Split(text, "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 5 || len(g) < 2 {
			continue
		}
		out = append(out, mount{root: unescapeMount(f[3]), point: unescapeMount(f[4]), fstype: g[0], source: unescapeMount(g[1])})
	}
	return out
}

func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

var kubeletPodRoot = regexp.MustCompile(`^/var/lib/kubelet/pods/([0-9a-f-]{36})/`)

// safety: these filesystems hand a container the node's devices or kernel internals, so a mount of
// one is a breach wherever it appears.
var hostKernelFS = map[string]bool{"devtmpfs": true, "debugfs": true, "securityfs": true, "tracefs": true, "configfs": true}

var diskFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "vfat": true, "nfs": true, "nfs4": true, "zfs": true, "9p": true, "drvfs": true}

// safety: a disk-backed mount is the node's filesystem, so it passes only when its root is this
// pod's own kubelet directory or the container runtime's per-sandbox files.
var runtimePodRoots = []string{
	"/var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/",
	"/var/lib/containerd/io.containerd.grpc.v1.cri/containers/",
}

func auditMounts(where string, mounts []mount) []Result {
	if len(mounts) == 0 {
		return []Result{{Item: "C10", Context: where, Probe: probeMountAudit, Expect: ExpectBlocked, Outcome: "no mounts parsed", Verdict: Inconclusive}}
	}
	pods := map[string]bool{}
	for _, m := range mounts {
		if sub := kubeletPodRoot.FindStringSubmatch(m.root); sub != nil {
			pods[sub[1]] = true
		}
	}
	var out []Result
	if len(pods) > 1 {
		out = append(out, Result{Item: "C10", Context: where, Probe: "mount audit: one pod's kubelet directory", Expect: ExpectBlocked, Outcome: "volumes from more than one pod", Verdict: Breach})
	}
	for _, m := range mounts {
		diskBacked := diskFS[m.fstype] || strings.HasPrefix(m.source, "/dev/")
		switch {
		case hostKernelFS[m.fstype]:
			out = append(out, Result{Item: "C10", Context: where, Probe: "host kernel filesystem mounted", Target: m.point, Expect: ExpectBlocked, Outcome: m.fstype, Verdict: Breach})
		case diskBacked && !ownPodRoot(m.root):
			out = append(out, Result{Item: "C10", Context: where, Probe: "node path mounted outside the pod", Target: m.point, Expect: ExpectBlocked, Outcome: m.fstype + " root " + m.root, Verdict: Breach})
		}
	}
	summary := Result{Item: "C10", Context: where, Probe: probeMountAudit, Expect: ExpectBlocked, Outcome: "all mounts are virtual, the container's own or this pod's", Verdict: Pass}
	if len(out) > 0 {
		summary.Outcome, summary.Verdict = "a mount reaches outside the pod", Breach
	}
	return append([]Result{summary}, out...)
}

func ownPodRoot(root string) bool {
	if kubeletPodRoot.MatchString(root) {
		return true
	}
	for _, p := range runtimePodRoots {
		if strings.HasPrefix(root, p) {
			return true
		}
	}
	return false
}

// safety: these paths exist on an EKS node and never in a container image, so finding one under a
// container's view of / means that view is the node's.
var nodeMarkers = []string{
	"var/lib/kubelet",
	"etc/kubernetes",
	"etc/eks",
	"usr/bin/kubelet",
	"var/log/pods",
	"run/containerd/containerd.sock",
}

func markerProbes(where, base string, exists func(string) bool) []Result {
	found := 0
	for _, m := range nodeMarkers {
		if exists(filepath.Join(base, m)) {
			found++
		}
	}
	r := Result{Item: "C10", Context: where, Probe: "node markers under " + base, Target: base, Expect: ExpectBlocked, Outcome: "none present", Verdict: Pass}
	if found > 0 {
		r.Outcome, r.Verdict = "node markers present", Breach
	}
	return []Result{r}
}

// safety: sockets with these names belong to a container runtime or the kubelet; one the pod does
// not own is a way to start containers outside the pod.
var runtimeSockets = map[string]bool{"docker.sock": true, "containerd.sock": true, "kubelet.sock": true, "dockershim.sock": true, "cri-dockerd.sock": true, "crio.sock": true}

// safety: a huge or slow tree must not stall the probe, so a walk stops at whichever bound it
// reaches first and reports the stop.
const (
	walkBudget  = 1_000_000
	walkTimeout = 2 * time.Minute
)

var kernelViews = []string{"/proc", "/sys", "/dev"}

// safety: dockerd keeps its state beside its socket on the scratch volume, so the pod's own daemon
// state is exempt by the same rule as the socket, and nothing else on the volume is.
func ownScratch(socket, runDir string) []string {
	var out []string
	if socket != "" {
		dir := filepath.Dir(socket)
		out = append(out, socket, filepath.Join(dir, "docker"), filepath.Join(dir, "docker-exec"))
	}
	if runDir != "" {
		out = append(out, runDir)
	}
	return out
}

// safety: the privileged container sees the sidecar's root, scratch included, under /host, so the
// pod's own paths are exempt there and peer scratch stays in the search.
func walkExemptions(where string, own []string) []string {
	out := append([]string(nil), kernelViews...)
	if where != ContextPrivileged {
		return append(out, own...)
	}
	out = append(out, "/out")
	for _, p := range append(append([]string(nil), kernelViews...), own...) {
		out = append(out, filepath.Join("/host", p))
	}
	return out
}

type walkFindings struct {
	sockets  []string
	canaries []string
	visited  int
	budget   bool
}

// safety: the walk passes over kernel views and the exact paths this pod owns and searches the
// rest, peer scratch included; an exhausted budget is reported, not passed.
func walkTree(root string, exempt, peerCanaries []string, budget int, expired func() bool, walk func(string, fs.WalkDirFunc) error) walkFindings {
	var f walkFindings
	peers := map[string]bool{}
	for _, c := range peerCanaries {
		peers[c] = true
	}
	exempted := map[string]bool{}
	for _, s := range exempt {
		exempted[s] = true
	}
	_ = walk(root, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck // unreadable entries are skipped
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		f.visited++
		if f.visited > budget || (f.visited%1024 == 0 && expired()) {
			f.budget = true
			return fs.SkipAll
		}
		if exempted[path] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSocket != 0 && runtimeSockets[d.Name()] {
			f.sockets = append(f.sockets, path)
		}
		if peers[d.Name()] {
			f.canaries = append(f.canaries, path)
		}
		return nil
	})
	return f
}

func walkResults(where string, f walkFindings) []Result {
	clean := "none in " + strconv.Itoa(f.visited) + " entries"
	sockets := Result{Item: "C9", Context: where, Probe: probeSockets, Expect: ExpectBlocked, Outcome: clean, Verdict: Pass}
	if len(f.sockets) > 0 {
		sockets.Outcome, sockets.Verdict = strings.Join(f.sockets, ","), Breach
	}
	canary := Result{Item: "C10", Context: where, Probe: probeCanaries, Expect: ExpectBlocked, Outcome: clean, Verdict: Pass}
	if len(f.canaries) > 0 {
		canary.Outcome, canary.Verdict = strings.Join(f.canaries, ","), Breach
	}
	out := []Result{sockets, canary}
	if f.budget {
		for i := range out {
			if out[i].Verdict == Pass {
				out[i].Outcome, out[i].Verdict = "walk budget exhausted", Inconclusive
			}
		}
	}
	return out
}
