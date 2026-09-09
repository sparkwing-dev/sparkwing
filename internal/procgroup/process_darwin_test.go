//go:build darwin

package procgroup

import (
	"os"
	"syscall"
	"testing"
)

func TestNativeProcessTableMatchesTheIdentityLookup(t *testing.T) {
	processes, err := processTable(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	processID := os.Getpid()
	var currentProcess Info
	for _, process := range processes {
		if process.PID == processID {
			currentProcess = process
			break
		}
	}
	if currentProcess.PID != processID {
		t.Fatalf("kernel listing of %d processes did not include this process (%d)", len(processes), processID)
	}
	if currentProcess.Birth == "" {
		t.Fatal("kernel listing carried no birth token")
	}
	if want := syscall.Getpgrp(); currentProcess.Group != want {
		t.Fatalf("process group = %d, want %d", currentProcess.Group, want)
	}

	sessionID, token, err := sessionIdentity(processID)
	if err != nil {
		t.Fatalf("session identity: %v", err)
	}
	if token != currentProcess.Birth {
		t.Fatalf("birth token from the listing = %q, from the lookup = %q", currentProcess.Birth, token)
	}
	if currentProcess.Session != sessionID {
		t.Fatalf("session from the listing = %d, from the lookup = %d", currentProcess.Session, sessionID)
	}
}

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
