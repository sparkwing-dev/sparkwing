package acceptance_test

import (
	"context"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestLocalControllerW8Components(t *testing.T) {
	t.Run("kernel_identity_required", func(t *testing.T) {
		uid := uint32(1000)
		for _, goos := range []string{"linux", "darwin", "windows"} {
			owner := wingd.LocalPeerIdentity{GOOS: goos, UID: &uid, SID: "S-1-5-21-1000"}
			if subject, err := wingd.LocalSubject(owner, owner); err != nil || subject == "" {
				t.Fatalf("same-account identity refused: %q, %v", subject, err)
			}
			if _, err := wingd.LocalSubject(wingd.LocalPeerIdentity{GOOS: goos}, owner); err == nil {
				t.Fatalf("%s accepted missing kernel identity", goos)
			}
		}
	})
	t.Run("startup_stay_standalone_and_drain", func(t *testing.T) {
		runtime := &localRuntimeW8{ready: make(chan struct{}), stop: make(chan struct{})}
		lifecycle := wingd.NewLocalLifecycle(runtime, nil)
		opts := wingd.LocalStartOptions{Home: t.TempDir(), Version: "v1"}
		status, err := lifecycle.EnsureStarted(t.Context(), opts)
		if err != nil || !status.Running {
			t.Fatalf("startup: %+v, %v", status, err)
		}
		opts.Stay = true
		status, err = lifecycle.StartDashboard(t.Context(), opts)
		if err != nil || !status.Stay {
			t.Fatalf("stay: %+v, %v", status, err)
		}
		opts.Standalone = true
		if _, err := lifecycle.EnsureStarted(t.Context(), opts); err == nil {
			t.Fatal("standalone accepted an owned store")
		}
		if err := lifecycle.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if runtime.starts != 1 || runtime.drains != 1 {
			t.Fatalf("starts=%d drains=%d", runtime.starts, runtime.drains)
		}
	})
}

type localRuntimeW8 struct {
	mu             sync.Mutex
	status         wingd.LocalStatus
	ready, stop    chan struct{}
	starts, drains int
}

func (r *localRuntimeW8) Run(ctx context.Context, opts wingd.LocalStartOptions) error {
	r.mu.Lock()
	r.starts++
	r.status = wingd.LocalStatus{Running: true, Stay: opts.Stay, Version: opts.Version}
	r.mu.Unlock()
	close(r.ready)
	select {
	case <-r.stop:
	case <-ctx.Done():
	}
	r.mu.Lock()
	r.status.Running = false
	r.mu.Unlock()
	return nil
}

func (r *localRuntimeW8) AwaitReady(ctx context.Context) (wingd.LocalStatus, error) {
	select {
	case <-r.ready:
		return r.Status(ctx)
	case <-ctx.Done():
		return wingd.LocalStatus{}, ctx.Err()
	}
}

func (r *localRuntimeW8) Status(context.Context) (wingd.LocalStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, nil
}

func (r *localRuntimeW8) SetStay(_ context.Context, stay bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Stay = stay
	return nil
}

func (r *localRuntimeW8) EnsureDashboard(context.Context, string) error { return nil }

func (r *localRuntimeW8) Drain(context.Context) error {
	r.drains++
	close(r.stop)
	return nil
}
