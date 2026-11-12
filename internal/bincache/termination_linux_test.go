//go:build linux

package bincache

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCompilePipelineRetainsExternalTermination(t *testing.T) {
	base, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ctx, stop := context.WithCancel(base)
	defer stop()
	ready, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer writer.Close()
	bin := t.TempDir()
	script := "#!/bin/sh\n/bin/sleep 60 &\nprintf '%s\\n' $$\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	inherited := &compileTerminationContext{Context: ctx, ready: bufio.NewReader(ready), cancel: stop}
	err = CompilePipeline(inherited, t.TempDir(), filepath.Join(t.TempDir(), "pipeline"), CompileOptions{Environment: []string{"PATH=" + bin}, Diagnostics: writer})
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("compiler wrapper hid external termination: %v", err)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("compiler wait status: %v", err)
	}
}

type compileTerminationContext struct {
	context.Context
	ready  *bufio.Reader
	cancel context.CancelFunc
	once   sync.Once
}

func (c *compileTerminationContext) Done() <-chan struct{} {
	c.once.Do(func() {
		line, err := c.ready.ReadString('\n')
		if err != nil {
			panic(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			panic(fmt.Sprintf("child PID: %q %v", line, err))
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			panic(err)
		}
		for {
			var info unix.Siginfo
			err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil && !errors.Is(err, syscall.ECHILD) {
				panic(err)
			}
			break
		}
		c.cancel()
	})
	return c.Context.Done()
}
