package isolationprobe

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const ownPod = "7d1f6c2e-1b4a-4c55-9e1b-3f6f2a9b0c11"

var jobMountinfo = strings.Join([]string{
	"2001 1900 0:452 / / rw,relatime master:1 - overlay overlay rw,lowerdir=/var/lib/containerd/l1,upperdir=/var/lib/containerd/u1,workdir=/var/lib/containerd/w1",
	"2002 2001 0:455 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw",
	"2003 2001 0:456 / /dev rw,nosuid - tmpfs tmpfs rw,size=65536k,mode=755",
	"2004 2003 0:457 / /dev/pts rw,nosuid,noexec,relatime - devpts devpts rw,gid=5,mode=620,ptmxmode=666",
	"2005 2003 0:445 / /dev/mqueue rw,nosuid,nodev,noexec,relatime - mqueue mqueue rw",
	"2006 2001 0:449 / /sys ro,nosuid,nodev,noexec,relatime - sysfs sysfs ro",
	"2007 2006 0:28 / /sys/fs/cgroup ro,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw",
	"2008 2001 259:1 /var/lib/kubelet/pods/" + ownPod + "/volumes/kubernetes.io~empty-dir/scratch /tmp rw,relatime - xfs /dev/nvme0n1p1 rw,attr2,inode64",
	"2009 2001 0:440 / /etc/sparkwing/bootstrap ro,relatime - tmpfs tmpfs rw,size=7972000k",
	"2010 2001 259:1 /var/lib/kubelet/pods/" + ownPod + "/etc-hosts /etc/hosts rw,relatime - xfs /dev/nvme0n1p1 rw",
	"2011 2001 259:1 /var/lib/kubelet/pods/" + ownPod + "/containers/run-node/6b1e2f3a /dev/termination-log rw,relatime - xfs /dev/nvme0n1p1 rw",
	"2012 2001 259:1 /var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/0f4b1c/hostname /etc/hostname rw,relatime - xfs /dev/nvme0n1p1 rw",
	"2013 2001 259:1 /var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/0f4b1c/resolv.conf /etc/resolv.conf rw,relatime - xfs /dev/nvme0n1p1 rw",
}, "\n")

func TestParseMountinfoUnescapesPaths(t *testing.T) {
	mounts := parseMountinfo(jobMountinfo + "\n2014 2001 0:99 /a\\040b /mnt/with\\040space rw - tmpfs tmp\\134fs rw\nnot a mount line")
	if len(mounts) != 14 {
		t.Fatalf("parsed %d mounts, want 14", len(mounts))
	}
	last := mounts[13]
	if last.root != "/a b" || last.point != "/mnt/with space" || last.source != `tmp\fs` || last.fstype != "tmpfs" {
		t.Fatalf("escaped mount = %+v", last)
	}
}

func TestAuditMountsPassesAJobPodsOwnMounts(t *testing.T) {
	results := auditMounts(ContextJob, parseMountinfo(jobMountinfo))
	if len(results) != 1 || results[0].Verdict != Pass {
		t.Fatalf("job mounts = %+v, want one pass", results)
	}
}

func TestAuditMountsFailsAnythingReachingOutsideThePod(t *testing.T) {
	for name, extra := range map[string]string{
		"a node directory":     "2100 2001 259:1 /var/log /host-logs rw - xfs /dev/nvme0n1p1 rw",
		"the node's root":      "2100 2001 259:1 / /node rw - ext4 /dev/nvme0n1p1 rw",
		"another pod's volume": "2100 2001 259:1 /var/lib/kubelet/pods/00000000-1111-2222-3333-444444444444/volumes/x /other rw - xfs /dev/nvme0n1p1 rw",
		"the node's devices":   "2100 2001 0:6 / /dev rw - devtmpfs devtmpfs rw",
		"kernel debug views":   "2100 2001 0:7 / /sys/kernel/debug rw - debugfs debugfs rw",
	} {
		results := auditMounts(ContextPrivileged, parseMountinfo(jobMountinfo+"\n"+extra))
		if results[0].Probe != "mount audit" || results[0].Verdict != Breach || len(results) < 2 {
			t.Errorf("%s: %+v, want a failed audit naming the mount", name, results)
		}
	}
	if r := auditMounts(ContextJob, nil); len(r) != 1 || r[0].Verdict != Inconclusive {
		t.Fatalf("unparsed mountinfo = %+v, want inconclusive", r)
	}
}

func TestMarkerProbesFindTheNodesOwnPaths(t *testing.T) {
	none := func(string) bool { return false }
	if r := markerProbes(ContextPrivileged, "/host", none); r[0].Verdict != Pass {
		t.Fatalf("no markers = %+v", r)
	}
	kubelet := func(p string) bool { return p == "/host/var/lib/kubelet" }
	if r := markerProbes(ContextPrivileged, "/host", kubelet); r[0].Verdict != Breach {
		t.Fatalf("kubelet directory under /host = %+v, want breach", r)
	}
}

func never() bool { return false }

func TestWalkTreeFindsRuntimeSocketsAndPeerCanariesOutsideExemptPaths(t *testing.T) {
	mfs := fstest.MapFS{
		"proc/1/comm":                        {Data: []byte("x")},
		"proc/docker.sock":                   {Mode: fs.ModeSocket},
		"tmp/docker.sock":                    {Mode: fs.ModeSocket},
		"run/containerd/containerd.sock":     {Mode: fs.ModeSocket},
		"run/unrelated.sock":                 {Mode: fs.ModeSocket},
		"var/tmp/isolation-canary-stg-iso-b": {},
		"var/tmp/isolation-canary-stg-iso-a": {},
		"etc/hosts":                          {},
	}
	walk := func(root string, fn fs.WalkDirFunc) error { return fs.WalkDir(mfs, root, fn) }
	f := walkTree(".", []string{"proc", "tmp/docker.sock"}, []string{"isolation-canary-stg-iso-b"}, 1000, never, walk)
	if strings.Join(f.sockets, ",") != "run/containerd/containerd.sock" || strings.Join(f.canaries, ",") != "var/tmp/isolation-canary-stg-iso-b" || f.budget {
		t.Fatalf("walk = %+v", f)
	}
	results := walkResults(ContextJob, f)
	if len(results) != 2 || results[0].Verdict != Breach || results[1].Verdict != Breach {
		t.Fatalf("walk results = %+v, want both breaches", results)
	}
	clean := walkResults(ContextJob, walkTree(".", []string{"proc", "tmp", "run", "var"}, []string{"isolation-canary-stg-iso-b"}, 1000, never, walk))
	if clean[0].Verdict != Pass || clean[1].Verdict != Pass || clean[0].Probe != probeSockets || clean[1].Probe != probeCanaries {
		t.Fatalf("clean walk = %+v", clean)
	}
	cut := walkResults(ContextJob, walkTree(".", nil, nil, 2, never, walk))
	for _, r := range cut {
		if r.Verdict != Inconclusive {
			t.Fatalf("budget-exhausted walk = %+v, want inconclusive", cut)
		}
	}
	big := fstest.MapFS{}
	for i := range 3000 {
		big["d/f"+strconv.Itoa(i)] = &fstest.MapFile{}
	}
	stalled := walkTree(".", nil, nil, 1_000_000, func() bool { return true }, func(root string, fn fs.WalkDirFunc) error { return fs.WalkDir(big, root, fn) })
	if !stalled.budget || stalled.visited > 1024 {
		t.Fatalf("walk past its time budget = %+v, want it stopped at the first check", stalled)
	}
}

// The walk exempts exactly the pod's own socket, DinD state and run directory, so a peer's canary
// or socket on the same scratch volume is found from the job container and, under /host, from the
// privileged container.
func TestWalkSearchesPeerScratchAndExemptsOnlyThePodsOwn(t *testing.T) {
	const peer, own = "isolation-canary-peer", "isolation-canary-own"
	scratch := func(root string) fstest.MapFS {
		return fstest.MapFS{
			root + "tmp/docker.sock":                              {Mode: fs.ModeSocket},
			root + "tmp/docker-exec/containerd/containerd.sock":   {Mode: fs.ModeSocket},
			root + "tmp/docker/overlay2/l/x":                      {},
			root + "tmp/isolation-probe/run-" + own + "/" + own:   {},
			root + "tmp/isolation-probe/run-" + peer + "/" + peer: {},
		}
	}
	walkOf := func(mfs fstest.MapFS) func(string, fs.WalkDirFunc) error {
		return func(_ string, fn fs.WalkDirFunc) error {
			return fs.WalkDir(mfs, ".", func(path string, d fs.DirEntry, err error) error {
				return fn("/"+strings.TrimPrefix(path, "."), d, err)
			})
		}
	}
	privileged := scratch("host/")
	privileged["out/exchange/plan.json"] = &fstest.MapFile{}
	views := map[string]fstest.MapFS{ContextJob: scratch(""), ContextPrivileged: privileged}
	ownPaths := ownScratch("/tmp/docker.sock", "/tmp/isolation-probe/run-"+own)
	for where, want := range map[string]string{
		ContextJob:        "/tmp/isolation-probe/run-" + peer + "/" + peer,
		ContextPrivileged: "/host/tmp/isolation-probe/run-" + peer + "/" + peer,
	} {
		f := walkTree("/", walkExemptions(where, ownPaths), []string{peer, own}, 1000, never, walkOf(views[where]))
		if len(f.sockets) != 0 {
			t.Errorf("%s flagged the pod's own sockets: %v", where, f.sockets)
		}
		if strings.Join(f.canaries, ",") != want {
			t.Errorf("%s canaries = %v, want only the peer's %s", where, f.canaries, want)
		}
	}
	job := views[ContextJob]
	job["tmp/peer-scratch/docker.sock"] = &fstest.MapFile{Mode: fs.ModeSocket}
	if f := walkTree("/", walkExemptions(ContextJob, ownPaths), nil, 1000, never, walkOf(job)); strings.Join(f.sockets, ",") != "/tmp/peer-scratch/docker.sock" {
		t.Fatalf("sockets = %v, want the peer's socket outside the pod's own", f.sockets)
	}
}
