//go:build unix

package nodehost

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

type cancelProcessSampler struct{}

func (cancelProcessSampler) Sample() (wingd.HostStat, error) {
	return wingd.HostStat{TotalCores: 2, TotalMemoryBytes: 8 << 30}, nil
}

func TestMachineLeaseWatchMeasuresCancelToJobProcessExit(t *testing.T) {
	home := t.TempDir()
	controller, err := wingd.New(wingd.Config{Home: home, Version: "test", Sampler: cancelProcessSampler{}, HeadroomFraction: -1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-controller.Ready():
	case err := <-done:
		done <- err
		t.Fatalf("controller startup: %v", err)
	}
	client, err := wingdclient.EnsureDaemon(t.Context(), wingdclient.Options{Home: home, Version: "test", NoTakeover: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	admission := MachineAdmission(home, "test")
	started := make(chan *protocolSession, 1)
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- admission.node(ctx, localAdmissionSeal(t, localAdmissionOptions()), localAdmissionRow("job"), func(ctx context.Context) error {
			session, err := startProtocolSession(ctx, ProtocolSessionConfig{
				Command: []string{binary, "-test.run=^TestProtocolSessionChild$", "--", "cancel"}, Environment: testProcessEnv(t, "GORACE=atexit_sleep_ms=0"),
				ParseFrame: protocolTestCodec,
				Log: func(_ context.Context, log nodeproto.FrameLog) error {
					if log.Message == "entered" {
						close(entered)
					}
					return nil
				},
			})
			if session != nil {
				defer func() { _ = session.Close() }()
			}
			if err != nil {
				started <- nil
				return err
			}
			_, err = session.Request(ctx, nodeproto.Request{
				Op: nodeproto.OpHello, Protocol: nodeproto.Version,
				Engine: &nodeproto.EngineIdentity{Version: "test"}, Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
			})
			if err != nil {
				started <- nil
				return err
			}
			started <- session
			_, err = session.Request(ctx, nodeproto.Request{Op: nodeproto.OpRunStep, Job: "job", Step: "body", Attempt: 1, Settled: []string{}})
			return errors.Join(err, context.Cause(ctx))
		})
	}()
	var session *protocolSession
	select {
	case session = <-started:
	case err := <-finished:
		t.Fatalf("admission failed: %v", err)
	}
	if session == nil {
		t.Fatalf("start admitted process: %v", <-finished)
	}
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("job startup: %v", err)
	}
	start := time.Now()
	found, err := client.CancelLease(t.Context(), testRunID)
	if err != nil || !found {
		stop()
		<-finished
		t.Fatalf("cancel lease: found=%t error=%v", found, err)
	}
	<-session.process.LeaderExited()
	latency := time.Since(start)
	if err := <-finished; !errors.As(err, new(*lease.CancelError)) {
		t.Fatalf("job did not stop through its lease watch: %v", err)
	}
	t.Logf("cancel request to observed job process exit over the verified API socket: %s", latency)
}
