package wingd_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type lifecycleIdleProcSampler struct{}

func (lifecycleIdleProcSampler) CPUUsage(int) (wingd.ProcUsage, bool) {
	return wingd.ProcUsage{}, true
}

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
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if !holdsRun(qs, "victim") {
			return
		}
		<-poll.C
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

func waitForRecoveredHolderRelease(t *testing.T, q *client.Client, runID string) {
	t.Helper()
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	if !holdsRun(qs, runID) {
		t.Fatalf("recovered holder %q was not visible during its grace window", runID)
	}

	deadline := time.Now().Add(3 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for holdsRun(qs, runID) && time.Now().Before(deadline) {
		<-poll.C
		qs, err = q.QueueState(context.Background())
		if err != nil {
			t.Fatalf("queue state: %v", err)
		}
	}
	if holdsRun(qs, runID) {
		t.Fatalf("recovered holder %q was not released after its grace window", runID)
	}
}

// TestDaemon_StalledHolderMustAnswerLivenessChallenge proves that stall
// detection is an automatic backstop, not only a queue annotation. The
// victim remains alive and keeps its socket open, but never runs Watch and
// therefore cannot answer a daemon challenge. A healthy zero-CPU holder does
// run Watch and must retain its lease for the same observation window.
func TestDaemon_StalledHolderMustAnswerLivenessChallenge(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: -1, HeadroomFraction: -1,
		ProcSampler: lifecycleIdleProcSampler{}, StallInterval: 5 * time.Millisecond,
		StallWindow: 10 * time.Millisecond, StallProbeTimeout: 25 * time.Millisecond,
	})

	wedgedClient := ensure(t, home, "v1")
	wedgedReq := wingwire.AdmissionRequest{RunID: "wedged", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{{Name: "wedged-lock", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}}}
	wedgedReq.PID = os.Getpid()
	if _, err := wedgedClient.Acquire(context.Background(), wedgedReq, nil); err != nil {
		t.Fatalf("wedged acquire: %v", err)
	}
	healthyClient := ensure(t, home, "v1")
	healthyReq := wingwire.AdmissionRequest{RunID: "healthy", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{{Name: "healthy-lock", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}}}
	healthyReq.PID = os.Getpid()
	healthy := mustAcquire(t, healthyClient, healthyReq)
	go healthy.Watch(nil)

	waiterClient := ensure(t, home, "v1")
	waiter := make(chan error, 1)
	go func() {
		lease, err := waiterClient.Acquire(context.Background(), wingwire.AdmissionRequest{
			RunID: "waiter", SemaphoresOnly: true,
			Semaphores: []wingwire.SemaphoreClaim{
				{Name: "wedged-lock", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue},
				{Name: "healthy-lock", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue},
			},
		}, nil)
		if err == nil {
			err = lease.Release()
		}
		waiter <- err
	}()

	deadline := time.Now().Add(time.Second)
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if !holdsRun(qs, "wedged") {
			if !holdsRun(qs, "healthy") {
				t.Fatal("healthy zero-CPU holder was reclaimed")
			}
			_ = healthy.Release()
			select {
			case err := <-waiter:
				if err != nil {
					t.Fatalf("waiter acquire: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("waiter was not promoted")
			}
			return
		}
		<-poll.C
	}
	t.Fatal("live but unresponsive holder was not reclaimed")
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
		Home: home, Version: "v1.0.0", Spawn: errSpawn, DialTimeout: time.Second, Backoff: 10 * time.Millisecond,
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

	startDaemon(t, wingd.Config{Home: home, GraceWindow: 300 * time.Millisecond})
	started := time.Now()
	q := ensure(t, home, "")
	waitForRecoveredHolderRelease(t, q, "a")
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Errorf("grace expiry observation took %v, want less than 500ms", elapsed)
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

	startDaemon(t, wingd.Config{Home: home, GraceWindow: 300 * time.Millisecond})
	started := time.Now()
	b := ensure(t, home, "")
	waitForRecoveredHolderRelease(t, b, "a")

	_, err := b.Reattach(context.Background(), token)
	if !errors.Is(err, client.ErrReattachRejected) {
		t.Fatalf("reattach after grace: got %v, want ErrReattachRejected", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Errorf("reattach rejection observation took %v, want less than 500ms", elapsed)
	}
}

// TestVersionTakeover_DrainsOldAndReattaches runs the full takeover: a
// v2 client drains the v1 daemon, brings up a v2 successor via the spawn
// hook, and the original holder reattaches to it inside the grace window.
func TestVersionTakeover_DrainsOldAndReattaches(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home, Version: "v1.0.0"})

	holder := ensure(t, home, "v1.0.0")
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

func TestVersionTakeover_ExactSourceBuildDrainsSameReleaseAndReattaches(t *testing.T) {
	home := shortHome(t)
	old := startDaemon(t, wingd.Config{Home: home, Version: "v0.22.2"})

	holder := ensure(t, home, "v0.22.2")
	lease := mustAcquire(t, holder, coreReq("same-release-holder", 1))
	newVersion := "v0.22.2-dev+1b9e5cd9"
	successor := newSuccessor(t, home, newVersion)

	newer, err := client.EnsureDaemon(context.Background(), client.Options{
		Home: home, Version: newVersion, Spawn: successor.spawn,
		DialTimeout: time.Second, Backoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("source takeover: %v", err)
	}
	defer newer.Close()
	if newer.DaemonVersion() != newVersion {
		t.Fatalf("connected daemon version %q, want %q", newer.DaemonVersion(), newVersion)
	}
	if err := old.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("release daemon should have drained and exited: %v", err)
	}
	reconnect := ensure(t, home, newVersion)
	reclaimed, err := reconnect.Reattach(context.Background(), lease.Token)
	if err != nil {
		t.Fatalf("holder reattach: %v", err)
	}
	if reclaimed.RunID != "same-release-holder" {
		t.Fatalf("reattached %q, want same-release-holder", reclaimed.RunID)
	}
}

func TestRefreshRunning_ReplacesSameReleaseSourceBuildAndReattachesHolder(t *testing.T) {
	home := shortHome(t)
	oldVersion := "v0.22.2-dev+1111111"
	newVersion := "v0.22.2-dev+2222222"
	old := startDaemon(t, wingd.Config{Home: home, Version: oldVersion})

	holder := ensure(t, home, oldVersion)
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
	reconnect := ensure(t, home, newVersion)
	reclaimed, err := reconnect.Reattach(context.Background(), lease.Token)
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

func TestVersionTakeover_DevBuildAcceptsSameSourceReleaseDaemon(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Version: "v1.0.0"})

	_, err := client.EnsureDaemon(context.Background(), client.Options{Home: home, Version: "(devel)", Spawn: errSpawn})
	if err != nil {
		t.Fatalf("ensure error = %v, want success", err)
	}

	select {
	case err := <-td.done:
		t.Fatalf("release daemon exited (%v); the dev build drained it", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestVersionTakeover_ReleaseAcceptsSameSourceDirtyDevDaemon(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Version: "v1.0.0+dirty"})

	_, err := client.EnsureDaemon(context.Background(), client.Options{Home: home, Version: "v1.1.0", Spawn: errSpawn})
	if err != nil {
		t.Fatalf("ensure error = %v, want success", err)
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
	const idleTimeout = 300 * time.Millisecond
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: idleTimeout})

	a := ensure(t, home, "")
	lease := mustAcquire(t, a, coreReq("a", 1))

	started := time.Now()
	time.Sleep(idleTimeout + 100*time.Millisecond)
	select {
	case err := <-td.done:
		t.Fatalf("daemon exited while a lease was held: %v", err)
	default:
	}
	if elapsed := time.Since(started); elapsed >= 550*time.Millisecond {
		t.Fatalf("held-lease observation took %s, want under 550ms", elapsed)
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
	if ver == "" {
		ver = "v1.0.0"
	}
	return &successor{t: t, home: home, ver: ver, ready: make(chan struct{})}
}

func (s *successor) spawn(home, version string) error {
	s.once.Do(func() { go s.bringUp() })
	return nil
}

func (s *successor) bringUp() {
	deadline := time.Now().Add(5 * time.Second)
	retry := time.NewTicker(20 * time.Millisecond)
	defer retry.Stop()
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
			<-retry.C
		}
	}
}
