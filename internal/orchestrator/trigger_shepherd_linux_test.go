//go:build linux

package orchestrator

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	shepherdMode     = "SPARKWING_SHEPHERD_TEST_MODE"
	shepherdBehavior = "SPARKWING_SHEPHERD_TEST_BEHAVIOR"
	shepherdRecord   = "SPARKWING_SHEPHERD_TEST_RECORD"
)

func shepherdHelper(mode string, session bool) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=^TestShepherdFixture$")
	c.Env = append(os.Environ(), shepherdMode+"="+mode)
	if session {
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	return c
}

func startReady(t *testing.T, c *exec.Cmd) {
	t.Helper()
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || !strings.Contains(line, "READY") {
		t.Fatalf("start: %q %v", line, err)
	}
}

func appendRecord(t *testing.T, line string) {
	t.Helper()
	f, err := os.OpenFile(os.Getenv(shepherdRecord), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// safety: fixtures: "shepherd" runs ShepherdTrigger over "host"; "host" starts a node by the behavior and
// leaves, crashes or waits for a signal; "branch" starts ten grandchildren that lead sessions of their own.
func TestShepherdFixture(t *testing.T) {
	behavior := os.Getenv(shepherdBehavior)
	switch os.Getenv(shepherdMode) {
	case "shepherd":
		// safety: the shepherd passes its own environment to the host, so the host's mode is set here.
		if err := os.Setenv(shepherdMode, "host"); err != nil {
			t.Fatal(err)
		}
		host := shepherdHelper("host", false)
		_, err := ShepherdTrigger(append([]string{host.Path}, host.Args[1:]...))
		appendRecord(t, "DONE")
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		os.Exit(0)
	case "host":
		switch behavior {
		case "exit":
			os.Exit(37)
		case "signal":
			caught := make(chan os.Signal, 1)
			signal.Notify(caught, syscall.SIGTERM)
			fmt.Println("HOST-READY")
			appendRecord(t, "GOT "+(<-caught).String())
			os.Exit(23)
		}
		node := shepherdHelper("hold", behavior == "detached" || behavior == "crash")
		if behavior == "tree" || behavior == "moved-tree" {
			node = shepherdHelper("branch", behavior == "tree")
		}
		startReady(t, node)
		appendRecord(t, fmt.Sprintf("node=%d", node.Process.Pid))
		if behavior == "crash" {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
		os.Exit(0)
	case "branch":
		// safety: moved into its session's leading group, which is the host's, the group a reap that shared
		// the shepherd's group would have signaled it through.
		if behavior == "moved-tree" {
			sid, err := unix.Getsid(0)
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.Setpgid(0, sid); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 10 {
			leaf := shepherdHelper("hold", true)
			startReady(t, leaf)
			appendRecord(t, fmt.Sprintf("leaf%d=%d", i, leaf.Process.Pid))
		}
		fmt.Println("READY")
		select {}
	case "hold":
		fmt.Println("READY")
		select {}
	default:
		t.Skip("fixture")
	}
}

func alive(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(raw), ')')
	return i >= 0 && !strings.HasPrefix(string(raw[i+2:]), "Z ")
}

func runShepherd(t *testing.T, behavior string) (string, error) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv(shepherdRecord, record)
	t.Setenv(shepherdBehavior, behavior)
	err := shepherdHelper("shepherd", true).Run()
	raw, _ := os.ReadFile(record)
	return string(raw), err
}

func TestAShepherdReapsEverythingItsHostLeft(t *testing.T) {
	for _, behavior := range []string{"detached", "crash", "tree", "plain", "moved-tree"} {
		t.Run(behavior, func(t *testing.T) {
			raw, err := runShepherd(t, behavior)
			// safety: a crashed host's kill is its exit status, which the shepherd reports.
			if !strings.Contains(raw, "DONE") || (err != nil && behavior != "crash") {
				t.Fatalf("the shepherd did not finish (err %v): it signaled itself while reaping?\n%s", err, raw)
			}
			for _, line := range strings.Split(raw, "\n") {
				name, value, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				pid, _ := strconv.Atoi(value)
				if alive(pid) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Errorf("%s %d outlived the shepherd", name, pid)
				}
			}
		})
	}
}

func TestAShepherdPassesOnTheHostsExit(t *testing.T) {
	_, err := runShepherd(t, "exit")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 {
		t.Fatalf("shepherd ended %v, want the host's exit status 37", err)
	}
}

func TestAShepherdPassesACancelToItsHost(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv(shepherdRecord, record)
	t.Setenv(shepherdBehavior, "signal")
	shepherd := shepherdHelper("shepherd", true)
	out, err := shepherd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := shepherd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(out)
	for lines.Scan() && !strings.Contains(lines.Text(), "HOST-READY") {
	}
	if err := syscall.Kill(-shepherd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for lines.Scan() {
	}
	err = shepherd.Wait()
	raw, _ := os.ReadFile(record)
	var exit *exec.ExitError
	if !strings.Contains(string(raw), "GOT terminated") || !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("shepherd ended %v with record %q, want the host to get the cancel and exit 23", err, raw)
	}
}
