//go:build darwin || linux

package nodemetrics

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeResidentChild(t *testing.T) {
	if flag.Lookup("test.run").Value.String() != "^TestNativeResidentChild$" {
		t.Skip("subprocess entry point")
	}
	allocation := make([]byte, 64<<20)
	for offset := 0; offset < len(allocation); offset += os.Getpagesize() {
		allocation[offset] = 1
	}
	consumeCPU(t, 150*time.Millisecond)
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	fmt.Println(usage.Utime.Nano() + usage.Stime.Nano())
	_, _ = io.Copy(io.Discard, os.Stdin)
	runtime.KeepAlive(allocation)
}

func consumeCPU(t *testing.T, target time.Duration) {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	start := usage.Utime.Nano() + usage.Stime.Nano()
	var value uint64 = 1
	for {
		for range 100_000 {
			value = value*1664525 + 1013904223
		}
		runtime.KeepAlive(value)
		if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}
		if time.Duration(usage.Utime.Nano()+usage.Stime.Nano()-start) >= target {
			return
		}
	}
}
