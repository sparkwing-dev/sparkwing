package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// blockQueueSocket strips search permission from the directory holding home's
// daemon socket, so a dial fails with EACCES the way a sandbox denial does
// rather than with the ENOENT an idle machine produces.
func blockQueueSocket(t *testing.T, home string) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatalf("place socket file: %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

const queueBlindnessWait = 10 * time.Second

// queueHome returns a scratch sparkwing home whose daemon socket stays inside
// the OS length limit, which t.TempDir cannot promise on macOS.
func queueHome(t *testing.T) string {
	t.Helper()
	previousVersion := Version
	Version = "v1.0.0"
	t.Cleanup(func() { Version = previousVersion })
	dir, err := os.MkdirTemp("/tmp", "swq")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serveQueueDaemon runs an idle daemon for home until the test ends.
func serveQueueDaemon(t *testing.T, home string) {
	t.Helper()
	d, err := wingd.New(queueDaemonConfig(home))
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(queueBlindnessWait):
			t.Error("daemon did not stop")
		}
	})
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(queueBlindnessWait):
		t.Fatal("daemon never became ready")
	}
}

func queueDaemonConfig(home string) wingd.Config {
	return wingd.Config{Home: home, Version: "v1.0.0", Sampler: queueTestHostSampler{}}
}

type queueTestHostSampler struct{}

func (queueTestHostSampler) Sample() (wingd.HostStat, error) {
	return wingd.HostStat{
		TotalCores:      8,
		TotalMemoryBytes: 8 << 30,
		FreeMemoryBytes:  8 << 30,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	}, nil
}

func TestQueueDaemonConfigUsesDeterministicHostSample(t *testing.T) {
	sampler := queueDaemonConfig(t.TempDir()).Sampler
	if sampler == nil {
		t.Fatal("queue test daemon uses the live host sampler")
	}
	stat, err := sampler.Sample()
	if err != nil {
		t.Fatalf("sample test host: %v", err)
	}
	if stat.TotalCores != 8 || stat.TotalMemoryBytes != 8<<30 || stat.BusyCores != 0 {
		t.Fatalf("test host sample = %+v, want idle 8-core, 8 GiB host", stat)
	}
}

func queueOutput(t *testing.T, home string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = runQueue([]string{"--home", home, "-o", "json"})
	})
	if err != nil {
		t.Fatalf("queue --home %s: %v", home, err)
	}
	return out
}

// TestRunQueue_NoDaemonDoesNotPrintWhatAnIdleDaemonPrints is the command-level
// negative control. `sparkwing queue -o json` printed `{}` when it had not
// reached the daemon, which is what an idle machine prints, so the command
// that promises "the truthful view of local admission" reported a quiet queue
// while it was blind.
func TestRunQueue_NoDaemonDoesNotPrintWhatAnIdleDaemonPrints(t *testing.T) {
	quiet := queueHome(t)
	idle := queueHome(t)
	serveQueueDaemon(t, idle)

	withoutDaemon := queueOutput(t, quiet)
	withIdleDaemon := queueOutput(t, idle)

	if withoutDaemon == withIdleDaemon {
		t.Fatalf("queue prints the same thing with and without a daemon:\n%s", withoutDaemon)
	}
	if strings.TrimSpace(withoutDaemon) == "{}" {
		t.Fatalf("queue with no daemon still prints a bare {}:\n%s", withoutDaemon)
	}
	if !strings.Contains(withoutDaemon, `"reachable": false`) {
		t.Errorf("queue with no daemon does not say the daemon was not reached:\n%s", withoutDaemon)
	}
	if !strings.Contains(withIdleDaemon, `"reachable": true`) {
		t.Errorf("queue against a live daemon does not say the daemon was reached:\n%s", withIdleDaemon)
	}
}

// A daemon that could not be reached must fail rather than exit 0 with an
// empty queue, and it must fail with the infrastructure code so a script can
// tell "I could not look" from "the queue is empty".
func TestRunQueue_UnreachableDaemonExitsWithTheInfrastructureCode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reaches a socket whatever its directory mode")
	}
	home := queueHome(t)
	blockQueueSocket(t, home)

	var err error
	out := captureStdout(t, func() {
		err = runQueue([]string{"--home", home, "-o", "json"})
	})
	if err == nil {
		t.Fatal("queue exited 0 against a daemon it could not reach")
	}
	if code := exitCodeFor(err); code != 4 {
		t.Errorf("exit code = %d, want 4 (infrastructure)", code)
	}
	if !strings.Contains(out, `"state": "unreachable"`) {
		t.Errorf("queue did not print the unreachable state:\n%s", out)
	}
	if strings.TrimSpace(out) == "{}" {
		t.Errorf("queue printed an empty queue for a daemon it never reached:\n%s", out)
	}
}
