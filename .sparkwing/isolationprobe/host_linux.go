//go:build linux && (amd64 || arm64)

package isolationprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type hostOptions struct {
	where        string
	workDir      string
	podNetNS     string
	own          []string
	peerCanaries []string
	walk         func(string, fs.WalkDirFunc) error
	invoke       func(syscallTry) (uintptr, syscall.Errno)
}

// safety: the job container and the privileged DinD container run the same host checks, and only
// the privileged one is held to the DinD profile's exact refusals, since it asks for every
// privilege the sidecar could pass on.
func hostProbes(o hostOptions) []Result {
	privileged := o.where == ContextPrivileged
	var out []Result
	out = append(out, uidMapProbe(o.where))
	out = append(out, seccompProbe(o.where))
	out = append(out, kernelFileProbes(o.where)...)
	out = append(out, mountProbes(o.where, o.workDir)...)
	out = append(out, mknodProbes(o.where, o.workDir)...)
	out = append(out, netAdminProbe(o.where))
	invoke := o.invoke
	if invoke == nil {
		invoke = rawSyscall
	}
	out = append(out, syscallProbes(o.where, invoke)...)
	out = append(out, deviceProbe(o.where))
	if data, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		out = append(out, auditMounts(o.where, parseMountinfo(string(data)))...)
	} else {
		out = append(out, Result{Item: "C10", Context: o.where, Probe: probeMountAudit, Expect: ExpectBlocked, Outcome: "mountinfo unreadable", Verdict: Inconclusive})
	}
	for _, base := range markerBases(o.where) {
		out = append(out, markerProbes(o.where, base, exists)...)
	}
	walk := o.walk
	if walk == nil {
		walk = filepath.WalkDir
	}
	start := time.Now()
	expired := func() bool { return time.Since(start) > walkTimeout }
	f := walkTree("/", walkExemptions(o.where, o.own), o.peerCanaries, walkBudget, expired, walk)
	out = append(out, walkResults(o.where, f)...)
	if privileged {
		out = append(out, processProbe(o.where), netnsProbe(o.where, o.podNetNS), spoofGap(o.where))
	}
	return out
}

// safety: forging a source address takes raw packets, which this connect-only probe never sends,
// so the vector is reported as a gap rather than passed.
func spoofGap(where string) Result {
	return Result{Item: "C12", Context: where, Probe: probeSpoofGap, Expect: ExpectBlocked, Outcome: "not probed: needs raw packets", Verdict: NotProbed}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func errnoName(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if name := errnoNames[errno]; name != "" {
			return name
		}
		return "errno-" + strconv.Itoa(int(errno))
	}
	if errors.Is(err, os.ErrNotExist) {
		return "ENOENT"
	}
	if errors.Is(err, os.ErrPermission) {
		return "EACCES"
	}
	return "error"
}

var errnoNames = map[syscall.Errno]string{
	syscall.EPERM: "EPERM", syscall.ENOENT: "ENOENT", syscall.EACCES: "EACCES", syscall.EROFS: "EROFS",
	syscall.ENOSYS: "ENOSYS", syscall.EINVAL: "EINVAL", syscall.EFAULT: "EFAULT", syscall.EBADF: "EBADF",
	syscall.EBUSY: "EBUSY", syscall.ENODEV: "ENODEV", syscall.ENXIO: "ENXIO", syscall.EOPNOTSUPP: "EOPNOTSUPP",
	syscall.E2BIG: "E2BIG", syscall.EEXIST: "EEXIST", syscall.ENOTDIR: "ENOTDIR",
}

// safety: an identity map means root in the job is root on the node; the kernel shows the
// initial namespace as "0 0 4294967295". The outcome never carries the mapped uid.
func uidMapProbe(where string) Result {
	r := Result{Item: "C3", Context: where, Probe: probeUIDMap, Target: "/proc/self/uid_map", Expect: ExpectBlocked}
	data, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		r.Outcome, r.Verdict = errnoName(err), Inconclusive
		return r
	}
	r.Outcome, r.Verdict = uidMapVerdict(string(data))
	return r
}

func uidMapVerdict(text string) (string, Verdict) {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		inside, err1 := strconv.ParseUint(f[0], 10, 64)
		outside, err2 := strconv.ParseUint(f[1], 10, 64)
		if err1 != nil || err2 != nil || inside != 0 {
			continue
		}
		if outside == 0 {
			return "uid 0 is host uid 0", Breach
		}
		return "uid 0 maps to a nonzero host uid", Pass
	}
	return "no mapping for uid 0", Inconclusive
}

// safety: mode 2 is a seccomp filter; every Cloud container runs under one, and the DinD sidecar's
// covers every container it starts because filters stack.
func seccompProbe(where string) Result {
	r := Result{Item: "C12", Context: where, Probe: probeSeccomp, Target: "/proc/self/status", Expect: ExpectBlocked}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		r.Outcome, r.Verdict = errnoName(err), Inconclusive
		return r
	}
	r.Outcome, r.Verdict = seccompVerdict(string(data))
	return r
}

func seccompVerdict(status string) (string, Verdict) {
	for _, line := range strings.Split(status, "\n") {
		v, ok := strings.CutPrefix(line, "Seccomp:")
		if !ok {
			continue
		}
		if mode := strings.TrimSpace(v); mode != "2" {
			return "seccomp mode " + mode, Breach
		}
		return "seccomp mode 2", Pass
	}
	return "seccomp mode not reported", Inconclusive
}

func kernelFileProbes(where string) []Result {
	out := make([]Result, 0, len(kernelFiles))
	for _, f := range kernelFiles {
		flag := os.O_RDONLY
		if f.write {
			flag = os.O_WRONLY
		}
		out = append(out, openProbe(where, f.path, flag, f.probe))
	}
	return out
}

func openProbe(where, path string, flag int, name string) Result {
	r := Result{Item: "C12", Context: where, Probe: name, Target: path, Expect: ExpectBlocked}
	f, err := os.OpenFile(path, flag, 0)
	if err == nil {
		masked := flag == os.O_RDONLY && maskedKernelFile(path, f)
		if masked {
			r.Outcome, r.Verdict = kernelMaskKind(f)+"; read 0 bytes, EOF", Pass
		}
		_ = f.Close()
		if masked {
			return r
		}
		r.Outcome, r.Verdict = "opened", Breach
		return r
	}
	r.Outcome, r.Verdict = errnoName(err), Pass
	return r
}

var maskedProcPaths = map[string]bool{
	"/proc/interrupts": true, "/proc/kcore": true, "/proc/keys": true,
	"/proc/latency_stats": true, "/proc/timer_list": true,
}

// safety: runc masks these proc files with /dev/null; an open alone cannot distinguish that from
// readable kernel memory. A bounded read must reach EOF without returning even one byte.
func maskedKernelFile(path string, f *os.File) bool {
	if !maskedProcPaths[path] {
		return false
	}
	info, err := f.Stat()
	if err != nil || !(info.Mode().IsRegular() || standardChar(info, 1, 3)) {
		return false
	}
	var b [1]byte
	n, err := f.Read(b[:])
	return n == 0 && err == io.EOF
}

func standardChar(info fs.FileInfo, major, minor uint64) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode()&os.ModeCharDevice != 0 && uint64(s.Rdev) == major<<8|minor
}

func kernelMaskKind(f *os.File) string {
	if info, err := f.Stat(); err == nil && standardChar(info, 1, 3) {
		return "masked path: null char device 1:3"
	}
	return "masked path: empty regular file"
}

// safety: user namespaces need these six device binds because mknod is unavailable there. Only
// the expected mount root, destination, character type and device number earn an exception.
func runtimeDeviceBind(where string, m mount) (string, bool) {
	path := m.point
	if where == ContextPrivileged {
		path = strings.TrimPrefix(path, "/host")
	}
	if maskedProcPaths[path] && m.root == "/null" {
		f, err := os.Open(m.point)
		if err != nil {
			return "", false
		}
		defer f.Close() //nolint:errcheck // only read-only inspection
		if !maskedKernelFile(path, f) {
			return "", false
		}
		return "devtmpfs root /null; " + kernelMaskKind(f) + "; read 0 bytes, EOF", true
	}
	devices := map[string][2]uint64{
		"null": {1, 3}, "zero": {1, 5}, "full": {1, 7},
		"random": {1, 8}, "urandom": {1, 9}, "tty": {5, 0},
	}
	name := strings.TrimPrefix(path, "/dev/")
	d, known := devices[name]
	if !known || m.root != "/"+name {
		return "", false
	}
	info, err := os.Stat(m.point)
	return "devtmpfs root " + m.root + "; standard char device " + strconv.FormatUint(d[0], 10) + ":" + strconv.FormatUint(d[1], 10), err == nil && standardChar(info, d[0], d[1])
}

func mountProbes(where, workDir string) []Result {
	var out []Result
	for _, m := range mountTries {
		dir := filepath.Join(workDir, "mnt-"+m.fstype)
		r := Result{Item: "C12", Context: where, Probe: m.name, Target: m.fstype, Expect: ExpectBlocked}
		switch {
		case m.kind == mountRecorded, m.kind == mountControl && where != ContextPrivileged:
			r.Expect = ExpectObserved
		case m.kind == mountControl:
			r.Expect = ExpectOpen
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			r.Outcome, r.Verdict = errnoName(err), Inconclusive
			out = append(out, r)
			continue
		}
		err := syscall.Mount("none", dir, m.fstype, 0, m.data)
		if err == nil {
			_ = syscall.Unmount(dir, syscall.MNT_DETACH) //nolint:errcheck // the probe only needed the mount to succeed
			r.Outcome = "mounted"
		} else {
			r.Outcome = errnoName(err)
		}
		r.Verdict = mountVerdict(r.Expect, err == nil)
		out = append(out, r)
	}
	return out
}

func mountVerdict(expect Expect, mounted bool) Verdict {
	switch expect {
	case ExpectObserved:
		return Observed
	case ExpectOpen:
		if mounted {
			return Pass
		}
		return Inconclusive
	default:
		if mounted {
			return Breach
		}
		return Pass
	}
}

// safety: a block device node for the node's disk would open its filesystem outside every mount
// check; the node is created in scratch and removed at once if the kernel ever allows it.
func mknodProbes(where, workDir string) []Result {
	var out []Result
	for _, dev := range blockDevices {
		path := filepath.Join(workDir, "blk-"+strconv.Itoa(dev.major)+"-"+strconv.Itoa(dev.minor))
		r := Result{Item: "C10", Context: where, Probe: probeMknod, Target: deviceTarget(dev.major, dev.minor), Expect: ExpectBlocked}
		err := syscall.Mknod(path, syscall.S_IFBLK|0o600, dev.major<<8|dev.minor)
		if err == nil {
			_ = os.Remove(path) //nolint:errcheck // the node existing at all is the finding
			r.Outcome, r.Verdict = "created", Breach
		} else {
			r.Outcome, r.Verdict = errnoName(err), Pass
		}
		out = append(out, r)
	}
	return out
}

type ifreqFlags struct {
	name  [syscall.IFNAMSIZ]byte
	flags uint16
	_     [22]byte
}

// safety: setting an interface's flags needs CAP_NET_ADMIN over the namespace and nothing less, so
// writing back the loopback flags just read proves the privileged context holds NET_ADMIN in the
// pod's namespace while changing nothing on it.
func netAdminProbe(where string) Result {
	r := Result{Item: "C12", Context: where, Probe: probeNetAdmin, Target: "lo", Expect: ExpectObserved}
	if where == ContextPrivileged {
		r.Expect = ExpectOpen
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err == nil {
		var ifr ifreqFlags
		copy(ifr.name[:], "lo")
		if err = ifreqIoctl(fd, syscall.SIOCGIFFLAGS, &ifr); err == nil {
			err = ifreqIoctl(fd, syscall.SIOCSIFFLAGS, &ifr)
		}
		_ = syscall.Close(fd) //nolint:errcheck // a probe socket
	}
	r.Outcome = "allowed"
	if err != nil {
		r.Outcome = errnoName(err)
	}
	r.Verdict = mountVerdict(r.Expect, err == nil)
	return r
}

func ifreqIoctl(fd int, request uintptr, ifr *ifreqFlags) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(unsafe.Pointer(ifr))); errno != 0 {
		return errno
	}
	return nil
}

type syscallTry struct {
	name string
	nr   uintptr
	args [6]uintptr
	want syscall.Errno
	fd   bool
}

// safety: every call carries arguments the kernel rejects, pointers included, so none can succeed
// even unfiltered; the errno says which layer refused. The DinD profile returns exactly EPERM or
// ENOSYS for each.
func syscallTries() []syscallTry {
	atFDCWD := ^uintptr(99)
	neg := ^uintptr(0)
	return []syscallTry{
		{name: "bpf", nr: sysBPF, want: syscall.EPERM, fd: true},
		{name: "perf_event_open", nr: sysPerfEventOpen, args: [6]uintptr{0, 0, neg, neg, 0}, want: syscall.EPERM, fd: true},
		{name: "userfaultfd", nr: sysUserfaultfd, args: [6]uintptr{0xffffffff}, want: syscall.EPERM, fd: true},
		{name: "kexec_load", nr: sysKexecLoad, args: [6]uintptr{0, 0, 0, 0xfff0}, want: syscall.EPERM},
		{name: "kexec_file_load", nr: sysKexecFileLoad, args: [6]uintptr{neg, neg, 0, 0, 0xfffffff0}, want: syscall.EPERM},
		{name: "init_module", nr: sysInitModule, want: syscall.EPERM},
		{name: "finit_module", nr: sysFinitModule, args: [6]uintptr{neg}, want: syscall.EPERM},
		{name: "delete_module", nr: sysDeleteModule, args: [6]uintptr{1, syscall.O_NONBLOCK}, want: syscall.EPERM},
		{name: "open_by_handle_at", nr: sysOpenByHandleAt, args: [6]uintptr{atFDCWD}, want: syscall.EPERM, fd: true},
		{name: "fanotify_init", nr: sysFanotifyInit, args: [6]uintptr{0xffffffff}, want: syscall.EPERM, fd: true},
		{name: "keyctl", nr: sysKeyctl, args: [6]uintptr{0xffff}, want: syscall.ENOSYS},
		{name: "add_key", nr: sysAddKey, want: syscall.ENOSYS},
		{name: "request_key", nr: sysRequestKey, want: syscall.ENOSYS},
		{name: "io_uring_setup", nr: sysIOUringSetup, want: syscall.ENOSYS, fd: true},
		{name: "io_uring_enter", nr: sysIOUringEnter, args: [6]uintptr{neg}, want: syscall.ENOSYS},
		{name: "io_uring_register", nr: sysIOUringRegister, args: [6]uintptr{neg}, want: syscall.ENOSYS},
		{name: "clone3", nr: sysClone3, want: syscall.ENOSYS},
	}
}

func rawSyscall(s syscallTry) (uintptr, syscall.Errno) {
	r1, _, errno := syscall.Syscall6(s.nr, s.args[0], s.args[1], s.args[2], s.args[3], s.args[4], s.args[5])
	return r1, errno
}

func syscallProbes(where string, invoke func(syscallTry) (uintptr, syscall.Errno)) []Result {
	var out []Result
	for _, s := range syscallTries() {
		r1, errno := invoke(s)
		r := Result{Item: "C12", Context: where, Probe: "seccomp refuses " + s.name, Target: s.name, Expect: ExpectObserved}
		if errno == 0 {
			if s.fd {
				_ = syscall.Close(int(r1)) //nolint:errcheck // close whatever the impossible success returned
			}
			r.Outcome = "returned"
		} else {
			r.Outcome = errnoName(errno)
		}
		r.Verdict = Observed
		if where == ContextPrivileged {
			r.Expect = ExpectBlocked
			r.Verdict = Breach
			if errno == s.want {
				r.Verdict = Pass
			}
		}
		out = append(out, r)
	}
	return out
}

func deviceProbe(where string) Result {
	r := Result{Item: "C10", Context: where, Probe: probeDevices, Target: "/dev", Expect: ExpectBlocked, Outcome: "none", Verdict: Pass}
	entries, err := os.ReadDir("/dev")
	if err != nil {
		r.Outcome, r.Verdict = errnoName(err), Inconclusive
		return r
	}
	var found []string
	for _, e := range entries {
		if t := e.Type(); t&os.ModeDevice != 0 && t&os.ModeCharDevice == 0 {
			found = append(found, e.Name())
		}
	}
	if len(found) > 0 {
		sort.Strings(found)
		r.Outcome, r.Verdict = strings.Join(found, ","), Breach
	}
	return r
}

// safety: with the host PID namespace requested, a container must still see only the sidecar's
// processes; any of these names means it sees the node's.
var nodeProcesses = map[string]bool{
	"kubelet": true, "systemd": true, "kube-proxy": true, "aws-k8s-agent": true, "amazon-ssm-agen": true,
	"sshd": true, "chronyd": true, "eks-pod-identit": true, "aws-network-pol": true, "aws-ebs-csi-dri": true,
}

func processProbe(where string) Result {
	r := Result{Item: "C12", Context: where, Probe: probeProcesses, Target: "/proc", Expect: ExpectBlocked}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		r.Outcome, r.Verdict = errnoName(err), Inconclusive
		return r
	}
	seen, node := 0, map[string]bool{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue
		}
		seen++
		if name := strings.TrimSpace(string(comm)); nodeProcesses[name] {
			node[name] = true
		}
	}
	r.Outcome, r.Verdict = strconv.Itoa(seen)+" processes, none of the node's", Pass
	if len(node) > 0 {
		names := make([]string, 0, len(node))
		for n := range node {
			names = append(names, n)
		}
		sort.Strings(names)
		r.Outcome, r.Verdict = "node processes visible: "+strings.Join(names, ","), Breach
	}
	return r
}

func readNetNS() string {
	ns, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return ""
	}
	return ns
}

// safety: a container given the host network must land in the pod's namespace, which the job
// container reports; any other namespace could be the node's.
func netnsProbe(where, podNetNS string) Result {
	r := Result{Item: "C11", Context: where, Probe: probeNetNS, Target: "/proc/self/ns/net", Expect: ExpectBlocked}
	own := readNetNS()
	switch {
	case podNetNS == "" || own == "":
		r.Outcome, r.Verdict = "namespace unreadable", Inconclusive
	case own == podNetNS:
		r.Outcome, r.Verdict = "same namespace as the job container", Pass
	default:
		r.Outcome, r.Verdict = "a different namespace from the job container", Breach
	}
	return r
}

// safety: the first hop answers a one-hop UDP probe with an ICMP time-exceeded from its own
// address, which on a VPC CNI pod is the node; the error queue carries that offender.
func discoverNodeAddress(ctx context.Context, timeout time.Duration) (string, error) {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return "", err
	}
	defer pc.Close() //nolint:errcheck // a probe socket
	udp, ok := pc.(*net.UDPConn)
	if !ok {
		return "", errors.New("not a UDP socket")
	}
	raw, err := udp.SyscallConn()
	if err != nil {
		return "", err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_RECVERR, 1)
		if serr == nil {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, 1)
		}
	}); err != nil {
		return "", err
	}
	if serr != nil {
		return "", serr
	}
	if _, err := udp.WriteTo([]byte(Command), &net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 33434}); err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := udp.SetReadDeadline(deadline); err != nil {
		return "", err
	}
	var offender string
	buf, oob := make([]byte, 64), make([]byte, 512)
	err = raw.Read(func(fd uintptr) bool {
		_, oobn, _, _, rerr := syscall.Recvmsg(int(fd), buf, oob, syscall.MSG_ERRQUEUE)
		if errors.Is(rerr, syscall.EAGAIN) {
			return false
		}
		if rerr != nil {
			serr = rerr
			return true
		}
		offender, serr = parseOffender(oob[:oobn])
		return true
	})
	if err != nil {
		return "", err
	}
	return offender, serr
}

// safety: an IP_RECVERR message is a sock_extended_err followed by the offender's sockaddr_in,
// and only an ICMP-originated error names the hop that answered.
func parseOffender(oob []byte) (string, error) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return "", err
	}
	for _, m := range msgs {
		if m.Header.Level != syscall.IPPROTO_IP || m.Header.Type != syscall.IP_RECVERR || len(m.Data) < 16+8 {
			continue
		}
		const originICMP = 2
		if m.Data[4] != originICMP {
			return "", errors.New("error queue carried no ICMP error")
		}
		addr := m.Data[16:]
		if binary.LittleEndian.Uint16(addr[0:2]) != syscall.AF_INET {
			return "", errors.New("offender is not an IPv4 address")
		}
		return net.IPv4(addr[4], addr[5], addr[6], addr[7]).String(), nil
	}
	return "", errors.New("no IP_RECVERR message")
}
