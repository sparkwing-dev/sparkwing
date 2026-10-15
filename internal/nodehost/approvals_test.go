package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type gateStore struct {
	storage.StateStoreOld
	created     chan string
	getApproval func(ctx context.Context, runID, nodeID string) (*store.Approval, error)

	mu     sync.Mutex
	events []string
}

func (g *gateStore) CreateApproval(ctx context.Context, a store.Approval) error {
	err := g.StateStoreOld.CreateApproval(ctx, a)
	if err == nil {
		g.created <- a.NodeID
	}
	return err
}

func (g *gateStore) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	if g.getApproval != nil {
		return g.getApproval(ctx, runID, nodeID)
	}
	return g.StateStoreOld.GetApproval(ctx, runID, nodeID)
}

func (g *gateStore) AppendEvent(_ context.Context, _, nodeID, kind string, _ []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, nodeID+":"+kind)
	return nil
}

type gateLog struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
	closed  bool
}

func (l *gateLog) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
}

func (l *gateLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return nil
}

func fastApprovalPoll(t *testing.T) {
	prev := approvalPoll
	approvalPoll = time.Millisecond
	t.Cleanup(func() { approvalPoll = prev })
}

func hostGates(t *testing.T, ctx context.Context, gs *gateStore, doc nodeproto.PlanDoc, fx *fakeExec) (RunResult, map[string]*gateLog) {
	t.Helper()
	if err := gs.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := validateNodes(doc.Jobs, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	masker := secrets.NewMasker()
	masker.Register("s3cret-value")
	var mu sync.Mutex
	logs := map[string]*gateLog{}
	cfg := Config{Execute: fx.run, Attempts: &Attempts{Masker: masker, OpenLog: func(_ context.Context, _, node string) (NodeLog, error) {
		mu.Lock()
		defer mu.Unlock()
		logs[node] = &gateLog{}
		return logs[node], nil
	}}}
	run := &Run{ID: testRunID, Doc: doc, State: gs, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	s := &scheduler{run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(&cfg)}
	if err := s.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	s.loop(ctx)
	res.Status, res.Err = s.verdict(ctx)
	return res, logs
}

func gateDoc(gate nodeproto.ApprovalSpec) nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "gate", Needs: deps("build"), Approval: &gate},
		{ID: "deploy", Needs: deps("gate"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "rollback", OnFailureOf: "gate", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}
}

func TestApprovalGateResolvesAsTheDispatcherDoes(t *testing.T) {
	fastApprovalPoll(t)
	cases := map[string]struct {
		gate       nodeproto.ApprovalSpec
		resolve    func(st *store.Store) error
		outcome    sparkwing.Outcome
		errHas     string
		annotation string
		output     string
		row        string
	}{
		"granted": {
			gate:       nodeproto.ApprovalSpec{Message: "ship it?"},
			resolve:    resolveAs(store.ApprovalResolutionApproved, "alice", "looks good"),
			outcome:    sparkwing.Success,
			annotation: "approved by alice · looks good",
			output:     `{"approver":"alice","comment":"looks good","resolution":"approved"}`,
			row:        store.ApprovalResolutionApproved,
		},
		"denied": {
			gate:       nodeproto.ApprovalSpec{Message: "ship it?"},
			resolve:    resolveAs(store.ApprovalResolutionDenied, "bob", "not today"),
			outcome:    sparkwing.Failed,
			errHas:     "denied by bob: not today",
			annotation: "denied by bob: not today",
			output:     `{"approver":"bob","comment":"not today","resolution":"denied"}`,
			row:        store.ApprovalResolutionDenied,
		},
		"timed out failing": {
			gate:       nodeproto.ApprovalSpec{TimeoutMS: new(int64(1))},
			outcome:    sparkwing.Failed,
			errHas:     "approval timed out (policy=fail)",
			annotation: "approval timed out (policy=fail)",
			row:        store.ApprovalResolutionTimedOut,
		},
		"timed out denying": {
			gate:       nodeproto.ApprovalSpec{TimeoutMS: new(int64(1)), OnTimeout: store.ApprovalOnTimeoutDeny},
			outcome:    sparkwing.Failed,
			errHas:     "approval timed out (policy=deny)",
			annotation: "auto-denied (timeout policy=deny)",
			row:        store.ApprovalResolutionTimedOut,
		},
		"timed out approving": {
			gate:       nodeproto.ApprovalSpec{TimeoutMS: new(int64(1)), OnTimeout: store.ApprovalOnTimeoutApprove},
			outcome:    sparkwing.Success,
			annotation: "auto-approved (timeout policy=approve)",
			row:        store.ApprovalResolutionTimedOut,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := openStore(t)
			gs := &gateStore{StateStoreOld: st, created: make(chan string, 1)}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			resolved := make(chan error, 1)
			go func() {
				select {
				case <-gs.created:
				case <-ctx.Done():
					resolved <- errors.New("the gate never created its approval")
					return
				}
				if tc.resolve == nil {
					resolved <- nil
					return
				}
				resolved <- tc.resolve(st)
			}()
			fx := &fakeExec{}

			res, logs := hostGates(t, ctx, gs, gateDoc(tc.gate), fx)

			if err := <-resolved; err != nil {
				t.Fatalf("resolve: %v", err)
			}
			wantRow(t, st, "gate", tc.outcome, tc.errHas)
			if fx.ran("gate") {
				t.Error("the gate reached the attempt executor")
			}
			appr, err := st.GetApproval(t.Context(), testRunID, "gate")
			if err != nil || appr.Resolution != tc.row || appr.Message != tc.gate.Message || appr.OnTimeout == "" {
				t.Errorf("approval row = %+v (err %v), want resolution %s", appr, err, tc.row)
			}
			row := nodeRow(t, st, "gate")
			if len(row.Annotations) != 1 || row.Annotations[0] != tc.annotation {
				t.Errorf("annotations = %q, want [%q]", row.Annotations, tc.annotation)
			}
			out, _ := st.GetNodeOutput(t.Context(), testRunID, "gate")
			if !jsonEqual(out, tc.output) {
				t.Errorf("gate output = %s, want %s", out, tc.output)
			}
			if !slices.Contains(gs.events, "gate:approval_requested") {
				t.Errorf("events = %v, want approval_requested", gs.events)
			}
			gl := logs["gate"]
			if gl == nil || !gl.closed || len(gl.records) != 2 || gl.records[0].Event != "approval_requested" || gl.records[1].Event != "approval_resolved" || gl.records[1].Msg != tc.annotation {
				t.Errorf("gate log = %+v, want a closed log with requested then resolved", gl)
			}
			if tc.outcome == sparkwing.Success {
				wantRow(t, st, "deploy", sparkwing.Success, "")
				wantRow(t, st, "rollback", sparkwing.Skipped, `parent "gate" did not fail`)
				if res.Status != "success" {
					t.Errorf("run = %s %v, want success", res.Status, res.Err)
				}
				return
			}
			wantRow(t, st, "deploy", sparkwing.Cancelled, "upstream-failed")
			wantRow(t, st, "rollback", sparkwing.Success, "")
			if f := fx.seen["rollback"].Failure; f == nil || f.Job != "gate" || f.Stage != "action" || f.Error == nil || f.Error.Message != tc.errHas {
				t.Errorf("rollback failure = %+v, want the gate's action failure %q", f, tc.errHas)
			}
			if res.Status != "failed" {
				t.Errorf("run = %s %v, want failed", res.Status, res.Err)
			}
		})
	}
}

func resolveAs(resolution, approver, comment string) func(*store.Store) error {
	return func(st *store.Store) error {
		_, err := st.ResolveApproval(context.Background(), testRunID, "gate", resolution, approver, comment)
		return err
	}
}

func jsonEqual(got []byte, want string) bool {
	if want == "" {
		return len(got) == 0
	}
	var g, w any
	return json.Unmarshal(got, &g) == nil && json.Unmarshal([]byte(want), &w) == nil && jsonText(g) == jsonText(w)
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestApprovalGateCancelledWithTheRun(t *testing.T) {
	fastApprovalPoll(t)
	st := openStore(t)
	gs := &gateStore{StateStoreOld: st, created: make(chan string, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-gs.created:
		case <-t.Context().Done():
		}
		cancel()
	}()
	fx := &fakeExec{}

	res, logs := hostGates(t, ctx, gs, gateDoc(nodeproto.ApprovalSpec{}), fx)

	wantRow(t, st, "gate", sparkwing.Cancelled, "ctx-cancelled")
	wantRow(t, st, "deploy", sparkwing.Cancelled, "")
	if fx.ran("rollback") {
		t.Error("the recovery ran for a cancelled gate")
	}
	if appr, err := st.GetApproval(t.Context(), testRunID, "gate"); err != nil || appr.ResolvedAt != nil {
		t.Errorf("approval = %+v (err %v), want it left unresolved", appr, err)
	}
	if len(nodeRow(t, st, "gate").Annotations) != 0 || len(logs["gate"].records) != 1 {
		t.Errorf("a cancelled gate recorded a resolution: %+v", logs["gate"].records)
	}
	if res.Status != "failed" {
		t.Errorf("run = %s %v, want failed for a plain cancel", res.Status, res.Err)
	}
}

func TestApprovalGateTimeoutYieldsToALateHumanAnswer(t *testing.T) {
	fastApprovalPoll(t)
	st := openStore(t)
	reads := 0
	gs := &gateStore{StateStoreOld: st, created: make(chan string, 1)}
	gs.getApproval = func(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
		reads++
		if reads == 1 {
			// safety: the answer lands between the gate's read and its timeout write.
			if _, err := st.ResolveApproval(ctx, runID, nodeID, store.ApprovalResolutionApproved, "carol", ""); err != nil {
				return nil, err
			}
			return &store.Approval{RunID: runID, NodeID: nodeID}, nil
		}
		return st.GetApproval(ctx, runID, nodeID)
	}

	res, _ := hostGates(t, t.Context(), gs, gateDoc(nodeproto.ApprovalSpec{TimeoutMS: new(int64(1))}), &fakeExec{})

	wantRow(t, st, "gate", sparkwing.Success, "")
	if row := nodeRow(t, st, "gate"); len(row.Annotations) != 1 || row.Annotations[0] != "approved by carol" {
		t.Errorf("annotations = %q, want carol's approval", row.Annotations)
	}
	if res.Status != "success" {
		t.Errorf("run = %s %v, want success", res.Status, res.Err)
	}
}

func TestApprovalGateFailsOnAStoreThatCannotRecover(t *testing.T) {
	fastApprovalPoll(t)
	st := openStore(t)
	gs := &gateStore{StateStoreOld: st, created: make(chan string, 1)}
	gs.getApproval = func(context.Context, string, string) (*store.Approval, error) {
		return nil, errors.New("database: locking protocol")
	}

	hostGates(t, t.Context(), gs, gateDoc(nodeproto.ApprovalSpec{}), &fakeExec{})

	wantRow(t, st, "gate", sparkwing.Failed, "locking protocol")
	wantRow(t, st, "rollback", sparkwing.Success, "")
}
