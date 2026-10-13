// Package sdkserve is the Go SDK's side of the node protocol
// (docs/node-protocol.md): a pipeline binary started with
// --sw-node-protocol reads requests from the host's request pipe, runs the
// pipeline code they name, and writes replies and log records to the frame
// pipe. stdout and stderr stay with pipeline code.
package sdkserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeclient"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Config is what the serving loop needs from the engine packages that link
// it, which sdkserve cannot import without a cycle.
type Config struct {
	// SDKVersion is the Go SDK's module version, sent in describe so a
	// refusal can name the SDK to upgrade. Empty leaves sdk out.
	SDKVersion string
	// PlanDoc turns a built plan into the plan document the plan request
	// answers with.
	PlanDoc func(*sparkwing.Plan, sparkwing.RunContext, *sparkwing.Registration) (nodeproto.PlanDoc, error)
	// Node reaches the host's /node/v1 routes; nil leaves secrets, other
	// nodes' outputs, OIDC and child runs unreachable. Main sets it from the
	// environment.
	Node *nodeclient.Client
}

// Main serves the protocol on the inherited pipes until the host closes the
// request pipe, and returns the process exit status.
func Main(cfg Config) int {
	req, frames, err := protofd.Pipes()
	if err == nil {
		// safety: no command this process starts may read a handle list that
		// names handles it does not hold.
		err = os.Unsetenv(protofd.HandlesEnv)
	}
	if err == nil {
		cfg.Node, err = nodeclient.FromEnv()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sw-node-protocol:", err)
		return 2
	}
	// safety: bound before any pipeline code runs, so cleanup.Register never
	// falls back to the session ledger in this process.
	nodeclient.Bind(cfg.Node)
	if err := Serve(context.Background(), req, frames, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "sw-node-protocol:", err)
		return 1
	}
	return 0
}

// Planned is the state a plan request leaves behind for the requests that
// follow it in the same process.
type Planned struct {
	Registration *sparkwing.Registration
	Plan         *sparkwing.Plan
	Run          sparkwing.RunContext
	Doc          nodeproto.PlanDoc
}

type server struct {
	cfg    Config
	frames *nodeproto.Writer
	// safety: set at end of input, after which the SDK writes nothing.
	done atomic.Bool

	mu       sync.Mutex
	planSeen bool
	planned  *Planned
}

type handler struct {
	needsPlan bool
	run       func(ctx context.Context, s *server, req nodeproto.Request) (any, error)
}

// safety: an op absent from this table answers unknown_op, so a stub needs no
// entry.
var handlers = map[nodeproto.Op]handler{
	nodeproto.OpDescribe: {run: describe},
	nodeproto.OpPlan:     {run: plan},
	nodeproto.OpRunNode:  {needsPlan: true, run: runNode},
	nodeproto.OpRunStep:  {needsPlan: true, run: runStep},
	nodeproto.OpEval:     {needsPlan: true, run: eval},
}

// Serve answers each request read from in with a reply on out until in ends.
// Requests run concurrently, since the host may have several eval and
// run_step requests in flight. End of input cancels any request still
// running and returns nil without writing anything further.
func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{cfg: cfg, frames: nodeproto.NewWriter(out)}
	r := nodeproto.NewReader(in)
	for {
		req, err := r.NextRequest()
		switch {
		case errors.Is(err, io.EOF):
			s.done.Store(true)
			return nil
		case req.ID == "" && err != nil:
			return fmt.Errorf("unanswerable request: %w", err)
		case err != nil:
			s.reply(req.ID, nil, err)
			continue
		}
		go s.dispatch(ctx, req)
	}
}

func (s *server) dispatch(ctx context.Context, req nodeproto.Request) {
	h, ok := handlers[req.Op]
	switch {
	case !ok:
		s.reply(req.ID, nil, &nodeproto.Error{Code: nodeproto.CodeUnknownOp, Message: fmt.Sprintf("this SDK does not implement %s", req.Op)})
	case h.needsPlan && s.plan() == nil:
		s.reply(req.ID, nil, &nodeproto.Error{Code: nodeproto.CodeNoPlan, Message: fmt.Sprintf("%s needs a plan earlier in this process", req.Op)})
	default:
		result, err := run(ctx, s, h, req)
		s.reply(req.ID, result, err)
	}
}

// safety: a panic in pipeline code fails its request and leaves the process
// serving, so the host reads a reply rather than sdk_exited.
func run(ctx context.Context, s *server, h handler, req nodeproto.Request) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			perr := &nodeproto.Error{Message: fmt.Sprintf("%s panicked: %v", req.Op, p)}
			if req.Op == nodeproto.OpPlan {
				perr.Code = nodeproto.CodePlanFailed
			}
			result, err = nil, perr
		}
	}()
	return h.run(ctx, s, req)
}

func (s *server) plan() *Planned {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planned
}

// safety: a result too large for one line still gets a failed reply, so the
// host never waits on an id the SDK dropped.
func (s *server) reply(id string, result any, err error) {
	if err == nil {
		var raw []byte
		if raw, err = json.Marshal(result); err == nil {
			if err = s.send(nodeproto.Reply{ID: id, OK: true, Result: raw}); err == nil {
				return
			}
		}
	}
	var perr *nodeproto.Error
	if !errors.As(err, &perr) {
		perr = &nodeproto.Error{Message: err.Error()}
	}
	if err := s.send(nodeproto.Reply{ID: id, Error: perr}); err != nil {
		// safety: the host waits on every reply, so a frame pipe that takes
		// none ends the process; the read loop sits in a pipe read no cancel
		// reaches.
		fmt.Fprintln(os.Stderr, "sw-node-protocol: write frame:", err)
		os.Exit(1) //nolint:forbidigo // the process cannot answer, and only its exit tells the host
	}
}

func (s *server) send(v any) error {
	if s.done.Load() {
		return nil
	}
	return s.frames.Send(v)
}

// safety: plan code logs to the frame pipe, the only channel the host reads
// records from; stdout is the customer's text.
type frameLogger struct{ s *server }

func (l frameLogger) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{TS: time.Now(), Level: level, Msg: msg})
}

func (l frameLogger) Emit(rec sparkwing.LogRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	_ = l.s.send(rec) //nolint:errcheck // a frame pipe that breaks fails the next reply instead
}
