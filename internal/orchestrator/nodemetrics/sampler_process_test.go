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

func TestSamplerCPUChild(t *testing.T) {
	if flag.Lookup("test.run").Value.String() != "^TestSamplerCPUChild$" {
		t.Skip("subprocess entry point")
	}
	var usage unix.Rusage
	for {
		var value uint64 = 1
		for range 100_000 {
			value = value*1664525 + 1013904223
		}
		runtime.KeepAlive(value)
		if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}
		if usage.Utime.Nano()+usage.Stime.Nano() >= int64(150*time.Millisecond) {
			break
		}
	}
	fmt.Println(usage.Utime.Nano() + usage.Stime.Nano())
	_, _ = io.Copy(io.Discard, os.Stdin)
}
