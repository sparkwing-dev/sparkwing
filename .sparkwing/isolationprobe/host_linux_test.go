//go:build linux && (amd64 || arm64)

package isolationprobe

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

func TestUIDMapVerdictFailsTheInitialNamespace(t *testing.T) {
	for name, c := range map[string]struct {
		text string
		want Verdict
	}{
		"initial namespace": {"         0          0 4294967295\n", Breach},
		"user namespace":    {"         0  123863040      65536\n", Pass},
		"root unmapped":     {"      1000       1000          1\n", Inconclusive},
		"unreadable":        {"", Inconclusive},
	} {
		outcome, got := uidMapVerdict(c.text)
		if got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
		if strings.Contains(outcome, "123863040") {
			t.Errorf("%s: outcome %q carries the mapped uid", name, outcome)
		}
	}
}

func TestSeccompVerdictNeedsAFilter(t *testing.T) {
	for status, want := range map[string]Verdict{
		"Name:\tsh\nSeccomp:\t2\nSeccomp_filters:\t2\n": Pass,
		"Name:\tsh\nSeccomp:\t0\n":                      Breach,
		"Name:\tsh\nSeccomp:\t1\n":                      Breach,
		"Name:\tsh\n":                                   Inconclusive,
	} {
		if _, got := seccompVerdict(status); got != want {
			t.Errorf("%q: %s, want %s", status, got, want)
		}
	}
}

func TestMountVerdictJudgesByExpectation(t *testing.T) {
	for _, c := range []struct {
		expect  Expect
		mounted bool
		want    Verdict
	}{
		{ExpectBlocked, true, Breach},
		{ExpectBlocked, false, Pass},
		{ExpectOpen, true, Pass},
		{ExpectOpen, false, Inconclusive},
		{ExpectObserved, true, Observed},
	} {
		if got := mountVerdict(c.expect, c.mounted); got != c.want {
			t.Errorf("%s mounted=%v: %s, want %s", c.expect, c.mounted, got, c.want)
		}
	}
}

// The DinD profile refuses each of these with one errno; a call that the kernel judged instead
// means the profile no longer covers it.
func TestSyscallTriesExpectTheDinDProfilesErrnos(t *testing.T) {
	names := map[string]bool{}
	for _, s := range syscallTries() {
		if names[s.name] || s.nr == 0 || (s.want != syscall.EPERM && s.want != syscall.ENOSYS) {
			t.Errorf("syscall try %+v", s)
		}
		names[s.name] = true
	}
	if len(names) != 17 {
		t.Fatalf("syscall tries = %d, want the profile's 17 refusals", len(names))
	}
	var tried []string
	for _, s := range syscallTries() {
		tried = append(tried, s.name)
	}
	if !slices.Equal(tried, syscallNames) {
		t.Fatalf("syscall tries %v differ from the manifest's %v", tried, syscallNames)
	}
}

// No call passes the kernel a usable pointer, so a missing filter cannot turn a probe into a real
// operation; delete_module in particular gets an address the kernel cannot read.
func TestSyscallTriesPassNoUsablePointer(t *testing.T) {
	for _, s := range syscallTries() {
		if s.name == "delete_module" && s.args[0] != 1 {
			t.Fatalf("delete_module name pointer = %#x, want the unreadable address 1", s.args[0])
		}
	}
}

func refuseAsTheProfile(s syscallTry) (uintptr, syscall.Errno) { return 0, s.want }

func emptyWalk(_ string, fn fs.WalkDirFunc) error { return fs.WalkDir(fstest.MapFS{}, ".", fn) }

// Each context's host checks answer every key the driver requires of them, so a check that is
// renamed or dropped fails coverage instead of vanishing.
func TestHostChecksAnswerTheManifest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the mount and device-node checks would act on this machine")
	}
	for _, where := range []string{ContextJob, ContextPrivileged} {
		results := hostProbes(hostOptions{where: where, workDir: t.TempDir(), podNetNS: readNetNS(), walk: emptyWalk, invoke: refuseAsTheProfile})
		if missing := Missing(hostKeys(where), results); len(missing) > 0 {
			t.Errorf("%s host checks miss %v", where, missing)
		}
	}
}

// The NET_ADMIN control passes only with NET_ADMIN over the namespace, which an unprivileged test
// process lacks whatever else it holds.
func TestNetAdminControlNeedsNetAdmin(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root holds NET_ADMIN")
	}
	if r := netAdminProbe(ContextPrivileged); r.Verdict != Inconclusive || r.Target != "lo" || r.Outcome != "EPERM" {
		t.Fatalf("privileged control without NET_ADMIN = %+v, want inconclusive EPERM", r)
	}
	if r := netAdminProbe(ContextJob); r.Verdict != Observed {
		t.Fatalf("job control = %+v, want observed", r)
	}
}

func TestErrnoNameNamesTheRefusal(t *testing.T) {
	for err, want := range map[error]string{
		syscall.EPERM:                     "EPERM",
		&os.PathError{Err: syscall.EROFS}: "EROFS",
		os.ErrNotExist:                    "ENOENT",
		syscall.Errno(200):                "errno-200",
		fmt.Errorf("plain"):               "error",
	} {
		if got := errnoName(err); got != want {
			t.Errorf("errnoName(%v) = %q, want %q", err, got, want)
		}
	}
}

func recvErr(origin byte, family uint16, addr [4]byte) []byte {
	data := make([]byte, 32)
	binary.LittleEndian.PutUint32(data[0:], uint32(syscall.EHOSTUNREACH))
	data[4], data[5] = origin, 11
	binary.LittleEndian.PutUint16(data[16:], family)
	copy(data[20:24], addr[:])
	oob := make([]byte, syscall.CmsgSpace(len(data)))
	binary.LittleEndian.PutUint64(oob[0:], uint64(syscall.CmsgLen(len(data))))
	binary.LittleEndian.PutUint32(oob[8:], uint32(syscall.IPPROTO_IP))
	binary.LittleEndian.PutUint32(oob[12:], uint32(syscall.IP_RECVERR))
	copy(oob[syscall.CmsgLen(0):], data)
	return oob
}

func TestParseOffenderReadsTheHopThatAnswered(t *testing.T) {
	got, err := parseOffender(recvErr(2, syscall.AF_INET, [4]byte{10, 9, 8, 7}))
	if err != nil || got != "10.9.8.7" {
		t.Fatalf("offender = %q, %v", got, err)
	}
	if _, err := parseOffender(recvErr(1, syscall.AF_INET, [4]byte{10, 9, 8, 7})); err == nil {
		t.Fatal("a local error was read as an ICMP offender")
	}
	if _, err := parseOffender(recvErr(2, syscall.AF_INET6, [4]byte{})); err == nil {
		t.Fatal("an IPv6 offender was read as IPv4")
	}
	if _, err := parseOffender(nil); err == nil {
		t.Fatal("an empty error queue named an offender")
	}
}
