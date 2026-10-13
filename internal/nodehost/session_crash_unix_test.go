//go:build unix

package nodehost

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

type stdoutSink struct{}

func (stdoutSink) Text(string, string)            {}
func (stdoutSink) Record(rec nodeproto.LogRecord) { fmt.Println(rec.Msg) }

// safety: the fake host starts a fake SDK whose node leaves a grandchild running, prints the grandchild's pid
// and waits to be killed, as a claimed trigger's host waits on its nodes.
func crashHost() {
	s, err := StartSession(context.Background(), SessionConfig{
		Entry: os.Args[0], Env: append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0"), Sink: stdoutSink{},
	})
	if err != nil {
		panic(err)
	}
	go func() {
		_, _ = s.Call(context.Background(), runNode("grandchild"))
	}()
	select {}
}

func TestKillingASessionLeadingHostReapsItsNodes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cmd := exec.Command(os.Args[0])
	cmd.Env, cmd.Stdout, cmd.Stderr = append(os.Environ(), fakeSDKEnv+"=crash-host"), w, os.Stderr
	host, err := procgroup.StartSession(cmd)
	_ = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Kill() }()
	if err := r.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("host printed %q, want the grandchild pid", line)
	}
	if err := host.Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// safety: Finish reports the leader's SIGKILL; what matters is that it returns once the session is empty.
	_ = host.Finish(ctx, time.Second)
	processes, err := procgroup.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range processes {
		if p.PID == grandchild && !p.Terminated() {
			_ = exec.Command("kill", "-9", strconv.Itoa(grandchild)).Run()
			t.Fatalf("the host's session was reaped, but its node's process %d still runs", grandchild)
		}
	}
}
