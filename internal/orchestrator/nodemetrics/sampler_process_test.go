//go:build darwin || linux

package nodemetrics

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
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

type finalSampleSink struct{ samples []Sample }

func (s *finalSampleSink) Push(_ context.Context, sample Sample) error {
	s.samples = append(s.samples, sample)
	return nil
}

func TestAttachRetainsChildCPUWithoutATimerTick(t *testing.T) {
	precision := nativeCPUPrecision(t)
	t.Cleanup(SetIntervalForTest(time.Hour))
	sink := &finalSampleSink{}
	start := time.Now()
	finish := Attach(t.Context(), sink)
	defer finish()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSamplerCPUChild$")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	childCPU, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if err != nil || childCPU < int64(150*time.Millisecond) {
		t.Fatalf("child CPU oracle %q: %v", line, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	finish()
	if len(sink.samples) != 1 || !sink.samples[0].Valid {
		t.Fatalf("short child has no valid final reading: %+v", sink.samples)
	}
	sample := sink.samples[0]
	lower := int64((time.Duration(childCPU)-precision).Seconds()/sample.TS.Sub(start).Seconds()*1000) - 1
	if sample.CPUMillicores < lower {
		t.Fatalf("sample CPU %dm is below independently measured child minimum %dm", sample.CPUMillicores, lower)
	}
}
