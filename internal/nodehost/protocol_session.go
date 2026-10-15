package nodehost

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// ProtocolSessionConfig launches a protocol-v2 process. Native Start methods
// build environments through Environments; StartProtocolSession takes the
// explicit Environment transport input. Log must mask before storage and
// return when its context ends.
// Revoke removes route authority before Close joins the process.
type ProtocolSessionConfig struct {
	Command         []string
	Directory       string
	Environment     []string
	Environments    SDKEnvironment
	Log             func(context.Context, nodeproto.FrameLog) error
	Revoke          func() error
	RunCleanups     func(context.Context, CleanupScope) error
	Activity        ExecutorActivity
	ParseFrame      func([]byte) (nodeproto.Frame, error)
	ValidateRequest func(nodeproto.Request) error
	cleanupEnv      []string
}

// Start launches an attempt with its validated grant and shared environment.
func (cfg ProtocolSessionConfig) Start(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
	if input.processRevoke != nil {
		cfg.Revoke = input.processRevoke
	}
	if input.Entry == nil {
		return nil, cfg.failedStartup(errors.New("SDK process requires a source entry"))
	}
	cfg.Command, cfg.Directory, cfg.Activity = input.Entry.Command, input.Entry.Directory, activity
	env, complete, err := cfg.Environments.jobEnvironments(ctx, procenv.Attempt, input.Accepted, input.NodeID, input.Generation, input.SpecHash, input.GrantedRunner, cfg.RunCleanups != nil)
	if err != nil {
		return nil, cfg.failedStartup(err)
	}
	cfg.Environment = env
	cfg.cleanupEnv = complete
	return cfg.start(ctx)
}

// StartEvaluation launches owner closures with their validated grant and shared environment.
func (cfg ProtocolSessionConfig) StartEvaluation(ctx context.Context, input EvaluationInput) (ExecutorProcess, error) {
	if input.processRevoke != nil {
		cfg.Revoke = input.processRevoke
	}
	if input.Entry == nil {
		return nil, cfg.failedStartup(errors.New("SDK evaluation requires a source entry"))
	}
	cfg.Command, cfg.Directory = input.Entry.Command, input.Entry.Directory
	env, complete, err := cfg.Environments.jobEnvironments(ctx, procenv.Eval, input.Accepted, input.NodeID, input.Generation, input.SpecHash, input.GrantedRunner, cfg.RunCleanups != nil)
	if err != nil {
		return nil, cfg.failedStartup(err)
	}
	cfg.Environment = env
	cfg.cleanupEnv = complete
	return cfg.start(ctx)
}

// StartPlanning sends all describe and plan output through the planner's secret
// gate. Planning hello carries no binding and requires no delegated route.
func (cfg ProtocolSessionConfig) StartPlanning(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
	if input.Entry == nil || log == nil {
		return nil, cfg.failedStartup(errors.New("SDK planning requires a source entry and log sink"))
	}
	cfg.Command, cfg.Directory, cfg.Log = input.Entry.Command, input.Entry.Directory, log
	if err := context.Cause(ctx); err != nil {
		return nil, cfg.failedStartup(err)
	}
	env, err := cfg.Environments.planning(ctx, input)
	if err != nil {
		return nil, cfg.failedStartup(err)
	}
	cfg.Environment = env
	cfg.cleanupEnv = slices.Clone(env)
	return cfg.start(ctx)
}

func (cfg ProtocolSessionConfig) start(ctx context.Context) (ExecutorProcess, error) {
	session, err := StartProtocolSession(ctx, cfg)
	if session == nil {
		return nil, err
	}
	return session, err
}

func (cfg ProtocolSessionConfig) failedStartup(err error) error {
	if cfg.Revoke != nil {
		err = errors.Join(err, cfg.Revoke())
	}
	return err
}

type protocolCall struct {
	request  nodeproto.Request
	callerID string
	reply    chan nodeproto.Reply
}

// ProtocolSession owns one SDK process until Close. Start's context bounds setup;
// individual requests cancel independently so deadline cleanup can still run.
type ProtocolSession struct {
	cfg                       ProtocolSessionConfig
	cmd                       *exec.Cmd
	process                   sessionProcess
	requests                  *os.File
	reads                     []*os.File
	writeMu                   sync.Mutex
	logMu                     sync.Mutex
	mu                        sync.Mutex
	pending                   map[string]protocolCall
	helloSent, ready, planned bool
	failure                   error
	done                      chan struct{}
	joined                    chan struct{}
	framesDone                chan struct{}
	streams                   sync.WaitGroup
	writes                    sync.WaitGroup
	logCtx                    context.Context
	stopLogs                  context.CancelFunc
	closeOnce                 sync.Once
	closeErr                  error
	reapErr                   error
}

var errProtocolSessionClosed = errors.New("SDK protocol session closed")

// StartProtocolSession starts the acquired command with private protocol pipes.
// Callers must Close every returned session, including after a rejected hello.
func StartProtocolSession(ctx context.Context, cfg ProtocolSessionConfig) (session *ProtocolSession, err error) {
	defer func() {
		if session == nil {
			err = cfg.failedStartup(err)
		}
	}()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if len(cfg.Command) == 0 || cfg.Command[0] == "" {
		return nil, errors.New("SDK process requires a command")
	}
	if cfg.ParseFrame == nil {
		cfg.ParseFrame = nodeproto.ParseFrame
	}
	if cfg.ValidateRequest == nil {
		cfg.ValidateRequest = nodeproto.Request.Validate
	}
	var parent, child []*os.File
	for range 4 {
		r, w, err := os.Pipe()
		if err != nil {
			closeProtocolFiles(parent)
			closeProtocolFiles(child)
			return nil, err
		}
		if len(parent) == 0 {
			parent = append(parent, w)
			child = append(child, r)
		} else {
			parent = append(parent, r)
			child = append(child, w)
		}
	}
	cmd := pipelineentry.Command(cfg.Command).Exec()
	cmd.Dir, cmd.Env = cfg.Directory, append([]string{}, cfg.Environment...)
	cmd.Stdout, cmd.Stderr = child[2], child[3]
	process, err := startSDKProcess(cmd, child[0], child[1])
	closeProtocolFiles(child)
	if err != nil {
		closeProtocolFiles(parent)
		return nil, fmt.Errorf("start SDK process: %w", err)
	}
	logCtx, stopLogs := context.WithCancel(context.WithoutCancel(ctx))
	s := &ProtocolSession{cfg: cfg, cmd: cmd, process: process, requests: parent[0], reads: parent[1:], pending: make(map[string]protocolCall), done: make(chan struct{}), joined: make(chan struct{}), framesDone: make(chan struct{}), logCtx: logCtx, stopLogs: stopLogs}
	s.streams.Add(3)
	go s.readProtocolFrames(parent[1])
	go s.readProtocolText(parent[2], "info")
	go s.readProtocolText(parent[3], "warn")
	go s.watchProtocolProcess()
	if err := context.Cause(ctx); err != nil {
		return s, err
	}
	return s, nil
}

func closeProtocolFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// Request assigns a fresh private correlation ID; returned replies use the
// caller's ID. Cancellation preserves the original request's right to a reply.
func (s *ProtocolSession) Request(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
	if err := context.Cause(ctx); err != nil {
		return nodeproto.Reply{}, err
	}
	callerID := request.ID
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nodeproto.Reply{}, err
	}
	request.ID = hex.EncodeToString(nonce[:])
	if err := s.cfg.ValidateRequest(request); err != nil {
		return nodeproto.Reply{}, err
	}
	c := protocolCall{request: request, callerID: callerID, reply: make(chan nodeproto.Reply, 1)}
	s.mu.Lock()
	if err := s.acceptProtocolRequest(request); err != nil {
		s.mu.Unlock()
		return nodeproto.Reply{}, err
	}
	s.pending[request.ID] = c
	s.writes.Add(1)
	s.mu.Unlock()
	written := make(chan error, 1)
	go func() { defer s.writes.Done(); written <- s.writeProtocolRequest(request) }()
	select {
	case err := <-written:
		if err != nil {
			s.failProtocol(err)
			return nodeproto.Reply{}, err
		}
	case <-ctx.Done():
		select {
		case err := <-written:
			if err != nil {
				s.failProtocol(err)
				return nodeproto.Reply{}, err
			}
		default:
			s.failProtocol(protocolSessionError(apierr.ProtocolError, "SDK request write interrupted"))
			<-s.joined
			return nodeproto.Reply{}, context.Cause(ctx)
		}
	case <-s.done:
		return nodeproto.Reply{}, s.protocolFailure()
	}
	select {
	case reply := <-c.reply:
		return reply, nil
	case <-ctx.Done():
		if request.Op == nodeproto.OpRunStep || request.Op == nodeproto.OpEval {
			return nodeproto.Reply{}, s.cancelProtocolRequest(c, context.Cause(ctx))
		}
		err := context.Cause(ctx)
		s.failProtocol(err)
		<-s.joined
		return nodeproto.Reply{}, err
	case <-s.done:
		select {
		case reply := <-c.reply:
			return reply, nil
		default:
			return nodeproto.Reply{}, s.protocolFailure()
		}
	}
}

func (s *ProtocolSession) acceptProtocolRequest(request nodeproto.Request) error {
	if s.failure != nil {
		return s.failure
	}
	if request.Op == nodeproto.OpHello {
		if s.helloSent {
			return protocolSessionError(apierr.ProtocolError, "SDK hello may occur only once")
		}
		if request.Binding != nil && request.Binding.Routes.Bearer != "" && s.cfg.Revoke == nil {
			return errors.New("bound SDK process requires a route revocation callback")
		}
	} else if !s.ready {
		return protocolSessionError(apierr.ProtocolError, "SDK hello has not completed")
	}
	if request.Op == nodeproto.OpPlan {
		if s.planned {
			return protocolSessionError(apierr.ProtocolError, "SDK plan may occur only once")
		}
	}
	for _, pending := range s.pending {
		if request.Op == nodeproto.OpDescribe || request.Op == nodeproto.OpPlan || pending.request.Op == nodeproto.OpDescribe || pending.request.Op == nodeproto.OpPlan {
			return protocolSessionError(apierr.ProtocolError, "SDK describe and plan require an idle process")
		}
		if request.Op == nodeproto.OpRunStep && pending.request.Op == nodeproto.OpRunStep && request.Job == pending.request.Job && request.Step == pending.request.Step {
			return protocolSessionError(apierr.ProtocolError, "SDK step is already running")
		}
	}
	s.helloSent = s.helloSent || request.Op == nodeproto.OpHello
	s.planned = s.planned || request.Op == nodeproto.OpPlan
	return nil
}

func (s *ProtocolSession) writeProtocolRequest(request nodeproto.Request) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if int64(len(data)+1) > limits.NodeOutputBytes+limits.ProtocolEnvelopeBytes {
		return protocolSessionError(apierr.ProtocolError, "SDK request exceeds protocol frame limit")
	}
	if _, err = s.requests.Write(append(data, '\n')); err != nil {
		return protocolSessionError(apierr.ProtocolError, "write SDK request failed")
	}
	return nil
}

func (s *ProtocolSession) cancelProtocolRequest(call protocolCall, cause error) error {
	select {
	case <-call.reply:
		return cause
	default:
	}
	ctx, cancel := context.WithTimeout(s.logCtx, limits.CleanupTimeout)
	defer cancel()
	request := nodeproto.Request{Op: nodeproto.OpCancel}
	if call.request.Op == nodeproto.OpEval {
		request.Target = call.request.ID
	} else {
		request.Job, request.Step = call.request.Job, call.request.Step
	}
	reply, err := s.Request(ctx, request)
	if err == nil && reply.Error != nil {
		err = reply.Error
	}
	if err == nil {
		select {
		case <-call.reply:
			return cause
		case <-ctx.Done():
			err = context.Cause(ctx)
		case <-s.done:
			err = s.protocolFailure()
		}
	}
	s.failProtocol(err)
	<-s.joined
	return errors.Join(cause, err)
}

func (s *ProtocolSession) readProtocolFrames(file *os.File) {
	defer s.streams.Done()
	defer close(s.framesDone)
	defer file.Close()
	scanner := protocolScanner(file)
	for scanner.Scan() {
		frame, err := s.cfg.ParseFrame(scanner.Bytes())
		if err != nil {
			s.failProtocol(protocolSessionError(apierr.ProtocolError, "invalid SDK frame"))
			return
		}
		if frame.Log != nil {
			s.emitProtocolLog(s.attributeProtocolLog(*frame.Log))
			continue
		}
		if frame.Reply == nil {
			s.failProtocol(protocolSessionError(apierr.ProtocolError, "empty SDK frame"))
			return
		}
		if err := s.deliverProtocolReply(*frame.Reply); err != nil {
			s.failProtocol(err)
			return
		}
	}
	if scanner.Err() != nil && !errors.Is(scanner.Err(), os.ErrClosed) {
		s.failProtocol(protocolSessionError(apierr.ProtocolError, "read SDK frames failed"))
	}
}

func (s *ProtocolSession) deliverProtocolReply(reply nodeproto.Reply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.pending[reply.ID]
	if !ok {
		return protocolSessionError(apierr.ProtocolError, "SDK sent a duplicate or unsolicited reply")
	}
	delete(s.pending, reply.ID)
	if call.request.Op == nodeproto.OpHello && reply.OK {
		var hello nodeproto.HelloResult
		if err := json.Unmarshal(reply.Result, &hello); err != nil {
			return protocolSessionError(apierr.ProtocolError, "SDK hello result is invalid")
		}
		if hello.Protocol != nodeproto.Version {
			return protocolSessionError(apierr.ProtocolMismatch, "SDK protocol differs from the engine; rebuild .sparkwing")
		}
		s.ready = true
	}
	if call.request.Op == nodeproto.OpHello && reply.Error != nil && reply.Error.Code == apierr.UnknownOp {
		return &apierr.Error{Code: apierr.ProtocolMismatch, Message: "SDK does not support hello", Hint: "Rebuild .sparkwing with the matching SDK."}
	}
	reply.ID = call.callerID
	call.reply <- reply
	return nil
}

func (s *ProtocolSession) attributeProtocolLog(log nodeproto.FrameLog) nodeproto.FrameLog {
	if log.Step == "" {
		return log
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.pending {
		if call.request.Op == nodeproto.OpRunStep && call.request.Step == log.Step {
			return log
		}
	}
	log.Step = ""
	return log
}

func protocolScanner(file *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), int(limits.NodeOutputBytes+limits.ProtocolEnvelopeBytes))
	return scanner
}

func (s *ProtocolSession) readProtocolText(file *os.File, level string) {
	defer s.streams.Done()
	defer file.Close()
	scanner := protocolScanner(file)
	for scanner.Scan() {
		s.emitProtocolLog(nodeproto.FrameLog{TS: time.Now().UTC().Format(time.RFC3339Nano), Level: level, Message: scanner.Text()})
	}
	if scanner.Err() != nil && !errors.Is(scanner.Err(), os.ErrClosed) {
		s.failProtocol(protocolSessionError(apierr.ProtocolError, "SDK text stream exceeds its read limit or failed"))
	}
}

func (s *ProtocolSession) emitProtocolLog(log nodeproto.FrameLog) {
	if s.cfg.Activity.Progress != nil {
		s.cfg.Activity.Progress()
	}
	if s.cfg.Log == nil {
		return
	}
	s.logMu.Lock()
	err := s.cfg.Log(s.logCtx, log)
	s.logMu.Unlock()
	if err != nil {
		s.failProtocol(err)
	}
}

func (s *ProtocolSession) failProtocol(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = err
		close(s.done)
	} else if errors.Is(s.failure, errProtocolSessionClosed) && !errors.Is(err, errProtocolSessionClosed) {
		s.failure = err
	}
}

func (s *ProtocolSession) protocolFailure() error { s.mu.Lock(); defer s.mu.Unlock(); return s.failure }

func (s *ProtocolSession) watchProtocolProcess() {
	defer close(s.joined)
	select {
	case <-s.done:
	case <-s.process.LeaderExited():
		s.awaitProtocol(s.framesDone)
		if s.protocolFailure() == nil {
			s.failProtocol(protocolSessionError(apierr.SDKExited, "SDK process exited"))
		}
	case <-s.framesDone:
		if !s.awaitProtocol(s.process.LeaderExited()) {
			s.failProtocol(protocolSessionError(apierr.FramesClosed, "SDK closed its frame pipe"))
		} else if s.protocolFailure() == nil {
			s.failProtocol(protocolSessionError(apierr.SDKExited, "SDK process exited"))
		}
	}
	_ = s.requests.Close()
	ctx, cancel := context.WithTimeout(context.Background(), limits.CleanupTimeout)
	defer cancel()
	if s.awaitProtocol(s.process.LeaderExited()) {
		s.reapErr = s.process.Finish(ctx, procgroup.DefaultTerminationGrace)
	} else {
		s.reapErr = s.process.Terminate(ctx, procgroup.DefaultTerminationGrace)
	}
}

func (s *ProtocolSession) awaitProtocol(done <-chan struct{}) bool {
	timer := time.NewTimer(procgroup.DefaultTerminationGrace)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Close revokes authority, closes the request pipe, and joins stream readers and
// the process tree. Concurrent callers receive the same final error.
func (s *ProtocolSession) Close() error {
	s.closeOnce.Do(func() {
		var revokeErr error
		if s.cfg.Revoke != nil {
			revokeErr = s.cfg.Revoke()
		}
		s.failProtocol(errProtocolSessionClosed)
		_ = s.requests.Close()
		<-s.joined
		s.writes.Wait()
		drained := make(chan struct{})
		go func() { s.streams.Wait(); close(drained) }()
		if !s.awaitProtocol(drained) {
			s.stopLogs()
			closeProtocolFiles(s.reads)
			<-drained
		}
		s.stopLogs()
		err := s.protocolFailure()
		if errors.Is(err, errProtocolSessionClosed) {
			err = nil
		}
		s.closeErr = errors.Join(revokeErr, err, s.reapErr)
	})
	return s.closeErr
}

// Cleanup runs host-registered cleanups after Close has revoked and joined.
func (s *ProtocolSession) Cleanup(ctx context.Context) error {
	if s.cfg.RunCleanups != nil {
		env := s.cfg.cleanupEnv
		if env == nil {
			env = s.cfg.Environment
		}
		return s.cfg.RunCleanups(ctx, CleanupScope{Dir: s.cfg.Directory, Env: append([]string{}, env...)})
	}
	return nil
}

func protocolSessionError(code apierr.Code, message string) error {
	return &apierr.Error{Code: code, Message: message}
}

var _ ExecutorProcess = (*ProtocolSession)(nil)
