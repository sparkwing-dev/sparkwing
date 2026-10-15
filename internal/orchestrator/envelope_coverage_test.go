//go:build unix

package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: every fixture body holds at the gate until the test releases it, so the test decides what overlaps and
// counts every body that ran.
type envelopeGate struct {
	addr     string
	deadline context.Context
	arrivals chan *gateArrival
	mu       sync.Mutex
	inside   int
	peak     int
	seen     []string
	conns    []net.Conn
}

type gateArrival struct {
	who  string
	conn net.Conn
	g    *envelopeGate
}

func newEnvelopeGate(t *testing.T) *envelopeGate {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	g := &envelopeGate{addr: ln.Addr().String(), deadline: deadline, arrivals: make(chan *gateArrival, 64)}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		g.mu.Lock()
		for _, c := range g.conns {
			_ = c.Close()
		}
		g.mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			g.mu.Lock()
			g.conns = append(g.conns, conn)
			g.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				g.mu.Lock()
				g.inside++
				g.peak = max(g.peak, g.inside)
				g.seen = append(g.seen, strings.TrimSpace(line))
				g.mu.Unlock()
				g.arrivals <- &gateArrival{who: strings.TrimSpace(line), conn: conn, g: g}
			}()
		}
	}()
	return g
}

func (g *envelopeGate) next(t *testing.T) *gateArrival {
	t.Helper()
	select {
	case a := <-g.arrivals:
		return a
	case <-g.deadline.Done():
		t.Fatal("no body reached the gate")
		return nil
	}
}

// safety: called once the other nodes are seen waiting in admission, so any arrival is one that skipped the wait.
func (g *envelopeGate) none(t *testing.T) {
	t.Helper()
	select {
	case a := <-g.arrivals:
		t.Fatalf("%s ran while it should have waited", a.who)
	default:
	}
}

func (g *envelopeGate) bodies() (seen []string, peak int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.seen...), g.peak
}

func (a *gateArrival) release(reply string) {
	a.g.mu.Lock()
	a.g.inside--
	a.g.mu.Unlock()
	_, _ = fmt.Fprintln(a.conn, reply)
	_ = a.conn.Close()
}

// hack: the run store's real slots, wrapped only to report waits and to hold a key's acquires while a test blocks it.
type watchedSlots struct {
	nodehost.ConcurrencySlots
	waiting chan string
	blocked chan string
	mu      sync.Mutex
	block   map[string]chan struct{}
}

func (s *watchedSlots) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	s.mu.Lock()
	gate := s.block[req.Key]
	s.mu.Unlock()
	if gate != nil {
		s.blocked <- req.RunID + "/" + req.NodeID
		select {
		case <-gate:
		case <-ctx.Done():
			return store.AcquireSlotResponse{}, ctx.Err()
		}
	}
	resp, err := s.ConcurrencySlots.AcquireSlot(ctx, req)
	if err == nil && (resp.Kind == store.AcquireQueued || resp.Kind == store.AcquireCoalesced) {
		s.waiting <- req.RunID + "/" + req.NodeID
	}
	return resp, err
}

// hack: the real admission daemon client, wrapped only to report the requests it queues and refuses.
type watchedAdmitter struct {
	nodehost.Admitter
	queued  chan string
	refused chan string
}

func (a watchedAdmitter) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (nodehost.Lease, error) {
	var once sync.Once
	lease, err := a.Admitter.Acquire(ctx, req, func(q wingwire.Queued) {
		once.Do(func() { a.queued <- req.DisplayRunID })
		if onQueued != nil {
			onQueued(q)
		}
	})
	if err != nil {
		a.refused <- req.DisplayRunID
	}
	return lease, err
}

type noEnvelopeSecrets struct{}

func (noEnvelopeSecrets) Resolve(context.Context, string) (string, bool, error) {
	return "", false, secretsource.ErrMissing
}

type discardNodeLog struct{}

func (discardNodeLog) Emit(sparkwing.LogRecord) {}
func (discardNodeLog) Close() error             { return nil }

// safety: hosts runs as a local run is hosted, the engine host with the runs store's slots and memo entries, and a
// real admission daemon for box- and run-scoped groups and host leases when asked.
type envelopeRig struct {
	t      *testing.T
	st     *store.Store
	gate   *envelopeGate
	slots  *watchedSlots
	daemon *watchedAdmitter
	routes *nodehost.Server
	runs   sync.WaitGroup
}

func newEnvelopeRig(t *testing.T, daemon bool) *envelopeRig {
	t.Helper()
	if testing.Short() {
		t.Skip("slow: hosts several runs, each node in a process of the test binary")
	}
	home := t.TempDir()
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	routes, err := nodehost.Start(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routes.Close() })
	r := &envelopeRig{
		t: t, st: st, gate: newEnvelopeGate(t), routes: routes,
		slots: &watchedSlots{
			ConcurrencySlots: localConcurrency{st: st}, waiting: make(chan string, 64), blocked: make(chan string, 64),
			block: map[string]chan struct{}{},
		},
	}
	if daemon {
		startWingd(t, home, 16)
		r.daemon = &watchedAdmitter{
			Admitter: &hostedAdmitter{
				la: testWingdAdmission(home, nil), backends: LocalBackends(paths, st, nil), pipeline: "envelope",
				plan: func() *nodeproto.PlanDoc { return nil },
			},
			queued: make(chan string, 64), refused: make(chan string, 64),
		}
	}
	// safety: registered last, so every run has ended before the daemon, gate and store close.
	t.Cleanup(r.runs.Wait)
	return r
}

func (r *envelopeRig) args(shape string, extra ...string) map[string]string {
	args := map[string]string{"gate": r.gate.addr, "shape": shape}
	for i := 0; i+1 < len(extra); i += 2 {
		args[extra[i]] = extra[i+1]
	}
	return args
}

func (r *envelopeRig) start(runID, pipeline string, args map[string]string, tweak func(*nodehost.Config)) <-chan nodehost.RunResult {
	r.t.Helper()
	masker := secrets.NewMasker()
	cfg := nodehost.Config{
		Session: nodehost.SessionConfig{
			Entry: pipelineentry.Binary(os.Args[0]), Dir: r.t.TempDir(), Grace: 2 * time.Second,
			Env:    append(engineenv.Strip(os.Environ()), nodeProtocolEntryEnv+"=1", "GORACE=atexit_sleep_ms=0"),
			Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
			Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: masker},
		},
		Pipeline: pipeline, Args: args, Run: nodeproto.RunContext{RunID: runID},
		State: r.st, Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour,
		Attempts: &nodehost.Attempts{
			Routes: r.routes, Resolver: noEnvelopeSecrets{}, Masker: masker,
			OpenLog: func(context.Context, string, string) (nodehost.NodeLog, error) { return discardNodeLog{}, nil },
		},
		Admission: &nodehost.Admission{Slots: r.slots},
		Memo: &nodehost.Memo{Slots: r.slots, Source: func(ctx context.Context, _ store.ClaimInputRequest, runID, nodeID string) (nodehost.MemoSource, error) {
			return nodehost.ReadMemoSource(ctx, r.st, runID, nodeID)
		}},
	}
	if r.daemon != nil {
		cfg.Admission.Daemon = r.daemon
	}
	if tweak != nil {
		tweak(&cfg)
	}
	out := make(chan nodehost.RunResult, 1)
	ctx, cancel := context.WithTimeout(r.t.Context(), 2*time.Minute)
	r.runs.Add(1)
	go func() {
		defer r.runs.Done()
		defer cancel()
		res, err := nodehost.Execute(ctx, cfg)
		if err != nil {
			res.Status, res.Err = "not recorded", err
		}
		out <- res
	}()
	return out
}

func (r *envelopeRig) run(runID, pipeline string, args map[string]string) nodehost.RunResult {
	r.t.Helper()
	return <-r.start(runID, pipeline, args, nil)
}

func recv(t *testing.T, deadline context.Context, c <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-deadline.Done():
		t.Fatalf("never saw %s", what)
		return ""
	}
}

func wantRun(t *testing.T, res nodehost.RunResult, status string) {
	t.Helper()
	if res.Status != status {
		t.Fatalf("run %s = %s (%v), want %s; nodes %+v", res.RunID, res.Status, res.Err, status, res.Nodes)
	}
}

func (r *envelopeRig) node(runID, nodeID string) *store.Node {
	r.t.Helper()
	n, err := r.st.GetNode(r.t.Context(), runID, nodeID)
	if err != nil {
		r.t.Fatalf("node %s/%s: %v", runID, nodeID, err)
	}
	return n
}

func (r *envelopeRig) output(runID, nodeID string) string {
	r.t.Helper()
	out, err := r.st.GetNodeOutput(r.t.Context(), runID, nodeID)
	if err != nil {
		r.t.Fatalf("output of %s/%s: %v", runID, nodeID, err)
	}
	return string(out)
}

// Six nodes of one memo key, two in each of three runs of two pipelines,
// arrive together: one body runs and the other five take its result, and a
// later run of either pipeline reuses it without running.
func TestEnvelope_SimultaneousMemoNodesAcrossPipelinesRunOneBody(t *testing.T) {
	r := newEnvelopeRig(t, false)
	args := r.args("memo", "key", "shared-v1")
	lead := r.start("memo-lead", "envelope-a", args, nil)
	leader := r.gate.next(t)
	f1 := r.start("memo-f1", "envelope-b", args, nil)
	f2 := r.start("memo-f2", "envelope-a", args, nil)
	for range 5 {
		recv(t, r.gate.deadline, r.slots.waiting, "a memo node waiting on the leader")
	}
	r.gate.none(t)
	leader.release("ok")
	want := "\"from " + leader.who + "\""
	for _, ch := range []<-chan nodehost.RunResult{lead, f1, f2} {
		res := <-ch
		wantRun(t, res, "success")
		for _, id := range []string{"build", "twin"} {
			if got := r.output(res.RunID, id); got != want {
				t.Errorf("%s/%s output %s, want the leader's %s", res.RunID, id, got, want)
			}
		}
	}
	hit := r.run("memo-hit", "envelope-b", args)
	wantRun(t, hit, "success")
	for _, id := range []string{"build", "twin"} {
		if n := r.node("memo-hit", id); n.Outcome != string(sparkwing.Memoized) || r.output("memo-hit", id) != want {
			t.Errorf("memo-hit/%s = %s with %s, want cached with the leader's output", id, n.Outcome, r.output("memo-hit", id))
		}
	}
	if seen, _ := r.gate.bodies(); len(seen) != 1 {
		t.Fatalf("bodies %v ran, want only the leader's", seen)
	}
}

// A memo leader that its concurrency group skips leaves no result behind, so
// the run that coalesced on it runs its own body rather than take a success
// nothing produced.
func TestEnvelope_ASkippedMemoLeaderLeavesItsFollowerToRun(t *testing.T) {
	r := newEnvelopeRig(t, false)
	groupKey := nodehost.ConcurrencyKey(nodeproto.ConcurrencySpec{Group: "memo-skip-grp"}, "", "")
	if resp, err := teststore.ForTeam(t, r.st, store.DefaultTeam).AcquireConcurrencySlot(t.Context(), store.AcquireSlotRequest{
		Key: groupKey, HolderID: "other/x", RunID: "other", NodeID: "x", Capacity: 1, Policy: "queue",
	}); err != nil || resp.Kind != store.AcquireGranted {
		t.Fatalf("hold the group: %+v %v", resp, err)
	}
	release := make(chan struct{})
	r.slots.mu.Lock()
	r.slots.block[groupKey] = release
	r.slots.mu.Unlock()
	lead := r.start("skip-lead", "envelope-a", r.args("memo-skip-lead", "key", "skip-dup"), nil)
	recv(t, r.gate.deadline, r.slots.blocked, "the leader holding its memo key while it asks for its group")
	follow := r.start("skip-follow", "envelope-b", r.args("memo-follow", "key", "skip-dup"), nil)
	recv(t, r.gate.deadline, r.slots.waiting, "the follower coalescing on the leader")
	close(release)
	wantRun(t, <-lead, "success")
	if n := r.node("skip-lead", "lead"); n.Outcome != string(sparkwing.Skipped) {
		t.Fatalf("lead = %s (%s), want skipped by its full group", n.Outcome, n.Error)
	}
	body := r.gate.next(t)
	if body.who != "skip-follow/follow" {
		t.Fatalf("%s ran, want the follower's own body", body.who)
	}
	body.release("ok")
	wantRun(t, <-follow, "success")
	if got := r.output("skip-follow", "follow"); got != "\"from skip-follow/follow\"" {
		t.Fatalf("follower output %s, want its own", got)
	}
}

// Box-scoped groups are one budget across every run on the machine: the
// second run's node waits in the daemon until the first one's body ends.
func TestEnvelope_BoxScopeSerializesAcrossRuns(t *testing.T) {
	r := newEnvelopeRig(t, true)
	a := r.start("box-1", "envelope-a", r.args("box"), nil)
	b := r.start("box-2", "envelope-a", r.args("box"), nil)
	first := r.gate.next(t)
	recv(t, r.gate.deadline, r.daemon.queued, "the other run's node queued on the box group")
	r.gate.none(t)
	first.release("ok")
	r.gate.next(t).release("ok")
	wantRun(t, <-a, "success")
	wantRun(t, <-b, "success")
	if _, peak := r.gate.bodies(); peak != 1 {
		t.Fatalf("%d bodies ran at once, want the box group's 1", peak)
	}
}

// A run-scoped group bounds each run apart: the second node of one run waits
// on its own run's group while the other run's node holds a slot of the
// group's name at the same time.
func TestEnvelope_RunScopeIsolatesRuns(t *testing.T) {
	r := newEnvelopeRig(t, true)
	pair := r.start("run-1", "envelope-a", r.args("run-pair"), nil)
	single := r.start("run-2", "envelope-a", r.args("run"), nil)
	held := map[string]*gateArrival{}
	for len(held) < 2 {
		a := r.gate.next(t)
		run, _, _ := strings.Cut(a.who, "/")
		if held[run] != nil {
			t.Fatalf("%s ran while %s held its run's group", a.who, held[run].who)
		}
		held[run] = a
	}
	if who := recv(t, r.gate.deadline, r.daemon.queued, "run-1's second node queued on its run's group"); !strings.HasPrefix(who, "run-1/") {
		t.Fatalf("%s queued, want run-1's second node", who)
	}
	r.gate.none(t)
	held["run-2"].release("ok")
	held["run-1"].release("ok")
	r.gate.next(t).release("ok")
	wantRun(t, <-pair, "success")
	wantRun(t, <-single, "success")
}

// A node's cost counts against the group's capacity across runs: three
// nodes of cost 4 in a box group of 8 run two at a time.
func TestEnvelope_WeightedCostsShareABoxGroupAcrossRuns(t *testing.T) {
	r := newEnvelopeRig(t, true)
	a := r.start("cost-1", "envelope-a", r.args("cost-a"), nil)
	b := r.start("cost-2", "envelope-b", r.args("cost-b"), nil)
	one, two := r.gate.next(t), r.gate.next(t)
	recv(t, r.gate.deadline, r.daemon.queued, "the third node queued on the group's spent capacity")
	r.gate.none(t)
	one.release("ok")
	r.gate.next(t).release("ok")
	two.release("ok")
	wantRun(t, <-a, "success")
	wantRun(t, <-b, "success")
	if seen, peak := r.gate.bodies(); len(seen) != 3 || peak != 2 {
		t.Fatalf("bodies %v with %d at once, want 3 with at most 2", seen, peak)
	}
}

func TestEnvelope_QueuedGroupNodesDoNotBlockIndependentWork(t *testing.T) {
	r := newEnvelopeRig(t, false)
	res := r.start("workers", "envelope-a", r.args("workers"), nil)
	var grouped, pre *gateArrival
	for grouped == nil || pre == nil {
		switch a := r.gate.next(t); {
		case a.who == "workers/pre" && pre == nil:
			pre = a
		case strings.HasPrefix(a.who, "workers/g") && grouped == nil:
			grouped = a
		default:
			t.Fatalf("%s ran beside %v and %v", a.who, grouped, pre)
		}
	}
	recv(t, r.gate.deadline, r.slots.waiting, "a grouped node queued")
	recv(t, r.gate.deadline, r.slots.waiting, "a second grouped node queued")
	pre.release("ok")
	if free := r.gate.next(t); free.who != "workers/free" {
		t.Fatalf("%s ran, want the independent node while %s holds the group", free.who, grouped.who)
	} else {
		free.release("ok")
	}
	grouped.release("ok")
	r.gate.next(t).release("ok")
	r.gate.next(t).release("ok")
	wantRun(t, <-res, "success")
}

func TestHostedRunLayersTheCheckoutsYAMLArgsUnderItsOwn(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: hosts two runs, each node in a process of the test binary")
	}
	yaml := "defaults:\n  args:\n    zone: eu-west\n    shape: none\npipelines:\n  - name: envelope-a\n    entrypoint: EnvelopeA\n    args:\n      shape: args\n"
	for _, c := range []struct {
		name string
		args map[string]string
		want string
	}{
		{"YAMLFillsWhatTheRunLeftOut", nil, "eu-west"},
		{"TheRunsOwnArgBeatsTheYAML", map[string]string{"zone": "us-east"}, "us-east"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, marker := checkoutWith(t, yaml), t.TempDir()
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := teststore.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			args := map[string]string{"marker": marker}
			for k, v := range c.args {
				args[k] = v
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			out, err := RunHosted(ctx, paths, Options{RunID: "yaml-args", Pipeline: "envelope-a", Args: args, State: st, Delegate: &storedLog{}},
				HostedEntry{
					Command: pipelineentry.Binary(os.Args[0]), Dir: dir,
					Env: append(os.Environ(), nodeProtocolEntryEnv+"=1", "GORACE=atexit_sleep_ms=0"),
				})
			if err != nil || out.Status != "success" {
				t.Fatalf("run = %+v, %v", out, err)
			}
			if got := readTrimmed(t, filepath.Join(marker, "zone")); got != c.want {
				t.Fatalf("the run planned with zone %q, want %q", got, c.want)
			}
		})
	}
}
