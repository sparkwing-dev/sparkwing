//go:build unix

package nodehost

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestHostCommandCancellationJoinsEscapedChildren(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "hold"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	const children = 16
	req := nodeproto.ExecRequest{
		ID: "cancel", Dir: dir, Capture: true,
		Argv: []string{"sh", "-c", `exec 3<> "$D/ready"
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16; do
timeout 60 sh -c 'echo $$ > "$D/$1.pid"; echo ready > "$D/ready"; read line < "$D/hold"' sh "$i" >/dev/null 2>&1 &
done
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16; do read -r line <&3; done
echo ready; wait`},
		Env: []string{"PATH=" + os.Getenv("PATH"), "D=" + dir},
	}
	kill := make(chan struct{})
	ready := 0
	_, session := runHostCommand(req, nil, kill, t.Context().Done(), func(stream string, chunk []byte, _ bool) error {
		if stream == "stdout" {
			ready += bytes.Count(chunk, []byte{'\n'})
			if ready == 1 {
				close(kill)
				ready++
			}
		}
		return nil
	}, nil)
	t.Cleanup(func() {
		if err := endCommandSessions([]*commandSession{session}); err != nil {
			t.Error(err)
		}
	})
	for i := 1; i <= children; i++ {
		path := filepath.Join(dir, strconv.Itoa(i)+".pid")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		processes, err := procgroup.ListSessions()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range processes {
			if process.PID == pid && !process.Terminated() {
				t.Errorf("cancellation returned with escaped child still alive: %+v", process)
			}
		}
	}
}
