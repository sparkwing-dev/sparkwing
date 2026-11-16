//go:build linux && (amd64 || arm64)

package isolationprobe

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"testing"
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
		if _, got := uidMapVerdict(c.text); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
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
	got, err := parseOffender(recvErr(2, syscall.AF_INET, [4]byte{10, 0, 51, 209}))
	if err != nil || got != "10.0.51.209" {
		t.Fatalf("offender = %q, %v", got, err)
	}
	if _, err := parseOffender(recvErr(1, syscall.AF_INET, [4]byte{10, 0, 51, 209})); err == nil {
		t.Fatal("a local error was read as an ICMP offender")
	}
	if _, err := parseOffender(recvErr(2, syscall.AF_INET6, [4]byte{})); err == nil {
		t.Fatal("an IPv6 offender was read as IPv4")
	}
	if _, err := parseOffender(nil); err == nil {
		t.Fatal("an empty error queue named an offender")
	}
}
