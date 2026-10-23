//go:build linux || darwin

package sparkwing

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	gort "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/execdiag"
)

const cancellationDiagnosticMarker = "cancellation diagnostic captured"

const cancellationDiagnosticTail = "cancellation diagnostic complete"

func TestDiagnosticOutputLimiter_BoundsOnlyPostTimeoutOutput(t *testing.T) {
	expired := false
	limiter := &diagnosticOutputLimiter{
		policy:    execdiag.Policy{Expired: func() bool { return expired }},
		remaining: 64,
	}
	if got := string(limiter.capture([]byte("ordinary output"))); got != "ordinary output" {
		t.Fatalf("pre-timeout output = %q", got)
	}
	expired = true
	if got := string(limiter.capture([]byte("short"))); got != "short" {
		t.Fatalf("in-budget diagnostic = %q", got)
	}
	used := len("short")
	if got := string(limiter.capture([]byte(strings.Repeat("x", 64)))); got != diagnosticTruncationMarker {
		t.Fatalf("first overflow = %q", got)
	} else {
		used += len(got)
	}
	if used > 64 {
		t.Fatalf("diagnostic output used %d bytes, limit 64", used)
	}
	if got := limiter.capture([]byte("more")); len(got) != 0 {
		t.Fatalf("post-marker overflow = %q", got)
	}
}

func TestExecCancellationDiagnosticHelper(t *testing.T) {
	if os.Getenv("SPARKWING_EXEC_CORE_LIMIT_HELPER") == "1" {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &limit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "core-limit=%d\n", limit.Cur)
		return
	}
	if os.Getenv("SPARKWING_EXEC_STUBBORN_DESCENDANT") == "1" {
		signal.Ignore(syscall.SIGQUIT)
		if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_READY_FILE"), []byte("ready"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("SPARKWING_EXEC_RUNTIME_DUMP_HELPER") == "1" {
		blocked := make(chan struct{})
		var started sync.WaitGroup
		started.Add(5_000)
		for range 5_000 {
			go func() {
				started.Done()
				<-blocked
			}()
		}
		started.Wait()
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGQUIT)
		go func() {
			<-quit
			buf := make([]byte, 1<<20)
			for {
				n := gort.Stack(buf, true)
				if n < len(buf) {
					fmt.Fprintln(os.Stderr, "SIGQUIT: quit")
					_, _ = os.Stderr.Write(buf[:n])
					fmt.Fprintln(os.Stderr, cancellationDiagnosticTail)
					os.Exit(2)
				}
				buf = make([]byte, len(buf)*2)
			}
		}()
		if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_READY_FILE"), []byte("ready"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("SPARKWING_EXEC_DIAGNOSTIC_HELPER") != "1" {
		return
	}
	descendantReady := os.Getenv("SPARKWING_EXEC_READY_FILE") + ".descendant"
	descendant := exec.Command(os.Args[0], "-test.run=^TestExecCancellationDiagnosticHelper$")
	descendant.Env = append(os.Environ(),
		"SPARKWING_EXEC_STUBBORN_DESCENDANT=1",
		"SPARKWING_EXEC_READY_FILE="+descendantReady,
	)
	if err := descendant.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(descendantReady); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "descendant did not become ready")
			os.Exit(3)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_DESCENDANT_FILE"), []byte(strconv.Itoa(descendant.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGQUIT)
	go func() {
		<-quit
		fmt.Fprintln(os.Stderr, cancellationDiagnosticMarker)
	}()
	if err := os.WriteFile(os.Getenv("SPARKWING_EXEC_READY_FILE"), []byte("ready"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	select {}
}
