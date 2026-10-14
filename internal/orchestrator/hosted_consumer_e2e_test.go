//go:build unix

package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// safety: the resident consumer, not the parent's trigger loop, claims the awaited child here, so the child
// has no process of its parent to inherit a lease from. Its parent node holds 1.5 of the 2-core budget and
// the child's node needs 1, so a child that did not attach through the daemon would wait on its own parent.
func TestConsumerClaimedChildAttachesToItsParentNode(t *testing.T) {
	repo, cli := leaseFixture(t)
	home := t.TempDir()
	stopHomeDaemon(t, home)
	config := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(config, []byte("admission:\n  budget: 2,ignore-external\nrun:\n  submit_env_allow: [C3_*]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "SPARKWING_HOME="+home, "SPARKWING_CONFIG="+config,
		"SPARKWING_WINGD_BIN="+cli, "C3_MARKER="+marker, "SPARKWING_DRY_RUN=",
		"GOCACHE="+goEnv(t, "GOCACHE"), "GOMODCACHE="+goEnv(t, "GOMODCACHE"), "GOWORK=off", "GOTOOLCHAIN=local")
	t.Setenv("SPARKWING_CONFIG", config)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	daemon, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, NoTakeover: true, Spawn: func(home, _ string) error {
		cmd := exec.Command(cli, "wingd", wingdclient.DaemonSpawnVerb, "--home", home)
		cmd.Env = env
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()
	hold := func(req wingwire.AdmissionRequest) *wingdclient.Lease {
		cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, NoTakeover: true})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := cl.Acquire(ctx, req, nil)
		if err != nil {
			t.Fatalf("hold %s: %v", req.RunID, err)
		}
		//nolint:contextcheck // the watch answers liveness probes until Release ends it, not a context.
		go func() { _ = lease.WatchOwnership(nil, nil, nil) }()
		t.Cleanup(func() { _ = lease.Release() })
		return lease
	}
	run := hold(wingwire.AdmissionRequest{RunID: "parent-run", Pipeline: "node-owned", SemaphoresOnly: true})
	hold(wingwire.AdmissionRequest{
		RunID: wingwire.NodeHostParticipant("parent-run", "call"), OwnerRunID: "parent-run", OwnerLeaseToken: run.Token,
		SubLease: true, Resources: wingwire.HostResources{Cores: 1.5},
	})

	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "parent-run", Pipeline: "node-owned", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	const child = "consumer-child"
	if err := orchestrator.CaptureSubmissionEnvironment(home, child, env, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: child, Pipeline: "leaf", TriggerSource: "await-pipeline", CreatedAt: time.Now(),
		ParentRunID: "parent-run", ParentNodeID: "call",
		TriggerEnv: map[string]string{orchestrator.SubmitRepoDirKey: repo, orchestrator.SubmissionEnvironmentCapturedKey: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	// safety: the consumer exits once idle with nothing in flight, so its exit is the child run's end; a child
	// queued behind its own parent keeps it running until the context kills it.
	consumer := exec.CommandContext(ctx, cli, "__runs-consume", "--home", home, "--idle", "2s")
	consumer.Env = env
	consumer.Stdout, consumer.Stderr = io.Discard, io.Discard
	consumer.WaitDelay = 5 * time.Second
	if err := consumer.Run(); err != nil && ctx.Err() == nil {
		t.Fatalf("consumer: %v", err)
	}
	got, err := st.GetRun(ctx, child)
	if err != nil {
		t.Fatalf("the consumer never ran the child (%v): %v", ctx.Err(), err)
	}
	if got.Status != "success" || got.ParentRunID != "parent-run" {
		t.Fatalf("child run ended %s (parent %q, context %v): %s", got.Status, got.ParentRunID, ctx.Err(), got.Error)
	}
	if _, err := os.Stat(filepath.Join(marker, "leaf.real")); err != nil {
		t.Fatalf("the child's node never ran its body: %v", err)
	}
}
