//go:build unix

package nodehost

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// A background child that holds stdin without reading must not hold the command open.
func TestHostCommandReturnsWhenABackgroundChildHoldsStdin(t *testing.T) {
	dir := t.TempDir()
	pidFile := dir + "/held.pid"
	req := nodeproto.ExecRequest{
		ID: "1", Argv: []string{"sh", "-c", `exec 3<&0; sleep 30 <&3 & echo $! > "$P"; exit 0`}, Dir: dir,
		Env: []string{"PATH=" + os.Getenv("PATH"), "P=" + pidFile}, Stdin: bytes.Repeat([]byte("x"), 256<<10),
	}
	finished := make(chan *commandSession, 1)
	go func() {
		_, session := runHostCommand(req, make(chan struct{}), make(chan struct{}), func(string, []byte, bool) error { return nil })
		finished <- session
	}()
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	select {
	case session := <-finished:
		if err := endCommandSessions([]*commandSession{session}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command never finished while a background child held its stdin")
	}
}
