//go:build darwin || linux

package nodemetrics

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeTreeMemoryTracksConcurrentChildren(t *testing.T) {
	start := func() (int, func()) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		t.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeResidentChild$")
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
		t.Cleanup(func() {
			_ = stdin.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		cpu, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil || cpu < int64(150*time.Millisecond) {
			t.Fatalf("child readiness %q: %v", line, err)
		}
		if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		var status syscall.WaitStatus
		stopped := syscall.WaitStatus(uint32(syscall.SIGSTOP)<<8 | 0x7f)
		if _, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WUNTRACED, nil); err != nil || status != stopped {
			t.Fatalf("stop child: status=%v error=%v", status, err)
		}
		return cmd.Process.Pid, func() {
			if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(children ...int) {
		t.Helper()
		var before, after int64
		for _, pid := range children {
			before += nativeRSSOracle(t, pid)
		}
		reading := processTreeUsage(os.Getpid())
		for _, pid := range children {
			after += nativeRSSOracle(t, pid)
		}
		childMemory := reading.memory - reading.processes[os.Getpid()].rss
		if !reading.valid || len(reading.processes) != len(children)+1 || childMemory < min(before, after) || childMemory > max(before, after) {
			t.Fatalf("%d concurrent children: RSS %d outside independent [%d, %d]; reading=%+v", len(children), childMemory, before, after, reading)
		}
		t.Logf("%d concurrent children: RSS %d; independent [%d, %d]", len(children), childMemory, before, after)
	}
	first, finishFirst := start()
	check(first)
	second, finishSecond := start()
	check(first, second)
	finishFirst()
	check(second)
	finishSecond()
	check()
	third, finishThird := start()
	check(third)
	finishThird()
	check()
}
