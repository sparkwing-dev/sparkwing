package supervise

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

// daemonChild hosts a real in-process daemon behind the supervisor's Child
// interface, so the loop is exercised against actual idle-exit behavior
// rather than a scripted exit.
type daemonChild struct {
	done chan error
	stop context.CancelFunc
}

func (c *daemonChild) Wait() <-chan error { return c.done }
func (c *daemonChild) Terminate() error   { c.stop(); return nil }
func (c *daemonChild) Kill() error        { c.stop(); return nil }

// TestLoopReapsDaemonThatIdlesOutUnderHealthProbes is the regression pin
// for supervised daemons that could never idle out: the loop's own health
// probes counted as daemon activity, so the supervise+run pair survived
// forever in homes nothing would ever use again. With the probe riding
// the health-probe path, a daemon whose only traffic is its supervisor
// idles out within its window, and the loop treats that clean exit as
// the end of supervision instead of restarting the child.
func TestLoopReapsDaemonThatIdlesOutUnderHealthProbes(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "wsup")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	starts := 0
	deps := Deps{
		Start: func() (Child, error) {
			starts++
			d, err := wingd.New(wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			child := &daemonChild{done: make(chan error, 1), stop: cancel}
			go func() { child.done <- d.Run(ctx) }()
			select {
			case <-d.Ready():
			case err := <-child.done:
				cancel()
				t.Fatalf("daemon exited before ready: %v", err)
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("daemon never became ready")
			}
			return child, nil
		},
		Probe: func(ctx context.Context) error {
			return wingdclient.HealthProbe(ctx, home)
		},
		Logf: t.Logf,
	}
	cfg := Config{
		ProbeInterval: 50 * time.Millisecond,
		ProbeTimeout:  time.Second,
		FailureLimit:  3,
		TermGrace:     time.Second,
	}

	loopDone := make(chan error, 1)
	go func() { loopDone <- Loop(context.Background(), cfg, deps) }()
	select {
	case err := <-loopDone:
		if err != nil {
			t.Fatalf("supervision should end with the daemon's clean idle exit, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor still running: health probes kept the daemon from idling out")
	}
	if starts != 1 {
		t.Errorf("supervisor started the daemon %d times; a clean idle exit must end supervision without a restart", starts)
	}
}
