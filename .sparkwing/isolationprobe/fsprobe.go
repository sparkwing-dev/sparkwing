package isolationprobe

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
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
		return []Result{{Item: "C10", Context: where, Probe: "mount audit", Expect: ExpectBlocked, Outcome: "no mounts parsed", Verdict: Inconclusive}}
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
	summary := Result{Item: "C10", Context: where, Probe: "mount audit", Expect: ExpectBlocked, Outcome: "all mounts are virtual, the container's own or this pod's", Verdict: Pass}
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

// safety: sockets with these names belong to a node's container runtime or kubelet; one outside
// the job's scratch is a way to start containers outside the pod.
var runtimeSockets = map[string]bool{"docker.sock": true, "containerd.sock": true, "kubelet.sock": true, "dockershim.sock": true, "cri-dockerd.sock": true, "crio.sock": true}

type walkFindings struct {
	sockets  []string
	canaries []string
	visited  int
	budget   bool
}

// safety: the walk skips kernel views and the job's own scratch, and stops at a fixed budget so a
// huge tree cannot stall the probe; an exhausted budget is reported, not passed.
func walkTree(root string, skip, peerCanaries []string, budget int, walk func(string, fs.WalkDirFunc) error) walkFindings {
	var f walkFindings
	peers := map[string]bool{}
	for _, c := range peerCanaries {
		peers[c] = true
	}
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	_ = walk(root, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck // unreadable entries are skipped
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		f.visited++
		if f.visited > budget {
			f.budget = true
			return fs.SkipAll
		}
		if d.IsDir() && skipped[path] {
			return fs.SkipDir
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

func walkResults(where string, f walkFindings, scanCanaries bool) []Result {
	sockets := Result{Item: "C9", Context: where, Probe: "runtime sockets outside scratch", Expect: ExpectBlocked, Outcome: "none found", Verdict: Pass}
	if len(f.sockets) > 0 {
		sockets.Outcome, sockets.Verdict = strings.Join(f.sockets, ","), Breach
	}
	out := []Result{sockets}
	if scanCanaries {
		canary := Result{Item: "C10", Context: where, Probe: "peer canary files", Expect: ExpectBlocked, Outcome: "none found", Verdict: Pass}
		if len(f.canaries) > 0 {
			canary.Outcome, canary.Verdict = strings.Join(f.canaries, ","), Breach
		}
		out = append(out, canary)
	}
	if f.budget {
		for i := range out {
			if out[i].Verdict == Pass {
				out[i].Outcome, out[i].Verdict = "walk budget exhausted", Inconclusive
			}
		}
	}
	return out
}
