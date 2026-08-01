package wingd_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func semReqCancel(runID, key string, cancelTimeoutMS int64) wingwire.AdmissionRequest {
	return wingwire.AdmissionRequest{
		RunID:          runID,
		SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{
			Name: key, Capacity: 1, Cost: 1,
			Policy:          wingwire.PolicyCancelOthers,
			CancelTimeoutMS: cancelTimeoutMS,
		}},
	}
}

// TestDaemon_CancelTimeoutForceReleasesNonCooperatingHolder holds a
// cancel_others semaphore on a connection that never reads its eviction
// push (a holder ignoring the cancel), then supersedes it. The daemon
// must force-release the wedged holder within the aggressor's
// CancelTimeout so it cannot pin the slot open.
func TestDaemon_CancelTimeoutForceReleasesNonCooperatingHolder(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Version: "v1", GraceWindow: -1, HeadroomFraction: -1})

	vcl := ensure(t, home, "v1")
	if _, err := vcl.Acquire(context.Background(), semReqCancel("victim", "lock", 200), nil); err != nil {
		t.Fatalf("victim acquire: %v", err)
	}

	acl := ensure(t, home, "v1")
	if _, err := acl.Acquire(context.Background(), semReqCancel("aggressor", "lock", 200), nil); err != nil {
		t.Fatalf("aggressor acquire: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if !holdsRun(qs, "victim") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("victim was never force-released after its cancel timeout")
}

func holdsRun(qs wingwire.QueueState, runID string) bool {
	for _, h := range qs.Holders {
		if h.RunID == runID {
			return true
		}
	}
	return false
}

func TestReattach_ReclaimsLeaseAfterRestart(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home, GraceWindow: 2 * time.Second})

	a := ensure(t, home, "")
	lease := mustAcquire(t, a, coreReq("a", 1))
	token := lease.Token

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}

	startDaemon(t, wingd.Config{Home: home, GraceWindow: 2 * time.Second})

	b, err := client.EnsureDaemon(context.Background(), client.Options{
		Home: home, Spawn: errSpawn, DialTimeout: time.Second, Backoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer b.Close()

	reclaimed, err := b.Reattach(context.Background(), token)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if reclaimed.RunID != "a" {
		t.Fatalf("reattached run id %q, want a", reclaimed.RunID)
	}
	if reclaimed.Token != token {
		t.Fatalf("reattach returned token %q, want %q", reclaimed.Token, token)
	}
}

func TestGraceExpiry_ReleasesUnclaimedLease(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	mustAcquire(t, a, coreReq("a", 1))

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}

	startDaemon(t, wingd.Config{Home: home, GraceWindow: 150 * time.Millisecond})
	time.Sleep(600 * time.Millisecond)

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	if len(qs.Holders) != 0 {
		t.Fatalf("expected 0 holders after grace window, got %d", len(qs.Holders))
	}
}

func TestReattach_RejectedAfterGrace(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	lease := mustAcquire(t, a, coreReq("a", 1))
	token := lease.Token

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}

	startDaemon(t, wingd.Config{Home: home, GraceWindow: 150 * time.Millisecond})
	time.Sleep(600 * time.Millisecond)

	b := ensure(t, home, "")
	_, err := b.Reattach(context.Background(), token)
	if !errors.Is(err, client.ErrReattachRejected) {
		t.Fatalf("reattach after grace: got %v, want ErrReattachRejected", err)
	}
}

// TestVersionTakeover_DrainsOldAndReattaches runs the full takeover: a
// v2 client drains the v1 daemon, brings up a v2 successor via the spawn
// hook, and the original holder reattaches to it inside the grace window.
func TestVersionTakeover_DrainsOldAndReattaches(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home, Version: "v1.0.0"})

	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("a", 1))
	token := lease.Token

	successor := newSuccessor(t, home, "v2.0.0")

	newer, err := client.EnsureDaemon(context.Background(), client.Options{
		Home:        home,
		Version:     "v2.0.0",
		Spawn:       successor.spawn,
		DialTimeout: time.Second,
		Backoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("takeover ensure: %v", err)
	}
	defer newer.Close()
	if newer.DaemonVersion() != "v2.0.0" {
		t.Fatalf("connected daemon version %q, want v2.0.0", newer.DaemonVersion())
	}

	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("old daemon should have drained and exited: %v", err)
	}

	reconnect := ensure(t, home, "v2.0.0")
	reclaimed, err := reconnect.Reattach(context.Background(), token)
	if err != nil {
		t.Fatalf("reattach after takeover: %v", err)
	}
	if reclaimed.RunID != "a" {
		t.Fatalf("reattached %q, want a", reclaimed.RunID)
	}
}

func TestRefreshRunning_ReplacesSameReleaseSourceBuildAndReattachesHolder(t *testing.T) {
	home := shortHome(t)
	oldVersion := "v0.22.2-dev+1111111"
	newVersion := "v0.22.2-dev+2222222"
	old := startDaemon(t, wingd.Config{Home: home, Version: oldVersion})

	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("active", 1))
	successor := newSuccessor(t, home, newVersion)

	result, err := client.RefreshRunning(context.Background(), client.Options{
		Home: home, Version: newVersion, Spawn: successor.spawn,
		DialTimeout: time.Second, Backoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !result.Restarted || result.PreviousVersion != oldVersion || result.RunningVersion != newVersion {
		t.Fatalf("refresh result = %+v", result)
	}
	if err := old.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("old daemon should exit: %v", err)
	}
	reclaimed, err := holder.Reattach(context.Background(), lease.Token)
	if err != nil {
		t.Fatalf("holder reattach: %v", err)
	}
	if reclaimed.RunID != "active" {
		t.Fatalf("reattached run = %q, want active", reclaimed.RunID)
	}
}

func TestRefreshRunning_LeavesStoppedDaemonStopped(t *testing.T) {
	spawned := false
	_, err := client.RefreshRunning(context.Background(), client.Options{
		Home: shortHome(t), Version: "v0.22.2-dev+2222222",
		Spawn: func(string, string) error { spawned = true; return nil },
	})
	if !errors.Is(err, client.ErrNoDaemon) {
		t.Fatalf("refresh error = %v, want ErrNoDaemon", err)
	}
	if spawned {
		t.Fatal("refresh spawned a daemon for a stopped home")
	}
}

// TestVersionTakeover_DevBuildJoinsReleaseDaemon verifies that a source-built
// "(devel)" client joins a release daemon without draining it. A fleet of
// identically-named dev binaries would otherwise each drain the shared release
// daemon, race to win the flock, and all fail. The ensure helper's errSpawn
// fires if any spawn occurs, pinning the no-takeover invariant.
func TestVersionTakeover_DevBuildJoinsReleaseDaemon(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Version: "v1.0.0"})

	dev := ensure(t, home, "(devel)")
	if dev.DaemonVersion() != "v1.0.0" {
		t.Fatalf("connected daemon version %q, want v1.0.0 (dev build must not supersede)", dev.DaemonVersion())
	}

	select {
	case err := <-td.done:
		t.Fatalf("release daemon exited (%v); the dev build drained it", err)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestVersionTakeover_ReleaseLeavesDevDaemon is the other half of the
// dev-build rule: a strictly newer release client connects to a daemon
// built from source and leaves it running. If it drained one, the two
// builds would supersede each other and each would respawn its own
// daemon over the other's for as long as both were in use. The ensure
// helper's spawn hook fails the test if it fires, so an attempted
// takeover shows up as a spawn the client had no business making.
func TestVersionTakeover_ReleaseLeavesDevDaemon(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Version: "v1.0.0+dirty"})

	rel := ensure(t, home, "v1.1.0")
	if rel.DaemonVersion() != "v1.0.0+dirty" {
		t.Fatalf("connected daemon version %q, want the source-built v1.0.0+dirty still resident", rel.DaemonVersion())
	}

	select {
	case err := <-td.done:
		t.Fatalf("source-built daemon exited (%v); the release client drained it", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestVersionTakeover_ReleaseLeavesCleanSourceDaemon(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Version: "v0.22.2-dev+e99c1800"})

	release := ensure(t, home, "v0.22.2")
	if release.DaemonVersion() != "v0.22.2-dev+e99c1800" {
		t.Fatalf("connected daemon version %q, want clean source build still resident", release.DaemonVersion())
	}

	select {
	case err := <-td.done:
		t.Fatalf("clean source daemon exited (%v); release build drained it", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestIdleExit_NoWork(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 250 * time.Millisecond})
	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("idle daemon should exit cleanly, got %v", err)
	}
}

func TestIdleExit_WaitsForHolders(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})

	a := ensure(t, home, "")
	lease := mustAcquire(t, a, coreReq("a", 1))

	time.Sleep(600 * time.Millisecond)
	select {
	case err := <-td.done:
		t.Fatalf("daemon exited while a lease was held: %v", err)
	default:
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon should idle out after release, got %v", err)
	}
}

func TestQueueState_ReportsHoldersAndWaiters(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), HeadroomFraction: -1})

	h := ensure(t, home, "")
	mustAcquire(t, h, coreReq("h", 3))

	w := ensure(t, home, "")
	pos, _ := acquireAsync(w, coreReq("w", 2))
	waitForQueue(t, pos)

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	if len(qs.Holders) != 1 || qs.Holders[0].RunID != "h" {
		t.Fatalf("holders = %+v, want one holder h", qs.Holders)
	}
	if len(qs.Waiters) != 1 || qs.Waiters[0].RunID != "w" {
		t.Fatalf("waiters = %+v, want one waiter w", qs.Waiters)
	}
	if held := resourceHeld(qs, "cores"); held != 3 {
		t.Fatalf("cores held %v, want 3", held)
	}
}

// successor lazily brings up a v2 daemon the first time the client's
// spawn hook fires, retrying the election until the drained v1 releases
// the lock.
type successor struct {
	t     *testing.T
	home  string
	ver   string
	once  sync.Once
	ready chan struct{}
}

func newSuccessor(t *testing.T, home, ver string) *successor {
	return &successor{t: t, home: home, ver: ver, ready: make(chan struct{})}
}

func (s *successor) spawn(home, version string) error {
	s.once.Do(func() { go s.bringUp() })
	return nil
}

func (s *successor) bringUp() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, err := wingd.New(wingd.Config{
			Home:        s.home,
			Version:     s.ver,
			GraceWindow: 2 * time.Second,
			Sampler:     newFakeSampler(64, 64<<30),
		})
		if err != nil {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Run(ctx) }()
		select {
		case <-d.Ready():
			s.t.Cleanup(cancel)
			close(s.ready)
			return
		case <-done:
			cancel()
			time.Sleep(20 * time.Millisecond)
		}
	}
}
