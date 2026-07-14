package wingd_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func coreReq(runID string, cores float64) wingwire.AdmissionRequest {
	return wingwire.AdmissionRequest{
		RunID:     runID,
		Resources: wingwire.HostResources{Cores: cores},
	}
}

func TestElection_ExactlyOneWinner(t *testing.T) {
	home := shortHome(t)
	const n = 8
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemons := make([]*wingd.Daemon, n)
	errs := make(chan error, n)
	for i := range daemons {
		d, err := wingd.New(wingd.Config{Home: home, Sampler: newFakeSampler(8, 8<<30)})
		if err != nil {
			t.Fatalf("new daemon %d: %v", i, err)
		}
		daemons[i] = d
	}
	var wg sync.WaitGroup
	for _, d := range daemons {
		wg.Add(1)
		go func(d *wingd.Daemon) {
			defer wg.Done()
			errs <- d.Run(ctx)
		}(d)
	}

	var winners int
	deadline := time.After(3 * time.Second)
	for winners == 0 {
		for _, d := range daemons {
			select {
			case <-d.Ready():
				winners++
			default:
			}
		}
		if winners == 0 {
			select {
			case <-deadline:
				t.Fatal("no daemon won the election")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	lost := 0
	loseDeadline := time.After(3 * time.Second)
	for lost < n-1 {
		select {
		case err := <-errs:
			if !errors.Is(err, wingd.ErrNotElected) {
				t.Fatalf("loser returned %v, want ErrNotElected", err)
			}
			lost++
		case <-loseDeadline:
			t.Fatalf("only %d of %d losers reported ErrNotElected", lost, n-1)
		}
	}
	if winners != 1 {
		t.Fatalf("saw %d ready daemons, want exactly 1", winners)
	}
	cancel()
	wg.Wait()
}

func TestHolderDisconnect_ReleasesAndPromotes(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "deploy", 1, 1, wingwire.PolicyQueue))
	_ = holder

	b := ensure(t, home, "")
	positions, resultB := acquireAsync(b, semReq("b", "deploy", 1, 1, wingwire.PolicyQueue))
	select {
	case q := <-positions:
		if q.Position < 1 {
			t.Fatalf("b queued at position %d, want >=1", q.Position)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b never reported a queue position")
	}

	a.Close()

	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have been promoted, got %v", r.err)
	}
	if r.lease.RunID != "b" {
		t.Fatalf("promoted lease run id %q, want b", r.lease.RunID)
	}
}

func TestPromotionRebroadcastsRemainingWaiterPosition(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "deploy", 1, 1, wingwire.PolicyQueue))

	b := ensure(t, home, "")
	positionsB, resultB := acquireAsync(b, semReq("b", "deploy", 1, 1, wingwire.PolicyQueue))
	select {
	case q := <-positionsB:
		if q.Position != 1 {
			t.Fatalf("b initial position = %d, want 1", q.Position)
		}
	case r := <-resultB:
		t.Fatalf("b resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("b never reported its initial queue position")
	}

	c := ensure(t, home, "")
	positionsC, resultC := acquireAsync(c, semReq("c", "deploy", 1, 1, wingwire.PolicyQueue))
	select {
	case q := <-positionsC:
		if q.Position != 2 {
			t.Fatalf("c initial position = %d, want 2", q.Position)
		}
	case r := <-resultC:
		t.Fatalf("c resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("c never reported its initial queue position")
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have promoted, got %v", r.err)
	}

	select {
	case q := <-positionsC:
		if q.Position != 1 {
			t.Fatalf("c refreshed position = %d, want 1", q.Position)
		}
	case r := <-resultC:
		t.Fatalf("c resolved instead of remaining queued: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("c never received refreshed queue position after b promoted")
	}
}

func TestPositionRebroadcastKeepsIndependentQueuesSeparate(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	semOnly := func(runID, key string) wingwire.AdmissionRequest {
		return wingwire.AdmissionRequest{
			RunID:          runID,
			SemaphoresOnly: true,
			Semaphores: []wingwire.SemaphoreClaim{
				{Name: key, Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue},
			},
		}
	}

	a := ensure(t, home, "")
	holderA := mustAcquire(t, a, semOnly("a", "deploy-a"))
	b := ensure(t, home, "")
	mustAcquire(t, b, semOnly("b", "deploy-b"))
	c := ensure(t, home, "")
	mustAcquire(t, c, semOnly("c", "deploy-c"))

	waitA := ensure(t, home, "")
	_, resultWaitA := acquireAsync(waitA, semOnly("wait-a", "deploy-a"))
	waitB := ensure(t, home, "")
	waitBPositions, waitBResult := acquireAsync(waitB, semOnly("wait-b", "deploy-b"))
	waitC := ensure(t, home, "")
	waitCPositions, waitCResult := acquireAsync(waitC, semOnly("wait-c", "deploy-c"))

	select {
	case q := <-waitBPositions:
		if q.Position != 1 {
			t.Fatalf("wait-b initial position = %d, want 1", q.Position)
		}
	case r := <-waitBResult:
		t.Fatalf("wait-b resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-b never reported its initial queue position")
	}
	select {
	case q := <-waitCPositions:
		if q.Position != 1 {
			t.Fatalf("wait-c initial position = %d, want 1", q.Position)
		}
	case r := <-waitCResult:
		t.Fatalf("wait-c resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-c never reported its initial queue position")
	}

	if err := holderA.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultWaitA, 2*time.Second)
	if r.err != nil {
		t.Fatalf("wait-a should have promoted, got %v", r.err)
	}

	select {
	case q := <-waitBPositions:
		if q.Position != 1 {
			t.Fatalf("wait-b refreshed position = %d, want 1", q.Position)
		}
	case r := <-waitBResult:
		t.Fatalf("wait-b resolved instead of remaining queued: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-b never received refreshed queue position")
	}
	select {
	case q := <-waitCPositions:
		if q.Position != 1 {
			t.Fatalf("wait-c refreshed position = %d, want 1", q.Position)
		}
	case r := <-waitCResult:
		t.Fatalf("wait-c resolved instead of remaining queued: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-c never received refreshed queue position")
	}
}

func TestQueuedSubmitReconnectReplacesStaleWaiter(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, semReq("holder", "shared-lock", 1, 1, wingwire.PolicyQueue))

	first := openRawQueuedAdmission(t, home, semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue))
	defer func() { _ = first.Close() }()

	qmsg := readRawMessage(t, first)
	q, ok := qmsg.(*wingwire.Queued)
	if !ok {
		t.Fatalf("first admission message = %T, want queued", qmsg)
	}
	if q.Position != 1 {
		t.Fatalf("initial position = %d, want 1", q.Position)
	}

	second := ensure(t, home, "")
	secondResult := make(chan acquireResult, 1)
	go func() {
		lease, err := second.Acquire(context.Background(), semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue), nil)
		secondResult <- acquireResult{lease: lease, err: err}
	}()

	select {
	case r := <-secondResult:
		t.Fatalf("replacement acquire resolved while holder still owns the semaphore: lease=%v err=%v", r.lease, r.err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	r := waitResult(t, secondResult, 2*time.Second)
	if r.err != nil {
		t.Fatalf("replacement acquire should be promoted after stale waiter closes, got %v", r.err)
	}
}

func TestQueuedSubmitReconnectRejectsMismatchedRequest(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, semReq("holder", "shared-lock", 1, 1, wingwire.PolicyQueue))

	first := openRawQueuedAdmission(t, home, semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue))
	defer func() { _ = first.Close() }()
	if msg := readRawMessage(t, first); msg == nil {
		t.Fatal("first admission returned no queue message")
	}

	second := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := second.Acquire(ctx, semReq("shard", "different-lock", 1, 1, wingwire.PolicyQueue), nil)
	if err == nil {
		t.Fatal("mismatched duplicate request admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("mismatched duplicate error = %q, want duplicate failure", got)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
}

func TestQueuedSubmitReconnectRejectsMismatchedCostSemantics(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, semReq("holder", "shared-lock", 1, 1, wingwire.PolicyQueue))

	measured := semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue)
	measured.Resources = wingwire.HostResources{Cores: 1}
	measured.CostSource = wingwire.CostSourceMeasured
	first := openRawQueuedAdmission(t, home, measured)
	defer func() { _ = first.Close() }()
	if msg := readRawMessage(t, first); msg == nil {
		t.Fatal("first admission returned no queue message")
	}

	pinned := measured
	pinned.CostSource = wingwire.CostSourcePin
	second := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := second.Acquire(ctx, pinned, nil)
	if err == nil {
		t.Fatal("mismatched cost semantics admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("mismatched cost semantics error = %q, want duplicate failure", got)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
}

func TestQueuedSubmitReconnectRejectsMismatchedSubLease(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, semReq("holder", "shared-lock", 1, 1, wingwire.PolicyQueue))

	firstReq := semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue)
	first := openRawQueuedAdmission(t, home, firstReq)
	defer func() { _ = first.Close() }()
	if msg := readRawMessage(t, first); msg == nil {
		t.Fatal("first admission returned no queue message")
	}

	mismatch := firstReq
	mismatch.SubLease = true
	second := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := second.Acquire(ctx, mismatch, nil)
	if err == nil {
		t.Fatal("mismatched sublease admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("mismatched sublease error = %q, want duplicate failure", got)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
}

func TestQueuedSubmitReconnectRejectsMismatchedDisplayMetadata(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, semReq("holder", "shared-lock", 1, 1, wingwire.PolicyQueue))

	measured := semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue)
	measured.Resources = wingwire.HostResources{Cores: 1}
	measured.CostSource = wingwire.CostSourceMeasured
	first := openRawQueuedAdmission(t, home, measured)
	defer func() { _ = first.Close() }()
	if msg := readRawMessage(t, first); msg == nil {
		t.Fatal("first admission returned no queue message")
	}

	displayMismatch := measured
	displayMismatch.CostSource = wingwire.CostSourceDefault
	second := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := second.Acquire(ctx, displayMismatch, nil)
	if err == nil {
		t.Fatal("mismatched display metadata admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("mismatched display metadata error = %q, want duplicate failure", got)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
}

func openRawQueuedAdmission(t *testing.T, home string, req wingwire.AdmissionRequest) net.Conn {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	if err := writeRawMessage(nc, &wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test"}); err != nil {
		_ = nc.Close()
		t.Fatalf("write hello: %v", err)
	}
	msg := readRawMessage(t, nc)
	if _, ok := msg.(*wingwire.HelloAck); !ok {
		_ = nc.Close()
		t.Fatalf("hello response = %T, want hello_ack", msg)
	}
	if err := writeRawMessage(nc, &req); err != nil {
		_ = nc.Close()
		t.Fatalf("write admission request: %v", err)
	}
	return nc
}

func writeRawMessage(nc net.Conn, msg wingwire.Message) error {
	line, err := wingwire.Encode(msg)
	if err != nil {
		return err
	}
	_, err = nc.Write(line)
	return err
}

func readRawMessage(t *testing.T, nc net.Conn) wingwire.Message {
	t.Helper()
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	sc := bufio.NewScanner(nc)
	if !sc.Scan() {
		t.Fatalf("read frame: %v", sc.Err())
	}
	msg, err := wingwire.Decode(sc.Bytes())
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return msg
}

func TestSemaphoresOnlyRunLeaseFinalizesOnDisconnect(t *testing.T) {
	home := shortHome(t)
	finalized := make(chan string, 1)
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeRun: func(runID string) {
			finalized <- runID
		},
	})

	cl := ensure(t, home, "")
	mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:          "run-semaphore",
		SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{
			Name: "deploy", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue,
		}},
	})
	cl.Close()

	select {
	case got := <-finalized:
		if got != "run-semaphore" {
			t.Fatalf("finalized %q, want run-semaphore", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("semaphores-only run lease was not finalized on disconnect")
	}
}

func TestSubLeaseDoesNotFinalizeOnDisconnect(t *testing.T) {
	home := shortHome(t)
	finalized := make(chan string, 1)
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeRun: func(runID string) {
			finalized <- runID
		},
	})

	cl := ensure(t, home, "")
	mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:     "parent/node",
		Resources: wingwire.HostResources{Cores: 1},
		SubLease:  true,
	})
	cl.Close()

	select {
	case got := <-finalized:
		t.Fatalf("sub-lease finalized %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunRegistrationFinalizesWhileNodeSubLeaseDoesNot(t *testing.T) {
	home := shortHome(t)
	finalized := make(chan string, 2)
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeRun: func(runID string) {
			finalized <- runID
		},
	})

	runClient := ensure(t, home, "")
	mustAcquire(t, runClient, wingwire.AdmissionRequest{
		RunID:          "run-unpinned",
		SemaphoresOnly: true,
	})
	nodeClient := ensure(t, home, "")
	mustAcquire(t, nodeClient, wingwire.AdmissionRequest{
		RunID:     "run-unpinned/node",
		Resources: wingwire.HostResources{Cores: 1},
		SubLease:  true,
	})

	nodeClient.Close()
	select {
	case got := <-finalized:
		t.Fatalf("node sub-lease finalized %q", got)
	case <-time.After(100 * time.Millisecond):
	}

	runClient.Close()
	select {
	case got := <-finalized:
		if got != "run-unpinned" {
			t.Fatalf("finalized %q, want run-unpinned", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run registration was not finalized on disconnect")
	}
}

func TestChildAttachReportsParentHostResources(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	hostParent := ensure(t, home, "")
	hostLease := mustAcquire(t, hostParent, wingwire.AdmissionRequest{
		RunID:     "host-parent",
		Resources: wingwire.HostResources{Cores: 1, MemoryBytes: 256 << 20},
	})
	hostChild := ensure(t, home, "")
	hostChildLease := mustAcquire(t, hostChild, wingwire.AdmissionRequest{
		RunID:            "host-child",
		ParentLeaseToken: hostLease.Token,
	})
	if hostChildLease.Resources.Cores != 1 || hostChildLease.Resources.MemoryBytes != 256<<20 {
		t.Fatalf("host child resources = %+v, want parent host resources", hostChildLease.Resources)
	}
	_ = hostChildLease.Release()
	_ = hostLease.Release()

	semParent := ensure(t, home, "")
	semLease := mustAcquire(t, semParent, wingwire.AdmissionRequest{
		RunID:          "sem-parent",
		SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{
			Name: "deploy", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue,
		}},
	})
	semChild := ensure(t, home, "")
	semChildLease := mustAcquire(t, semChild, wingwire.AdmissionRequest{
		RunID:            "sem-child",
		ParentLeaseToken: semLease.Token,
	})
	if semChildLease.Resources.Cores != 0 || semChildLease.Resources.MemoryBytes != 0 {
		t.Fatalf("semaphore child resources = %+v, want zero host resources", semChildLease.Resources)
	}
	_ = semChildLease.Release()
	_ = semLease.Release()
}

func TestMeasuredRequestAboveIdleGrantableCapacityIsAdmitted(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	cl := ensure(t, home, "")
	lease := mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:      "measured-heavy",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 10},
	})
	if lease.Resources.Cores != 6.4 {
		t.Fatalf("admitted cores = %v, want idle grantable ceiling 6.4", lease.Resources.Cores)
	}
}

func TestOversizedMeasuredCPURequestQueuesFollower(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30), HeadroomFraction: -1})

	holderClient := ensure(t, home, "")
	lease := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID:      "oversized",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 10},
	})
	if lease.Resources.Cores != 8 {
		t.Fatalf("oversized charge = %v, want full host charge 8", lease.Resources.Cores)
	}

	followerClient := ensure(t, home, "")
	positions, result := acquireAsync(followerClient, wingwire.AdmissionRequest{
		RunID:      "follower",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1},
	})
	select {
	case <-positions:
	case r := <-result:
		t.Fatalf("follower resolved without queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("follower neither queued nor resolved")
	}
}

// TestLivenessFloor_AdmitsSoleRunUnderExternalLoad drives the floor end to
// end: on an otherwise-idle box pinned under synthetic 100% external load the
// queue head still admits (charged the grantable budget, flagged sole-run),
// while a second arrival queues -- the box runs exactly one pipeline, never
// zero. It also composes the floor with the run-alone clamp: the head's cost
// is an oversized measured peak.
func TestLivenessFloor_AdmitsSoleRunUnderExternalLoad(t *testing.T) {
	home := shortHome(t)
	sampler := newFakeSampler(8, 16<<30)
	sampler.set(wingd.HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, FreeMemoryBytes: 16 << 30, LoadAverage: 100})
	startDaemon(t, wingd.Config{Home: home, Sampler: sampler})

	cl := ensure(t, home, "")
	lease := mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:      "sole",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 10},
	})
	if !lease.SoleRunUnderLoad {
		t.Fatal("sole run under external load was not flagged SoleRunUnderLoad")
	}
	if lease.Resources.Cores != 6.4 {
		t.Fatalf("sole run charge = %v, want grantable 6.4", lease.Resources.Cores)
	}

	second := ensure(t, home, "")
	positions, result := acquireAsync(second, wingwire.AdmissionRequest{
		RunID:      "second",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1},
	})
	select {
	case <-positions:
	case r := <-result:
		t.Fatalf("second run resolved without queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("second run neither queued nor resolved")
	}
}

func TestMeasuredCPUDeficitQueuesBehindExistingWork(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30), HeadroomFraction: -1})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID:      "holder",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 6, MemoryBytes: 2 << 30},
	})

	headClient := ensure(t, home, "")
	positions, result := acquireAsync(headClient, wingwire.AdmissionRequest{
		RunID:      "head",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 6, MemoryBytes: 2 << 30},
	})
	select {
	case <-positions:
	case r := <-result:
		t.Fatalf("head run resolved without queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("head run neither queued nor resolved")
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	r := waitResult(t, result, 2*time.Second)
	if r.err != nil {
		t.Fatalf("head did not admit after release: %v", r.err)
	}
	if r.lease.Resources.Cores != 6 {
		t.Fatalf("head cores = %v, want measured charge retained", r.lease.Resources.Cores)
	}
}

func TestPinnedRequestAboveCapacityFails(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	cl := ensure(t, home, "")
	_, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:      "pinned-heavy",
		CostSource: wingwire.CostSourcePin,
		Resources:  wingwire.HostResources{Cores: 10},
	}, nil)
	if err == nil {
		t.Fatal("oversized pin admitted, want never-admissible failure")
	}
}

func TestUnknownCostSourceFails(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	cl := ensure(t, home, "")
	_, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:      "unknown-source",
		CostSource: wingwire.CostSource("typo"),
		Resources:  wingwire.HostResources{Cores: 1},
	}, nil)
	if err == nil {
		t.Fatal("unknown cost source admitted, want invalid request failure")
	}
}

func TestResolvedCostSourcesAdmit(t *testing.T) {
	for _, source := range []wingwire.CostSource{
		wingwire.CostSourceMeasuring,
		wingwire.CostSourceFloor,
	} {
		t.Run(string(source), func(t *testing.T) {
			home := shortHome(t)
			startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

			cl := ensure(t, home, "")
			lease := mustAcquire(t, cl, wingwire.AdmissionRequest{
				RunID:      "resolved-" + string(source),
				CostSource: source,
				Resources:  wingwire.HostResources{Cores: 1, MemoryBytes: 1 << 30},
			})
			if lease.Resources.Cores != 1 {
				t.Fatalf("lease cores = %v, want resolved charge retained", lease.Resources.Cores)
			}
		})
	}
}

func TestUnknownCostSourceFailsOnChildAttach(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	parentClient := ensure(t, home, "")
	parent := mustAcquire(t, parentClient, wingwire.AdmissionRequest{
		RunID:     "parent",
		Resources: wingwire.HostResources{Cores: 1},
	})

	childClient := ensure(t, home, "")
	_, err := childClient.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:            "child",
		ParentLeaseToken: parent.Token,
		CostSource:       wingwire.CostSource("typo"),
	}, nil)
	if err == nil {
		t.Fatal("child attach with unknown cost source admitted, want invalid request failure")
	}
}

func TestExplicitRelease_Promotes(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "lock", 1, 1, wingwire.PolicyQueue))

	b := ensure(t, home, "")
	_, resultB := acquireAsync(b, semReq("b", "lock", 1, 1, wingwire.PolicyQueue))
	time.Sleep(100 * time.Millisecond)

	if err := holder.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have been promoted after release, got %v", r.err)
	}
}

// TestWaiterDisconnect_UnblocksProtectedFollower drives the weighted
// backfill guard end to end: a lighter run backfills past a queued heavy
// head, which protects the head from being starved, so a later waiter
// stays queued behind it. Disconnecting the heavy head lifts the
// protection and promotes the follower -- the snapshot-rebuild
// cancellation the daemon must get right when a queued waiter drops.
func TestWaiterDisconnect_UnblocksProtectedFollower(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
	})

	older := ensure(t, home, "")
	mustAcquire(t, older, semReq("older", "k", 10, 5, wingwire.PolicyQueue))

	heavy := ensure(t, home, "")
	heavyPos, _ := acquireAsync(heavy, semReq("heavy", "k", 10, 8, wingwire.PolicyQueue))
	waitForQueue(t, heavyPos)

	light1 := ensure(t, home, "")
	mustAcquire(t, light1, semReq("light-1", "k", 10, 5, wingwire.PolicyQueue))

	light2 := ensure(t, home, "")
	light2Pos, light2Result := acquireAsync(light2, semReq("light-2", "k", 10, 5, wingwire.PolicyQueue))
	waitForQueue(t, light2Pos)

	older.Close()
	select {
	case r := <-light2Result:
		t.Fatalf("light-2 jumped the protected heavy head: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}

	heavy.Close()
	r := waitResult(t, light2Result, 2*time.Second)
	if r.err != nil {
		t.Fatalf("light-2 should promote once the heavy head leaves, got %v", r.err)
	}
	if r.lease.RunID != "light-2" {
		t.Fatalf("promoted %q, want light-2", r.lease.RunID)
	}
}

func TestChildAttach_SharesLeaseWithoutDoubleCharge(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
	})

	parent := ensure(t, home, "")
	pl := mustAcquire(t, parent, coreReq("p", 2))

	child := ensure(t, home, "")
	cl, err := child.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:            "c",
		ParentLeaseToken: pl.Token,
	}, nil)
	if err != nil {
		t.Fatalf("child attach: %v", err)
	}
	if cl.Token != pl.Token {
		t.Fatalf("child token %q, want parent token %q", cl.Token, pl.Token)
	}

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	held := resourceHeld(qs, "cores")
	if held != 2 {
		t.Fatalf("cores held %v, want 2 (child must not double-charge)", held)
	}
}

func waitForQueue(t *testing.T, positions <-chan wingwire.Queued) {
	t.Helper()
	select {
	case <-positions:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never reported a queue position")
	}
}

func resourceHeld(qs wingwire.QueueState, key string) float64 {
	for _, r := range qs.Resources {
		if r.Key == key {
			return r.Held
		}
	}
	return -1
}
