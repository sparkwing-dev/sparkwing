//go:build linux

package procgroup

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// hack: the fixture pads to the kernel's field count so a test line exercises
// the same offsets as a live read.
func statLine(pid int, comm, state string, group, session int, start uint64) string {
	fields := make([]string, procStatMinFields)
	for i := range fields {
		fields[i] = "0"
	}
	fields[procStatState] = state
	fields[procStatGroup] = strconv.Itoa(group)
	fields[procStatSession] = strconv.Itoa(session)
	fields[procStatStart] = strconv.FormatUint(start, 10)
	return strconv.Itoa(pid) + " (" + comm + ") " + strings.Join(fields, " ") + " 0 0\n"
}

func TestParseProcStatReadsFieldsPastAParenthesisedCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		comm string
	}{
		{name: "plain", comm: "go"},
		{name: "spaces", comm: "my build tool"},
		{name: "parentheses", comm: "sh (deleted)"},
		{name: "spaces and parentheses", comm: "a (b c) d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			process, err := parseProcStat(statLine(4021, tc.comm, "S", 4000, 3900, 918273), true)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			want := Info{PID: 4021, Group: 4000, Session: 3900, State: "S", Birth: "918273"}
			if process != want {
				t.Fatalf("parsed %+v, want %+v", process, want)
			}
		})
	}
}

func TestParseProcStatLeavesTheSessionUnreadWhenUnwanted(t *testing.T) {
	process, err := parseProcStat(statLine(7, "cc1 (x)", "Z", 5, 3, 12), false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if process.Session != 0 {
		t.Fatalf("session = %d, want it unread", process.Session)
	}
	if !process.Terminated() {
		t.Fatalf("state %q, want one the terminated check accepts", process.State)
	}
}

func TestParseProcStatRejectsUnusableLines(t *testing.T) {
	for name, line := range map[string]string{
		"no command":   "4021 4000 3900\n",
		"no pid":       "(go) S 1 4000 3900\n",
		"short fields": "4021 (go) S 1 4000\n",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProcStat(line, true); err == nil {
				t.Fatalf("accepted %q as a process stat", line)
			}
		})
	}
}

func TestNativeProcessTableSkipsWhatIsNotALiveProcess(t *testing.T) {
	root := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("fixture dir: %v", err)
		}
		if contents == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(contents), 0o600); err != nil {
			t.Fatalf("fixture stat: %v", err)
		}
	}
	write("11", statLine(11, "go build", "R", 11, 9, 100))
	write("12", statLine(12, "compile (x)", "S", 11, 9, 101))
	write("13", "")
	write("14", "garbage\n")
	write("self", statLine(99, "go", "S", 11, 9, 102))
	write("sys", statLine(98, "go", "S", 11, 9, 103))

	original := procRoot
	t.Cleanup(func() { procRoot = original })
	procRoot = root

	processes, ok := nativeProcessTable(true)
	if !ok {
		t.Fatal("the /proc listing refused a readable root")
	}
	want := []Info{
		{PID: 11, Group: 11, Session: 9, State: "R", Birth: "100"},
		{PID: 12, Group: 11, Session: 9, State: "S", Birth: "101"},
	}
	if len(processes) != len(want) {
		t.Fatalf("listed %+v, want %+v", processes, want)
	}
	for i, process := range processes {
		if process != want[i] {
			t.Fatalf("process %d = %+v, want %+v", i, process, want[i])
		}
	}
}

func TestNativeProcessTableMatchesTheIdentityLookup(t *testing.T) {
	processes, ok := nativeProcessTable(true)
	if !ok {
		t.Skip("/proc listing unavailable")
	}
	self := os.Getpid()
	var mine Info
	for _, process := range processes {
		if process.PID == self {
			mine = process
			break
		}
	}
	if mine.PID != self {
		t.Fatalf("listing of %d processes did not include this process (%d)", len(processes), self)
	}
	if want := syscall.Getpgrp(); mine.Group != want {
		t.Fatalf("process group = %d, want %d", mine.Group, want)
	}
	sid, token, err := sessionIdentity(self)
	if err != nil {
		t.Fatalf("session identity: %v", err)
	}
	if token != mine.Birth {
		t.Fatalf("birth token from the listing = %q, from the lookup = %q", mine.Birth, token)
	}
	if mine.Session != sid {
		t.Fatalf("session from the listing = %d, from the lookup = %d", mine.Session, sid)
	}
}

func TestProcessTableFallsBackAudiblyWhenProcIsUnreadable(t *testing.T) {
	originalRoot := procRoot
	originalLog := nativeFallbackLog
	t.Cleanup(func() {
		procRoot = originalRoot
		nativeFallbackLog = originalLog
		nativeFallbackOnce = sync.Once{}
	})
	nativeFallbackOnce = sync.Once{}
	procRoot = filepath.Join(t.TempDir(), "absent")
	reported := 0
	var reportedErr error
	nativeFallbackLog = func(err error) {
		reported++
		reportedErr = err
	}

	for i := range 3 {
		processes, err := processTable(true)
		if err != nil {
			t.Fatalf("listing %d fell back but failed: %v", i, err)
		}
		if len(processes) == 0 {
			t.Fatalf("listing %d returned no processes; the ps fallback did not answer", i)
		}
		if processes[0].Birth != "" {
			t.Fatal("the ps fallback carried a birth token it cannot know")
		}
	}
	if reported != 1 {
		t.Fatalf("fallback reported %d times, want exactly one warning per process", reported)
	}
	if reportedErr == nil || !errors.Is(reportedErr, os.ErrNotExist) {
		t.Fatalf("fallback warning carried %v, want the unreadable root", reportedErr)
	}
}
