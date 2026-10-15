package nodehost

import (
	"context"
	"sync"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func init() { waiterPoll = 5 * time.Millisecond }

const testBox = "box-1"

type fakeDaemon struct {
	mu       sync.Mutex
	changed  chan struct{}
	used     map[string]int
	reqs     []wingwire.AdmissionRequest
	leases   map[string]*fakeLease
	live     int
	granted  int
	released int
	hostGate chan struct{}
	queued   chan string
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{changed: make(chan struct{}), used: map[string]int{}, leases: map[string]*fakeLease{}, queued: make(chan string, 64)}
}

func (d *fakeDaemon) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (Lease, error) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	d.mu.Unlock()
	if d.hostGate != nil && !req.SemaphoresOnly {
		select {
		case <-d.hostGate:
		default:
			onQueued(wingwire.Queued{RunID: req.RunID, Key: "cores", Position: 1})
			d.queued <- req.RunID
			select {
			case <-d.hostGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	reported := false
	for {
		d.mu.Lock()
		full := ""
		for _, c := range req.Semaphores {
			if d.used[c.Name]+max(1, c.Cost) > max(1, c.Capacity) {
				full = c.Name
			}
		}
		if full == "" {
			for _, c := range req.Semaphores {
				d.used[c.Name] += max(1, c.Cost)
			}
			l := &fakeLease{
				d: d, req: req, done: make(chan struct{}), evict: make(chan wingwire.Evicted, 1),
				cancel: make(chan wingwire.Cancel, 1), lose: make(chan error, 1), probes: make(chan string), acks: make(chan string, 1),
				watchDone: make(chan struct{}),
			}
			d.leases[req.RunID] = l
			d.live++
			d.granted++
			d.mu.Unlock()
			return l, nil
		}
		ch := d.changed
		d.mu.Unlock()
		if p := req.Semaphores[0].Policy; p == wingwire.PolicySkip || p == wingwire.PolicyFail {
			return nil, &wingdclient.AdmissionError{Policy: p, Key: full}
		}
		if !reported {
			reported = true
			onQueued(wingwire.Queued{RunID: req.RunID, Key: full, Position: 2})
			d.queued <- req.RunID
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (d *fakeDaemon) counts() (live, granted, released int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.live, d.granted, d.released
}

func (d *fakeDaemon) requests() []wingwire.AdmissionRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]wingwire.AdmissionRequest(nil), d.reqs...)
}

type fakeLease struct {
	d         *fakeDaemon
	req       wingwire.AdmissionRequest
	once      sync.Once
	done      chan struct{}
	evict     chan wingwire.Evicted
	cancel    chan wingwire.Cancel
	lose      chan error
	probes    chan string
	acks      chan string
	watching  sync.WaitGroup
	watchDone chan struct{}
}

func (l *fakeLease) Token() string { return "lease-" + l.req.RunID }

func (l *fakeLease) Release() error {
	l.once.Do(func() {
		d := l.d
		d.mu.Lock()
		for _, c := range l.req.Semaphores {
			d.used[c.Name] -= max(1, c.Cost)
		}
		d.live--
		d.released++
		close(d.changed)
		d.changed = make(chan struct{})
		d.mu.Unlock()
		close(l.done)
	})
	return nil
}

// hack: the fake answers liveness probes only while Watch runs, as the real
// client answers them only from its lease watcher.
func (l *fakeLease) Watch(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) error {
	l.watching.Add(1)
	defer l.watching.Done()
	defer close(l.watchDone)
	for {
		select {
		case <-l.done:
			return nil
		case ev := <-l.evict:
			onEvicted(ev)
		case c := <-l.cancel:
			onCancel(c)
		case err := <-l.lose:
			return err
		case nonce := <-l.probes:
			l.acks <- nonce
		}
	}
}

func (d *fakeDaemon) only(t *testing.T) *fakeLease {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.leases) != 1 {
		t.Errorf("%d leases, want 1", len(d.leases))
		return nil
	}
	for _, l := range d.leases {
		return l
	}
	return nil
}

func awaitHostedEvent(t *testing.T, ctx context.Context, event <-chan string, done <-chan RunResult) string {
	t.Helper()
	select {
	case value := <-event:
		return value
	case result := <-done:
		t.Fatalf("host returned before its event: %v", result.Err)
	case <-ctx.Done():
		t.Fatal("host event did not arrive:", context.Cause(ctx))
	}
	return ""
}
