//go:build darwin

package procgroup

import (
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

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

func TestProcessTableFallsBackAudiblyWhenTheKernelListingFails(t *testing.T) {
	originalListing := darwinProcessListing
	originalLog := nativeFallbackLog
	t.Cleanup(func() {
		darwinProcessListing = originalListing
		nativeFallbackLog = originalLog
		nativeFallbackOnce = sync.Once{}
	})
	nativeFallbackOnce = sync.Once{}
	darwinProcessListing = func() ([]byte, error) { return nil, errors.New("sysctl refused") }
	reported := 0
	var reportedErr error
	nativeFallbackLog = func(err error) {
		reported++
		reportedErr = err
	}

	for i := 0; i < 3; i++ {
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
	if reportedErr == nil || !strings.Contains(reportedErr.Error(), "sysctl refused") {
		t.Fatalf("fallback warning carried %v, want the kernel failure", reportedErr)
	}
}

func TestShortKernelListingFallsBack(t *testing.T) {
	originalListing := darwinProcessListing
	originalLog := nativeFallbackLog
	t.Cleanup(func() {
		darwinProcessListing = originalListing
		nativeFallbackLog = originalLog
		nativeFallbackOnce = sync.Once{}
	})
	nativeFallbackOnce = sync.Once{}
	darwinProcessListing = func() ([]byte, error) { return []byte{1, 2, 3}, nil }
	nativeFallbackLog = func(error) {}

	if _, ok := nativeProcessTable(true); ok {
		t.Fatal("a truncated kernel listing was accepted as the process table")
	}
}
