//go:build darwin

package procgroup

import (
	"os"
	"syscall"
	"testing"
)

// TestNativeProcessTableMatchesTheIdentityLookup keeps the kernel listing
// and the per-process identity lookup answering the same question. The
// guarded sweep compares a birth token recorded by one against tokens
// carried by the other, so a formatting difference between them would
// read as a reused leader and silently drop a live run's admission.
func TestNativeProcessTableMatchesTheIdentityLookup(t *testing.T) {
	processes, ok := nativeProcessTable(true)
	if !ok {
		t.Skip("kernel process listing unavailable")
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
		t.Fatalf("kernel listing of %d processes did not include this process (%d)", len(processes), self)
	}
	if mine.Birth == "" {
		t.Fatal("kernel listing carried no birth token, so a sweep still needs a second lookup")
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

// TestNativeProcessTableReportsTerminatedChildren keeps the state letters
// compatible with the `ps` reader they replace: a zombie holds no
// admission, and misreading one as live would strand a guarded lease.
func TestNativeProcessTableReportsTerminatedChildren(t *testing.T) {
	if got := darwinProcessState(5); !processTerminated(got) {
		t.Fatalf("zombie state = %q, want a state the terminated check accepts", got)
	}
	for _, live := range []int8{1, 2, 3, 4} {
		if got := darwinProcessState(live); processTerminated(got) {
			t.Fatalf("live state %d = %q, want a state the terminated check rejects", live, got)
		}
	}
}
