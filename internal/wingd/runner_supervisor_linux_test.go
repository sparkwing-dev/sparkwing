package wingd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestLocalRunnerWithoutSystemdSurvivesStartupCancellation(t *testing.T) {
	host := runnerSupervisorHost(t)
	host.SparkwingHome = t.TempDir()
	host.Binary = filepath.Join(t.TempDir(), "runner")
	for _, name := range []string{"ready", "control", "finished"} {
		if err := unix.Mkfifo(filepath.Join(host.SparkwingHome, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$SPARKWING_HOME/ready\"\n" +
		"IFS= read -r value < \"$SPARKWING_HOME/control\"\n" +
		"printf '%s\\n' \"$value\" > \"$SPARKWING_HOME/finished\"\n"
	if err := fssecure.WriteFile(host.Binary, []byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(host.Binary, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := startLocalRunnerLinux(ctx, host, "instance", filepath.Join(t.TempDir(), "no-systemd")); err != nil {
		t.Fatal(err)
	}
	readFIFO := func(name string) string {
		t.Helper()
		file, err := os.Open(filepath.Join(host.SparkwingHome, name))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		line, err := bufio.NewReader(file).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(line)
	}
	pid, err := strconv.Atoi(readFIFO("ready"))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Kill()
		_ = process.Release()
	})
	if session, err := unix.Getsid(pid); err != nil || session != pid {
		t.Fatalf("runner session = %d, %v; want own session %d", session, err, pid)
	}
	cancel()
	control, err := os.OpenFile(filepath.Join(host.SparkwingHome, "control"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(control, "survived"); err != nil {
		t.Fatal(err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFIFO("finished"); got != "survived" {
		t.Fatalf("detached runner result = %q", got)
	}
}
