//go:build unix

package orchestrator

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// safety: the stand-in node session's leader starts a descendant in a process group of its own, which a
// group-wide kill would miss and a session-wide one reaches.
func TestAClaimerReapsTheNodeSessionsItsHostLeft(t *testing.T) {
	cmd := exec.Command("sh", "-c", `set -m; sleep 300 & echo $!; wait`)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	node, err := procgroup.StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node.Terminate(context.Background(), time.Second) }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	descendant, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("descendant pid %q", line)
	}
	log := filepath.Join(t.TempDir(), "sessions")
	sessionRecorder(log)(cmd.Process.Pid)

	reapRecordedSessions(log, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_ = node.Finish(ctx, time.Second)
	processes, err := procgroup.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range processes {
		if p.PID == descendant && !p.Terminated() {
			_ = exec.Command("kill", "-9", strconv.Itoa(descendant)).Run()
			t.Fatalf("the recorded node session's descendant %d outlived the claimer's reap", descendant)
		}
	}
}
