package wingd_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
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
	if q.Key != "shared-lock" {
		t.Fatalf("queued key = %q, want shared-lock", q.Key)
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

func TestQueuedHostPressureDoesNotReportColdSemaphoreKey(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(1, 8<<30),
		HeadroomFraction: -1,
	})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, coreReq("host-holder", 1))
	defer func() { _ = holder.Release() }()

	waiterReq := semReq("host-waiter", "cold-lock", 1, 1, wingwire.PolicyQueue)
	waiterReq.Resources = wingwire.HostResources{Cores: 1}
	waiter := openRawQueuedAdmission(t, home, waiterReq)
	defer func() { _ = waiter.Close() }()
	msg := readRawMessage(t, waiter)
	q, ok := msg.(*wingwire.Queued)
	if !ok {
		t.Fatalf("waiter message = %T, want queued", msg)
	}
	if q.Key != "" {
		t.Fatalf("queued key = %q, want empty while only host capacity blocks", q.Key)
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

func TestQueuedSubmitReconnectAdoptsRemeasuredWaiter(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, semReq("holder", "shared-lock", 1, 1, wingwire.PolicyQueue))

	firstReq := semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue)
	firstReq.Resources = wingwire.HostResources{Cores: 1}
	firstReq.CostSource = wingwire.CostSourceDefault
	firstReq.ExpectedDurationMS = 1000
	first := openRawQueuedAdmission(t, home, firstReq)
	defer func() { _ = first.Close() }()
	if msg := readRawMessage(t, first); msg == nil {
		t.Fatal("first admission returned no queue message")
	}

	remeasured := firstReq
	remeasured.Resources = wingwire.HostResources{Cores: 2}
	remeasured.CostSource = wingwire.CostSourceMeasured
	remeasured.ExpectedDurationMS = 2000
	remeasured.ExpectedP99MS = 2500
	remeasured.SampleCount = 12
	second := ensure(t, home, "")
	positions := make(chan wingwire.Queued, 8)
	result := make(chan acquireResult, 1)
	go func() {
		lease, err := second.Acquire(context.Background(), remeasured, func(q wingwire.Queued) {
			select {
			case positions <- q:
			default:
			}
		})
		result <- acquireResult{lease: lease, err: err}
	}()
	waitForQueue(t, positions)

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	r := waitResult(t, result, 2*time.Second)
	if r.err != nil {
		t.Fatalf("remeasured waiter should be adopted, got %v", r.err)
	}
	if r.lease == nil {
		t.Fatal("remeasured waiter returned no lease")
	}
	if r.lease.Resources.Cores != 2 {
		t.Fatalf("remeasured waiter cores = %v, want 2", r.lease.Resources.Cores)
	}
	if err := r.lease.Release(); err != nil {
		t.Fatalf("release remeasured lease: %v", err)
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

func TestQueuedSubmitReconnectAdoptsDisplayMetadataChange(t *testing.T) {
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
	positions := make(chan wingwire.Queued, 8)
	result := make(chan acquireResult, 1)
	go func() {
		lease, err := second.Acquire(context.Background(), displayMismatch, func(q wingwire.Queued) {
			select {
			case positions <- q:
			default:
			}
		})
		result <- acquireResult{lease: lease, err: err}
	}()
	waitForQueue(t, positions)

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	r := waitResult(t, result, 2*time.Second)
	if r.err != nil {
		t.Fatalf("display metadata change should be adopted, got %v", r.err)
	}
	if r.lease == nil {
		t.Fatal("display metadata change returned no lease")
	}
	if err := r.lease.Release(); err != nil {
		t.Fatalf("release adopted lease: %v", err)
	}
}

func TestQueuedSubmitReconnectFailurePreservesOriginalWaiter(t *testing.T) {
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

	invalid := firstReq
	invalid.Resources = wingwire.HostResources{Cores: -1}
	second := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := second.Acquire(ctx, invalid, nil)
	if err == nil {
		t.Fatal("invalid reconnect admitted, want failure")
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	msg := readRawMessage(t, first)
	grant, ok := msg.(*wingwire.Grant)
	if !ok {
		t.Fatalf("original waiter message = %T, want grant", msg)
	}
	if grant.RunID != "shard" {
		t.Fatalf("grant run = %q, want shard", grant.RunID)
	}
}

func TestQueuedSubmitReconnectRejectsMismatchedPID(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, semReq("holder", "shared-lock", 1, 1, wingwire.PolicyQueue))

	firstReq := semReq("shard", "shared-lock", 1, 1, wingwire.PolicyQueue)
	firstReq.PID = 101
	first := openRawQueuedAdmission(t, home, firstReq)
	defer func() { _ = first.Close() }()
	if msg := readRawMessage(t, first); msg == nil {
		t.Fatal("first admission returned no queue message")
	}

	mismatch := firstReq
	mismatch.PID = 202
	second := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := second.Acquire(ctx, mismatch, nil)
	if err == nil {
		t.Fatal("mismatched pid admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("mismatched pid error = %q, want duplicate failure", got)
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

func TestExplicitCancelRetainsLegacyFinalizerCompatibility(t *testing.T) {
	home := shortHome(t)
	finalized := make(chan string, 1)
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeRun: func(runID string) {
			finalized <- runID
		},
	})

	holder := ensure(t, home, "")
	mustAcquire(t, holder, wingwire.AdmissionRequest{
		RunID: "legacy-cancel-finalizer", Resources: wingwire.HostResources{Cores: 1},
	})
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "legacy-cancel-finalizer")
	if err != nil {
		t.Fatalf("cancel lease: %v", err)
	}
	if !found {
		t.Fatal("cancel did not find holder")
	}
	_ = holder.Close()

	select {
	case got := <-finalized:
		if got != "legacy-cancel-finalizer" {
			t.Fatalf("finalized %q, want legacy-cancel-finalizer", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit cancellation suppressed the configured legacy finalizer")
	}
}

func TestExplicitCancelPersistenceFailureIsNotAcknowledged(t *testing.T) {
	home := shortHome(t)
	legacyFinalized := make(chan string, 1)
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeRun: func(runID string) {
			legacyFinalized <- runID
		},
		FinalizeCancelledRuns: func([]string, string) error {
			return errors.New("store unavailable")
		},
	})

	holder := ensure(t, home, "")
	mustAcquire(t, holder, wingwire.AdmissionRequest{
		RunID: "cancel-finalize-failure", Resources: wingwire.HostResources{Cores: 1},
	})
	control := ensure(t, home, "")
	cancelCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if found, err := control.CancelLease(cancelCtx, "cancel-finalize-failure"); err == nil {
		t.Fatalf("CancelLease = (found=%v, nil), want persistence failure", found)
	}
	_ = holder.Close()

	select {
	case got := <-legacyFinalized:
		if got != "cancel-finalize-failure" {
			t.Fatalf("fallback finalized %q, want cancel-finalize-failure", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed explicit persistence suppressed disconnect finalization")
	}
}

func TestExplicitCancelStateWriteFailureStillSignalsOwnerWithoutAcknowledging(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:                  home,
		FinalizeCancelledRuns: func([]string, string) error { return nil },
	})
	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("cancel-state-write-failure", 1))
	cancelled := make(chan wingwire.Cancel, 1)
	go lease.WatchControl(nil, func(c wingwire.Cancel) { cancelled <- c })
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	control, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := writeRawMessage(control, &wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readRawMessage(t, control).(*wingwire.HelloAck); !ok {
		t.Fatal("control handshake failed")
	}
	moved := home + ".moved"
	if err := os.Rename(home, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.WriteFile(home, []byte("blocks state directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeRawMessage(control, &wingwire.CancelLease{RunID: "cancel-state-write-failure"}); err != nil {
		t.Fatal(err)
	}
	_ = control.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := control.Read(buf); err == nil {
		t.Fatal("cancel was acknowledged despite state persistence failure")
	}
	select {
	case got := <-cancelled:
		if got.RunID != "cancel-state-write-failure" {
			t.Fatalf("cancel run = %q", got.RunID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("state persistence failure left cancelled execution running")
	}
}

func TestCancelledLeaseCannotReattachAfterStateWriteFailureAndRestart(t *testing.T) {
	home := shortHome(t)
	cfg := wingd.Config{
		Home:                  home,
		FinalizeCancelledRuns: func([]string, string) error { return nil },
	}
	first := startDaemon(t, cfg)
	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("cancelled-token-after-restart", 1))
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	control, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRawMessage(control, &wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readRawMessage(t, control).(*wingwire.HelloAck); !ok {
		t.Fatal("control handshake failed")
	}
	moved := home + ".moved"
	if err := os.Rename(home, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home, []byte("blocks state directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeRawMessage(control, &wingwire.CancelLease{RunID: "cancelled-token-after-restart"}); err != nil {
		t.Fatal(err)
	}
	_ = control.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = control.Read(make([]byte, 1))
	_ = control.Close()
	first.stop()
	if err := first.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("first daemon exit: %v", err)
	}
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, home); err != nil {
		t.Fatal(err)
	}
	cfg.IsRunTerminal = func(runID string) (bool, error) {
		return runID == "cancelled-token-after-restart", nil
	}
	startDaemon(t, cfg)
	reconnector := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if reclaimed, err := reconnector.Reattach(ctx, lease.Token); err == nil {
		_ = reclaimed.Release()
		t.Fatal("restart reattached a lease whose run is durably cancelled")
	}
}

func TestExplicitCancelFinalizerDoesNotHoldDaemonMutex(t *testing.T) {
	home := shortHome(t)
	var query *client.Client
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeCancelledRuns: func([]string, string) error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := query.QueueState(ctx)
			return err
		},
	})
	holder := ensure(t, home, "")
	mustAcquire(t, holder, coreReq("cancel-lock-liveness", 1))
	query = ensure(t, home, "")
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "cancel-lock-liveness")
	if err != nil || !found {
		t.Fatalf("CancelLease = (%v, %v), want found", found, err)
	}
}

func TestExplicitCancelSignalsConnectionThatReattachedDuringPersistence(t *testing.T) {
	home := shortHome(t)
	entered := make(chan struct{})
	resume := make(chan struct{})
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeCancelledRuns: func([]string, string) error {
			close(entered)
			<-resume
			return nil
		},
	})
	req := coreReq("cancel-current-connection", 1)
	old := ensure(t, home, "")
	mustAcquire(t, old, req)
	control := ensure(t, home, "")
	result := make(chan error, 1)
	go func() {
		found, err := control.CancelLease(context.Background(), req.RunID)
		if err == nil && !found {
			err = errors.New("cancel did not find run")
		}
		result <- err
	}()
	<-entered
	current := ensure(t, home, "")
	lease, err := current.Acquire(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("reattach current connection: %v", err)
	}
	cancelled := make(chan wingwire.Cancel, 1)
	go lease.WatchControl(nil, func(c wingwire.Cancel) { cancelled <- c })
	close(resume)
	if err := <-result; err != nil {
		t.Fatalf("CancelLease: %v", err)
	}
	select {
	case got := <-cancelled:
		if got.RunID != req.RunID {
			t.Fatalf("cancel run = %q", got.RunID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement connection did not receive cancellation")
	}
}

func TestExplicitCancelRejectsReplacementAfterResolution(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeCancelledRuns: func([]string, string) error {
			return nil
		},
	})
	req := coreReq("cancel-no-replacement", 1)
	holder := ensure(t, home, "")
	mustAcquire(t, holder, req)
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), req.RunID)
	if err != nil || !found {
		t.Fatalf("CancelLease = (%v, %v), want found", found, err)
	}

	replacement := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if lease, err := replacement.Acquire(ctx, req, nil); err == nil {
		_ = lease.Release()
		t.Fatal("replacement reacquired a durably cancelled run")
	}
}

func TestChildAttachRejectsLeaseWhileCancellationPersistenceIsPending(t *testing.T) {
	home := shortHome(t)
	entered := make(chan struct{})
	resume := make(chan struct{})
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeCancelledRuns: func([]string, string) error {
			close(entered)
			<-resume
			return nil
		},
	})
	parent := ensure(t, home, "")
	parentLease := mustAcquire(t, parent, coreReq("cancel-parent-pending", 1))
	control := ensure(t, home, "")
	done := make(chan error, 1)
	go func() {
		_, err := control.CancelLease(context.Background(), "cancel-parent-pending")
		done <- err
	}()
	<-entered

	attacher := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if lease, err := attacher.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: "late-child", ParentLeaseToken: parentLease.Token,
	}, nil); err == nil {
		_ = lease.Release()
		t.Fatal("child attached to a lease whose member cancellation was pending")
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatalf("CancelLease: %v", err)
	}
}

func TestChildAttachRejectsPreviouslyCancelledRunID(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:                  home,
		FinalizeCancelledRuns: func([]string, string) error { return nil },
	})
	cancelled := ensure(t, home, "")
	mustAcquire(t, cancelled, coreReq("cancelled-child-id", 1))
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "cancelled-child-id")
	if err != nil || !found {
		t.Fatalf("CancelLease = (%v, %v), want found", found, err)
	}

	parent := ensure(t, home, "")
	parentLease := mustAcquire(t, parent, coreReq("unrelated-live-parent", 1))
	attacher := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if lease, err := attacher.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: "cancelled-child-id", ParentLeaseToken: parentLease.Token,
	}, nil); err == nil {
		_ = lease.Release()
		t.Fatal("cancelled run ID resurrected as a child lease member")
	}
}

func TestExplicitCancelRejectsTopLevelResurrectionAfterRestart(t *testing.T) {
	home := shortHome(t)
	cfg := wingd.Config{Home: home, FinalizeCancelledRuns: func([]string, string) error { return nil }}
	first := startDaemon(t, cfg)
	holder := ensure(t, home, "")
	mustAcquire(t, holder, coreReq("cancelled-before-restart", 1))
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "cancelled-before-restart")
	if err != nil || !found {
		t.Fatalf("CancelLease = (%v, %v), want found", found, err)
	}
	first.stop()
	if err := first.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("first daemon exit: %v", err)
	}
	startDaemon(t, cfg)

	replacement := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if lease, err := replacement.Acquire(ctx, coreReq("cancelled-before-restart", 1), nil); err == nil {
		_ = lease.Release()
		t.Fatal("restart resurrected a cancelled top-level run")
	}
}

func TestExplicitCancelRejectsChildResurrectionAfterRestart(t *testing.T) {
	home := shortHome(t)
	cfg := wingd.Config{Home: home, FinalizeCancelledRuns: func([]string, string) error { return nil }}
	first := startDaemon(t, cfg)
	holder := ensure(t, home, "")
	mustAcquire(t, holder, coreReq("cancelled-child-before-restart", 1))
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "cancelled-child-before-restart")
	if err != nil || !found {
		t.Fatalf("CancelLease = (%v, %v), want found", found, err)
	}
	first.stop()
	if err := first.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("first daemon exit: %v", err)
	}
	startDaemon(t, cfg)

	parent := ensure(t, home, "")
	parentLease := mustAcquire(t, parent, coreReq("parent-after-restart", 1))
	child := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if lease, err := child.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: "cancelled-child-before-restart", ParentLeaseToken: parentLease.Token,
	}, nil); err == nil {
		_ = lease.Release()
		t.Fatal("restart resurrected a cancelled run as a child")
	}
}

func TestConcurrentReattachClaimsRestoredLeaseOnlyOnce(t *testing.T) {
	home := shortHome(t)
	first := startDaemon(t, wingd.Config{Home: home})
	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("concurrent-reattach", 1))
	first.stop()
	if err := first.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("first daemon exit: %v", err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	startDaemon(t, wingd.Config{
		Home: home,
		IsRunTerminal: func(string) (bool, error) {
			entered <- struct{}{}
			<-release
			return false, nil
		},
	})
	results := make(chan error, 2)
	clients := []*client.Client{ensure(t, home, ""), ensure(t, home, "")}
	for _, cl := range clients {
		go func(cl *client.Client) {
			_, err := cl.Reattach(context.Background(), lease.Token)
			results <- err
		}(cl)
	}
	<-entered
	<-entered
	close(release)
	succeeded := 0
	for range 2 {
		if <-results == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful reattach claims = %d, want 1", succeeded)
	}
}

func TestExplicitCancelFinalizesEverySharedLeaseMemberInOneBatch(t *testing.T) {
	home := shortHome(t)
	finalized := make(chan []string, 1)
	startDaemon(t, wingd.Config{
		Home: home,
		FinalizeCancelledRuns: func(runIDs []string, _ string) error {
			finalized <- append([]string(nil), runIDs...)
			return nil
		},
	})
	parent := ensure(t, home, "")
	parentLease := mustAcquire(t, parent, coreReq("shared-parent", 1))
	child := ensure(t, home, "")
	childLease := mustAcquire(t, child, wingwire.AdmissionRequest{RunID: "shared-child", ParentLeaseToken: parentLease.Token})
	parentCancelled := make(chan wingwire.Cancel, 1)
	childCancelled := make(chan wingwire.Cancel, 1)
	go parentLease.WatchControl(nil, func(c wingwire.Cancel) { parentCancelled <- c })
	go childLease.WatchControl(nil, func(c wingwire.Cancel) { childCancelled <- c })
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "shared-parent")
	if err != nil || !found {
		t.Fatalf("CancelLease = (%v, %v), want found", found, err)
	}
	select {
	case got := <-finalized:
		slices.Sort(got)
		if !slices.Equal(got, []string{"shared-child", "shared-parent"}) {
			t.Fatalf("batch = %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared lease was not finalized")
	}
	for name, ch := range map[string]<-chan wingwire.Cancel{"shared-parent": parentCancelled, "shared-child": childCancelled} {
		select {
		case got := <-ch:
			if got.RunID != name {
				t.Fatalf("%s connection got cancel for %q", name, got.RunID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s connection was not signalled", name)
		}
	}
	qs, err := control.QueueState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(qs.Holders) != 0 {
		t.Fatalf("holders after acknowledged cancel = %+v", qs.Holders)
	}
}

func TestSharedCancelPersistenceFailureFallsBackWithoutSuppression(t *testing.T) {
	home := shortHome(t)
	legacy := make(chan string, 2)
	startDaemon(t, wingd.Config{
		Home:        home,
		FinalizeRun: func(runID string) { legacy <- runID },
		FinalizeCancelledRuns: func(runIDs []string, _ string) error {
			if len(runIDs) != 2 {
				return fmt.Errorf("batch members = %v", runIDs)
			}
			return errors.New("second member failed")
		},
	})
	parent := ensure(t, home, "")
	parentLease := mustAcquire(t, parent, coreReq("failed-parent", 1))
	child := ensure(t, home, "")
	mustAcquire(t, child, wingwire.AdmissionRequest{RunID: "failed-child", ParentLeaseToken: parentLease.Token})
	control := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if found, err := control.CancelLease(ctx, "failed-parent"); err == nil {
		t.Fatalf("CancelLease = (%v, nil), want failure", found)
	}
	_ = parent.Close()
	_ = child.Close()
	got := []string{<-legacy, <-legacy}
	slices.Sort(got)
	if !slices.Equal(got, []string{"failed-child", "failed-parent"}) {
		t.Fatalf("legacy finalizations = %v", got)
	}
}

func TestDisconnectDuringFailedCancelPersistenceGetsOrphanFallback(t *testing.T) {
	home := shortHome(t)
	entered := make(chan struct{})
	resume := make(chan struct{})
	legacy := make(chan string, 1)
	startDaemon(t, wingd.Config{
		Home:        home,
		FinalizeRun: func(runID string) { legacy <- runID },
		FinalizeCancelledRuns: func([]string, string) error {
			close(entered)
			<-resume
			return errors.New("store unavailable")
		},
	})
	holder := ensure(t, home, "")
	mustAcquire(t, holder, coreReq("disconnect-during-persist", 1))
	control := ensure(t, home, "")
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go func() { _, err := control.CancelLease(ctx, "disconnect-during-persist"); done <- err }()
	<-entered
	_ = holder.Close()
	close(resume)
	<-done // the first exchange is closed without an acknowledgement; the client may reconnect and observe not-found.
	select {
	case got := <-legacy:
		if got != "disconnect-during-persist" {
			t.Fatalf("legacy finalized %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending disconnect stayed suppressed after persistence failed")
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
	sampler.set(wingd.HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, FreeMemoryBytes: 16 << 30, LoadAverage: 100, BusyCores: 100, LoadMeasured: true, CPUMeasured: true, MemoryMeasured: true})
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

// TestLivenessFloor_ZeroCostConnectionsDoNotSuppressFIFOHead reproduces a
// multi-run stall. Run registrations keep several live daemon
// connections and zero-cost leases while their node-level resource requests
// wait behind one real grant. When that grant releases under total external
// pressure, exactly the FIFO head must bootstrap; the remaining nodes stay
// queued behind its positive host charge.
func TestLivenessFloor_ZeroCostConnectionsDoNotSuppressFIFOHead(t *testing.T) {
	home := shortHome(t)
	sampler := newFakeSampler(8, 16<<30)
	sampler.set(wingd.HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  1 << 30,
		LoadAverage:      100,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	})
	startDaemon(t, wingd.Config{Home: home, Sampler: sampler})

	for i := 1; i <= 3; i++ {
		registration := ensure(t, home, "")
		mustAcquire(t, registration, wingwire.AdmissionRequest{
			RunID:          fmt.Sprintf("run-%d", i),
			SemaphoresOnly: true,
		})
	}

	blockerClient := ensure(t, home, "")
	blocker := mustAcquire(t, blockerClient, wingwire.AdmissionRequest{
		RunID:      "real-holder",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1, MemoryBytes: 1 << 30},
	})

	results := make([]<-chan acquireResult, 3)
	for i := range results {
		cl := ensure(t, home, "")
		positions, result := acquireAsync(cl, wingwire.AdmissionRequest{
			RunID:        fmt.Sprintf("run-%d/node-host/bm9kZQ", i+1),
			OwnerRunID:   fmt.Sprintf("run-%d", i+1),
			DisplayRunID: fmt.Sprintf("run-%d/node", i+1),
			SubLease:     true,
			CostSource:   wingwire.CostSourceMeasured,
			Resources:    wingwire.HostResources{Cores: 4, MemoryBytes: 4 << 30},
		})
		select {
		case q := <-positions:
			if q.Position != i+1 {
				t.Fatalf("node %d position = %d, want FIFO position %d", i+1, q.Position, i+1)
			}
		case r := <-result:
			t.Fatalf("node %d resolved before blocker release: lease=%v err=%v", i+1, r.lease, r.err)
		case <-time.After(2 * time.Second):
			t.Fatalf("node %d neither queued nor resolved", i+1)
		}
		results[i] = result
	}

	if err := blocker.Release(); err != nil {
		t.Fatalf("release blocker: %v", err)
	}
	first := waitResult(t, results[0], 2*time.Second)
	if first.err != nil || first.lease == nil {
		t.Fatalf("FIFO head did not bootstrap: lease=%v err=%v", first.lease, first.err)
	}
	for i := 1; i < len(results); i++ {
		select {
		case r := <-results[i]:
			t.Fatalf("node %d also resolved while head holds resources: lease=%v err=%v", i+1, r.lease, r.err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	connected, holding := 0, 0
	for _, h := range qs.Holders {
		if h.ConnectionOnly {
			connected++
		} else {
			holding++
		}
	}
	if connected != 3 || holding != 1 || len(qs.Waiters) != 2 {
		t.Fatalf("queue after bootstrap = %d connected, %d holding, %d waiting; want 3, 1, 2", connected, holding, len(qs.Waiters))
	}
}

func TestOwnerRunAdmissionOrderPromotesOlderOwnerDescendant(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30)})

	olderOwnerClient := ensure(t, home, "")
	olderOwner := mustAcquire(t, olderOwnerClient, wingwire.AdmissionRequest{
		RunID: "owner-older", SemaphoresOnly: true,
	})
	newerOwnerClient := ensure(t, home, "")
	newerOwner := mustAcquire(t, newerOwnerClient, wingwire.AdmissionRequest{
		RunID: "owner-newer", SemaphoresOnly: true,
	})
	blockerClient := ensure(t, home, "")
	blocker := mustAcquire(t, blockerClient, wingwire.AdmissionRequest{
		RunID: "blocker", Resources: wingwire.HostResources{Cores: 4},
	})

	newerChildClient := ensure(t, home, "")
	newerPositions, newerResult := acquireAsync(newerChildClient, wingwire.AdmissionRequest{
		RunID: "newer-child", OwnerRunID: "owner-newer", OwnerLeaseToken: newerOwner.Token, SubLease: true,
		Resources: wingwire.HostResources{Cores: 4},
	})
	select {
	case <-newerPositions:
	case <-time.After(2 * time.Second):
		t.Fatal("newer child did not queue")
	}

	olderChildClient := ensure(t, home, "")
	olderPositions, olderResult := acquireAsync(olderChildClient, wingwire.AdmissionRequest{
		RunID: "older-child", OwnerRunID: "owner-older", OwnerLeaseToken: olderOwner.Token, SubLease: true,
		Resources: wingwire.HostResources{Cores: 4},
	})
	select {
	case q := <-olderPositions:
		if q.Position != 1 {
			t.Fatalf("older child position = %d, want 1 ahead of newer owner's child", q.Position)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("older child did not queue")
	}

	if err := blocker.Release(); err != nil {
		t.Fatalf("release blocker: %v", err)
	}
	first := waitResult(t, olderResult, 2*time.Second)
	if first.err != nil || first.lease == nil {
		t.Fatalf("older owner's child was not promoted: lease=%v err=%v", first.lease, first.err)
	}
	select {
	case r := <-newerResult:
		t.Fatalf("newer owner's child resolved first: lease=%v err=%v", r.lease, r.err)
	case <-time.After(100 * time.Millisecond):
	}

	_ = olderOwner.Release()
	_ = newerOwner.Release()
}

func TestOwnerRunAdmissionAuthorityRejectsRankTheft(t *testing.T) {
	tests := []struct {
		name       string
		ownerSetup func(t *testing.T, home string) (claimedOwner, proofToken string)
	}{
		{
			name: "wrong top-level owner",
			ownerSetup: func(t *testing.T, home string) (string, string) {
				claimed := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "claimed-owner", SemaphoresOnly: true,
				})
				actual := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "actual-owner", SemaphoresOnly: true,
				})
				t.Cleanup(func() { _ = claimed.Release(); _ = actual.Release() })
				return "claimed-owner", actual.Token
			},
		},
		{
			name: "non-top-level participant",
			ownerSetup: func(t *testing.T, home string) (string, string) {
				owner := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "top-owner", SemaphoresOnly: true,
				})
				participant := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "internal-participant", OwnerRunID: "top-owner", OwnerLeaseToken: owner.Token,
					SemaphoresOnly: true, SubLease: true,
				})
				t.Cleanup(func() { _ = participant.Release(); _ = owner.Release() })
				return "internal-participant", participant.Token
			},
		},
		{
			name: "non-live owner",
			ownerSetup: func(t *testing.T, home string) (string, string) {
				owner := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "released-owner", SemaphoresOnly: true,
				})
				token := owner.Token
				if err := owner.Release(); err != nil {
					t.Fatalf("release owner: %v", err)
				}
				return "released-owner", token
			},
		},
		{
			name: "attached child is not the canonical owner",
			ownerSetup: func(t *testing.T, home string) (string, string) {
				parent := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "canonical-parent", SemaphoresOnly: true,
				})
				child := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "attached-child", ParentLeaseToken: parent.Token,
				})
				t.Cleanup(func() { _ = child.Release(); _ = parent.Release() })
				return "attached-child", parent.Token
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := shortHome(t)
			startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30)})
			claimedOwner, proofToken := tt.ownerSetup(t, home)
			blocker := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
				RunID: "blocker", Resources: wingwire.HostResources{Cores: 4},
			})
			t.Cleanup(func() { _ = blocker.Release() })

			baselinePositions, _ := acquireAsync(ensure(t, home, ""), wingwire.AdmissionRequest{
				RunID: "baseline", Resources: wingwire.HostResources{Cores: 4},
			})
			select {
			case <-baselinePositions:
			case <-time.After(2 * time.Second):
				t.Fatal("baseline did not queue")
			}
			forgedPositions, _ := acquireAsync(ensure(t, home, ""), wingwire.AdmissionRequest{
				RunID: "forged", OwnerRunID: claimedOwner, OwnerLeaseToken: proofToken, SubLease: true,
				Resources: wingwire.HostResources{Cores: 4},
			})
			select {
			case q := <-forgedPositions:
				if q.Position != 2 {
					t.Fatalf("forged position = %d, want 2 behind earlier fallback participant", q.Position)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("forged request did not queue")
			}
		})
	}
}

func TestMeasuredCPUDeficitAdmitsOneAdditionalMemoryFittingRun(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30), HeadroomFraction: -1})

	holderClient := ensure(t, home, "")
	mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID:      "holder",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 6, MemoryBytes: 2 << 30},
	})

	headClient := ensure(t, home, "")
	lease := mustAcquire(t, headClient, wingwire.AdmissionRequest{
		RunID:      "head",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 6, MemoryBytes: 2 << 30},
	})
	if lease.Resources.Cores != 6 {
		t.Fatalf("head cores = %v, want measured charge retained", lease.Resources.Cores)
	}

	nextClient := ensure(t, home, "")
	positions, result := acquireAsync(nextClient, wingwire.AdmissionRequest{
		RunID:      "next",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1, MemoryBytes: 2 << 30},
	})
	select {
	case <-positions:
	case r := <-result:
		t.Fatalf("next run resolved without queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("next run neither queued nor resolved")
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

func TestAutoMeasuredCostSourcesAdmitOnIdleBox(t *testing.T) {
	for _, source := range []wingwire.CostSource{wingwire.CostSourceMeasuring, wingwire.CostSourceFloor} {
		t.Run(string(source), func(t *testing.T) {
			home := shortHome(t)
			startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

			cl := ensure(t, home, "")
			lease, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
				RunID:      "auto-measured",
				CostSource: source,
				Resources:  wingwire.HostResources{Cores: 1.5},
			}, nil)
			if err != nil {
				t.Fatalf("auto-measured %s request rejected on an idle box: %v", source, err)
			}
			if lease == nil {
				t.Fatalf("auto-measured %s request returned no lease", source)
			}
		})
	}
}

func TestUnknownCostSourceNamesTheOffendingInput(t *testing.T) {
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
	var ae *client.AdmissionError
	if !errors.As(err, &ae) {
		t.Fatalf("want *client.AdmissionError, got %T: %v", err, err)
	}
	if !strings.Contains(ae.Reason, "typo") {
		t.Fatalf("rejection reason %q does not name the offending cost source %q", ae.Reason, "typo")
	}
}

func TestRepeatedInvalidRequestsSurfaceInQueueStateWindow(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	for i := 0; i < 3; i++ {
		cl := ensure(t, home, "")
		_, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
			RunID:      "bad-" + strconv.Itoa(i),
			CostSource: wingwire.CostSource("typo"),
			Resources:  wingwire.HostResources{Cores: 1},
		}, nil)
		if err == nil {
			t.Fatal("invalid request admitted")
		}
	}

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if qs.Events == nil {
		t.Fatal("events window nil after rejections")
	}
	var count int
	for _, r := range qs.Events.Rejections {
		if r.Cause == "cost_source" {
			count = r.Count
		}
	}
	if count != 3 {
		t.Fatalf("cost_source rejection count = %d, want 3 (window: %+v)", count, qs.Events.Rejections)
	}
}

func TestExplicitRelease_Promotes(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "lock", 1, 1, wingwire.PolicyQueue))

	b := ensure(t, home, "")
	positions, resultB := acquireAsync(b, semReq("b", "lock", 1, 1, wingwire.PolicyQueue))
	waitForQueue(t, positions)
	started := time.Now()
	time.Sleep(100 * time.Millisecond)

	if err := holder.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have been promoted after release, got %v", r.err)
	}
	if elapsed := time.Since(started); elapsed >= 75*time.Millisecond {
		t.Fatalf("release-to-promotion took %v, want less than 75ms", elapsed)
	}
}

func TestGrantedSubmitReconnectReclaimsLiveGrant(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	first := ensure(t, home, "")
	lease := mustAcquire(t, first, semReq("shard", "lock", 1, 1, wingwire.PolicyQueue))

	waiter := ensure(t, home, "")
	waiterPos, waiterResult := acquireAsync(waiter, semReq("waiter", "lock", 1, 1, wingwire.PolicyQueue))
	waitForQueue(t, waiterPos)

	replacement := ensure(t, home, "")
	reclaimed, err := replacement.Acquire(context.Background(), semReq("shard", "lock", 1, 1, wingwire.PolicyQueue), nil)
	if err != nil {
		t.Fatalf("replacement acquire: %v", err)
	}
	if reclaimed.Token != lease.Token {
		t.Fatalf("replacement token = %q, want original token %q", reclaimed.Token, lease.Token)
	}

	if err := reclaimed.Release(); err != nil {
		t.Fatalf("replacement release: %v", err)
	}
	r := waitResult(t, waiterResult, 2*time.Second)
	if r.err != nil {
		t.Fatalf("waiter should promote after replacement release, got %v", r.err)
	}
}

func TestGrantedSubmitReconnectRejectsMismatchedRequest(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	first := ensure(t, home, "")
	lease := mustAcquire(t, first, semReq("shard", "lock", 1, 1, wingwire.PolicyQueue))
	t.Cleanup(func() { _ = lease.Release() })

	replacement := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := replacement.Acquire(ctx, semReq("shard", "different-lock", 1, 1, wingwire.PolicyQueue), nil)
	if err == nil {
		t.Fatal("mismatched granted duplicate admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("mismatched granted duplicate error = %q, want duplicate failure", got)
	}
}

func TestGrantedSubmitReconnectRejectsRequestedChargeChange(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(2, 8<<30),
		HeadroomFraction: -1,
	})

	req := coreReq("oversized", 10)
	req.CostSource = wingwire.CostSourceMeasured
	first := ensure(t, home, "")
	lease := mustAcquire(t, first, req)

	replacement := ensure(t, home, "")
	reclaimed, err := replacement.Acquire(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("replacement acquire: %v", err)
	}
	if reclaimed.Token != lease.Token {
		t.Fatalf("replacement token = %q, want original token %q", reclaimed.Token, lease.Token)
	}

	mismatch := req
	mismatch.Resources.Cores = 9
	third := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = third.Acquire(ctx, mismatch, nil)
	if err == nil {
		t.Fatal("changed raw request reclaimed a granted lease, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("changed raw request error = %q, want duplicate failure", got)
	}

	if err := reclaimed.Release(); err != nil {
		t.Fatalf("replacement release: %v", err)
	}
}

func TestGrantedSubmitReconnectRejectsSemaphoresOnlyMismatch(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	req := semReq("shard", "lock", 1, 1, wingwire.PolicyQueue)
	req.SemaphoresOnly = true
	first := ensure(t, home, "")
	lease := mustAcquire(t, first, req)
	t.Cleanup(func() { _ = lease.Release() })

	mismatch := req
	mismatch.SemaphoresOnly = false
	replacement := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := replacement.Acquire(ctx, mismatch, nil)
	if err == nil {
		t.Fatal("semaphores-only mismatch admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("semaphores-only mismatch error = %q, want duplicate failure", got)
	}
}

func TestGrantedSubmitReconnectRejectsRestoredMultiMemberLease(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home, GraceWindow: 2 * time.Second})

	parentReq := semReq("parent", "lock", 1, 1, wingwire.PolicyQueue)
	parentClient := ensure(t, home, "")
	parentLease := mustAcquire(t, parentClient, parentReq)

	childClient := ensure(t, home, "")
	childLease := mustAcquire(t, childClient, wingwire.AdmissionRequest{
		RunID:            "child",
		ParentLeaseToken: parentLease.Token,
	})
	if childLease.Token != parentLease.Token {
		t.Fatalf("child token = %q, want parent token %q", childLease.Token, parentLease.Token)
	}

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}

	startDaemon(t, wingd.Config{Home: home, GraceWindow: 2 * time.Second})

	reattachedClient := ensure(t, home, "")
	reattached, err := reattachedClient.Reattach(context.Background(), parentLease.Token)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if reattached.RunID != "parent" {
		t.Fatalf("reattached run id = %q, want parent", reattached.RunID)
	}

	replacementClient := ensure(t, home, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = replacementClient.Acquire(ctx, parentReq, nil)
	if err == nil {
		t.Fatal("multi-member submit reconnect admitted, want duplicate failure")
	}
	if got := err.Error(); got != `wingd: fail on "duplicate"` {
		t.Fatalf("multi-member submit reconnect error = %q, want duplicate failure", got)
	}
	if err := reattached.Release(); err != nil {
		t.Fatalf("reattached release: %v", err)
	}

	waiterClient := ensure(t, home, "")
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	waiter, err := waiterClient.Acquire(ctx, semReq("waiter", "lock", 1, 1, wingwire.PolicyQueue), nil)
	if err != nil {
		t.Fatalf("waiter acquire after reattached release: %v", err)
	}
	if err := waiter.Release(); err != nil {
		t.Fatalf("waiter release: %v", err)
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

// The reconnect must retain enough reservation to admit the older waiter as
// soon as memory opens while leaving genuinely spare cores usable.
func TestBackfillStream_ReconnectedOlderWaiterUsesOnlyReservedSpareCapacity(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(8, 8<<30),
		HeadroomFraction: -1,
	})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID:     "older-holder",
		Resources: wingwire.HostResources{Cores: 4, MemoryBytes: 4 << 30},
	})

	olderReq := wingwire.AdmissionRequest{
		RunID:     "older-small",
		Resources: wingwire.HostResources{Cores: 1, MemoryBytes: 6 << 30},
	}
	firstOlderConn := openRawQueuedAdmission(t, home, olderReq)
	defer func() { _ = firstOlderConn.Close() }()
	if msg := readRawMessage(t, firstOlderConn); msg == nil {
		t.Fatal("older waiter returned no queue message")
	}

	firstNewerClient := ensure(t, home, "")
	firstNewer := mustAcquire(t, firstNewerClient, coreReq("newer-high-core-0", 4))

	reconnectedClient := ensure(t, home, "")
	olderPositions, olderResult := acquireAsync(reconnectedClient, olderReq)
	waitForQueue(t, olderPositions)

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query after reconnect: %v", err)
	}
	olderWaiter, ok := waiterByRun(qs, "older-small")
	if !ok || olderWaiter.BackfillCount != 1 {
		t.Fatalf("reconnected older waiter = %+v, present=%v; want retained backfill count 1", olderWaiter, ok)
	}
	if qs.Events == nil || qs.Events.Backfills != 1 || qs.Events.BackfillProtections != 1 {
		t.Fatalf("events after bypass = %+v, want one durable backfill/protection transition", qs.Events)
	}

	const streamSize = 6
	newerResults := make([]<-chan acquireResult, 0, streamSize)
	for i := 1; i <= streamSize; i++ {
		cl := ensure(t, home, "")
		positions, result := acquireAsync(cl, coreReq("newer-high-core-"+strconv.Itoa(i), 4))
		waitForQueue(t, positions)
		newerResults = append(newerResults, result)
	}

	if err := firstNewer.Release(); err != nil {
		t.Fatalf("release first newer job: %v", err)
	}
	secondNewer := waitResult(t, newerResults[0], 2*time.Second)
	if secondNewer.err != nil || secondNewer.lease == nil {
		t.Fatalf("safe second backfill = lease=%v err=%v, want grant", secondNewer.lease, secondNewer.err)
	}
	qs, err = client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query protected stream: %v", err)
	}
	olderWaiter, ok = waiterByRun(qs, "older-small")
	if !ok || olderWaiter.BackfillCount != 2 {
		t.Fatalf("protected older waiter = %+v, present=%v; want one additional bypass inside reserved spare cores", olderWaiter, ok)
	}
	for i, result := range newerResults[1:] {
		select {
		case r := <-result:
			t.Fatalf("newer high-core job %d exceeded protected reservation: lease=%v err=%v", i+2, r.lease, r.err)
		default:
		}
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release memory holder: %v", err)
	}
	olderGrant := waitResult(t, olderResult, 2*time.Second)
	if olderGrant.err != nil || olderGrant.lease == nil || olderGrant.lease.RunID != "older-small" {
		t.Fatalf("older waiter grant = lease=%v err=%v, want bounded grant", olderGrant.lease, olderGrant.err)
	}
	if err := olderGrant.lease.Release(); err != nil {
		t.Fatalf("release older waiter: %v", err)
	}
	if err := secondNewer.lease.Release(); err != nil {
		t.Fatalf("release safe second backfill: %v", err)
	}

	qs, err = client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query after older departure: %v", err)
	}
	if qs.Events == nil || qs.Events.Backfills != 2 || qs.Events.BackfillProtections != 1 {
		t.Fatalf("events after older departure = %+v, want persisted starvation history", qs.Events)
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
