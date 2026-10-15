// Package sdkserve serves the SDK protocol without importing author or engine implementations.
package sdkserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

// Config supplies the SDK operations and identity for one serving process.
type Config struct {
	SDKVersion string
	Operations Operations
}

// Main serves the inherited private pipes and returns the process exit code.
func Main(cfg Config) int {
	in, out, err := protofd.Pipes()
	if err == nil {
		err = Serve(context.Background(), in, out, cfg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sw-node-protocol:", err)
		return 1
	}
	return 0
}

type server struct {
	cfg        Config
	out        io.Writer
	writeMu    sync.Mutex
	writeErr   error
	hello      *nodeproto.Request
	planned    any
	planSeen   bool
	run        nodeproto.RunContext
	stop       context.CancelFunc
	closeInput func()
	ids        map[string]bool
	steps      map[[2]string]context.CancelFunc
	mu         sync.Mutex
	workers    sync.WaitGroup
}

// Serve negotiates once and joins every request when its input closes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	if cfg.Operations == nil {
		cfg.Operations = DefaultOperations
	}
	if cfg.Operations == nil {
		return fmt.Errorf("SDK operations unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{cfg: cfg, out: out, ids: map[string]bool{}, steps: map[[2]string]context.CancelFunc{}, stop: cancel, closeInput: func() {}}
	if closer, ok := in.(io.Closer); ok {
		s.closeInput = func() { _ = closer.Close() }
	}
	stopClose := context.AfterFunc(ctx, s.closeInput)
	defer stopClose()
	reader := nodeproto.NewReader(in)
	var readErr error
	for {
		req, err := reader.NextRequest()
		if errors.Is(err, io.EOF) {
			break
		}
		if req.ID == "" && err != nil {
			readErr = err
			break
		}
		if req.ID == "" {
			cancel()
			s.workers.Wait()
			return fmt.Errorf("unanswerable request: %w", err)
		}
		if s.ids[req.ID] {
			cancel()
			s.workers.Wait()
			return protocolError("duplicate request ID")
		}
		s.ids[req.ID] = true
		if err != nil {
			s.reply(req.ID, nil, err)
			continue
		}
		if req.Op == nodeproto.OpHello {
			s.negotiate(req)
			continue
		}
		if s.hello == nil {
			s.reply(req.ID, nil, protocolError("hello must be first"))
			continue
		}
		switch req.Op {
		case nodeproto.OpDescribe:
			run := s.run
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				result, err := s.execute(ctx, req, run)
				s.reply(req.ID, result, err)
			}()
		case nodeproto.OpPlan:
			result, err := s.execute(ctx, req, s.run)
			s.reply(req.ID, result, err)
		case nodeproto.OpCancel:
			key := [2]string{req.Job, req.Step}
			s.mu.Lock()
			if stop := s.steps[key]; stop != nil {
				stop()
			}
			s.mu.Unlock()
			s.reply(req.ID, struct{}{}, nil)
		default:
			s.start(ctx, req)
		}
	}
	if ctx.Err() != nil {
		readErr = ctx.Err()
	}
	cancel()
	s.workers.Wait()
	if s.writeErr != nil {
		return s.writeErr
	}
	return readErr
}

func (s *server) start(ctx context.Context, req nodeproto.Request) {
	if s.planned == nil {
		s.reply(req.ID, nil, &apierr.Error{Code: apierr.NoPlan, Message: "plan is required before execution"})
		return
	}
	if s.hello.Binding == nil || (req.Op == nodeproto.OpRunStep && s.hello.Binding.JobID != req.Job) {
		s.reply(req.ID, nil, protocolError("request does not match the process job binding"))
		return
	}
	if req.Op == nodeproto.OpEval {
		req.Job = s.hello.Binding.JobID
	}
	key := [2]string{req.Job, req.Step}
	if req.Op == nodeproto.OpRunStep {
		s.mu.Lock()
		_, running := s.steps[key]
		s.mu.Unlock()
		if running {
			s.reply(req.ID, nil, protocolError("step already running"))
			return
		}
	}
	if prepare, ok := s.cfg.Operations.(interface {
		Prepare(any, nodeproto.Request) error
	}); ok {
		if err := prepare.Prepare(s.planned, req); err != nil {
			s.reply(req.ID, nil, err)
			return
		}
	}
	ctx, stop := context.WithCancel(ctx)
	if req.Op == nodeproto.OpRunStep {
		s.mu.Lock()
		s.steps[key] = stop
		s.mu.Unlock()
	}
	s.workers.Add(1)
	run := s.run
	go func() {
		defer s.workers.Done()
		defer stop()
		result, err := s.execute(ctx, req, run)
		s.mu.Lock()
		delete(s.steps, key)
		s.mu.Unlock()
		s.reply(req.ID, result, err)
	}()
}

func protocolError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}

func (s *server) negotiate(req nodeproto.Request) {
	if s.hello != nil {
		s.reply(req.ID, nil, protocolError("hello is allowed once"))
		return
	}
	if req.Protocol != nodeproto.Version {
		s.reply(req.ID, nil, &apierr.Error{Code: apierr.ProtocolMismatch, Message: "unsupported SDK protocol", Hint: "rebuild the pipeline with its matching engine", Details: map[string]any{"supported": []int{nodeproto.Version}}})
		return
	}
	s.hello = &req
	s.reply(req.ID, nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: s.cfg.SDKVersion}, Features: []string{}}, nil)
}

func (s *server) execute(ctx context.Context, req nodeproto.Request, rc nodeproto.RunContext) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &apierr.Error{Code: apierr.SDKError, Message: fmt.Sprint(p)}
		}
	}()
	if req.Run != nil {
		rc = *req.Run
	}
	services := &routeServices{binding: s.hello.Binding, emit: s.emit}
	if req.Op == nodeproto.OpPlan || req.Op == nodeproto.OpDescribe {
		services.binding = nil
	}
	ctx = s.cfg.Operations.Context(ctx, rc, *s.hello.Runner, services)
	switch req.Op {
	case nodeproto.OpDescribe:
		result, err = s.cfg.Operations.Describe(ctx)
	case nodeproto.OpPlan:
		if s.planSeen {
			err = protocolError("plan is allowed once")
			return result, err
		}
		s.planSeen = true
		s.run = rc
		s.planned, result, err = s.cfg.Operations.Plan(ctx, req.Pipeline, req.Args, rc)
	case nodeproto.OpRunStep, nodeproto.OpEval:
		if req.Op == nodeproto.OpRunStep {
			result, err = s.cfg.Operations.RunStep(ctx, s.planned, req)
		} else {
			result, err = s.cfg.Operations.Eval(ctx, s.planned, req)
		}
	default:
		err = &apierr.Error{Code: apierr.UnknownOp, Message: "unsupported operation"}
	}
	return result, err
}

func (s *server) reply(id string, result any, err error) {
	r := nodeproto.Reply{ID: id, OK: err == nil}
	if err == nil {
		r.Result, err = json.Marshal(result)
		if int64(len(r.Result)) > limits.NodeOutputBytes+limits.ProtocolEnvelopeBytes-1024 {
			err = fmt.Errorf("protocol result exceeds frame limit")
		}
	}
	if err != nil {
		r.OK = false
		r.Result = nil
		if !errors.As(err, &r.Error) {
			r.Error = &apierr.Error{Code: apierr.SDKError, Message: err.Error()}
		}
	}
	s.send(r)
}
func (s *server) emit(rec nodeproto.FrameLog) { s.send(rec) }
func (s *server) send(value any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writeErr != nil {
		return
	}
	raw, err := json.Marshal(value)
	if err == nil && int64(len(raw)) > limits.NodeOutputBytes+limits.ProtocolEnvelopeBytes {
		err = fmt.Errorf("protocol frame exceeds output limit")
	}
	if err == nil {
		var n int
		raw = append(raw, '\n')
		n, err = s.out.Write(raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
	}
	s.writeErr = err
	if err != nil {
		s.stop()
		s.closeInput()
	}
}
