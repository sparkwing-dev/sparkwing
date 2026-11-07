package sdkserve

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// HostExecResult is the host's final word on a command. Stdout and Stderr are
// complete for a captured command and bounded tails for a logged one.
type HostExecResult struct {
	Done           nodeproto.ExecDone
	Stdout, Stderr []byte
}

// HostExecFunc runs a command through the host that said hello.
type HostExecFunc func(context.Context, nodeproto.ExecRequest) (HostExecResult, error)

type hostExecKey struct{}

// HostExec returns the command runner bound to the current host session.
func HostExec(ctx context.Context) (HostExecFunc, bool) {
	run, ok := ctx.Value(hostExecKey{}).(HostExecFunc)
	return run, ok
}

// WithHostExec installs run for the SDK's command helpers.
func WithHostExec(ctx context.Context, run HostExecFunc) context.Context {
	return context.WithValue(ctx, hostExecKey{}, run)
}

var errHostExecClosed = errors.New("host closed the protocol session before the command finished")

type execCall struct {
	stdout, stderr bytes.Buffer
	done           chan nodeproto.ExecDone
}

type hostExecs struct {
	mu     sync.Mutex
	seq    uint64
	calls  map[string]*execCall
	closed chan struct{}
	ended  bool
}

func (s *server) hostExec(ctx context.Context, req nodeproto.ExecRequest) (HostExecResult, error) {
	// safety: a cancelled caller must not start a command, as exec.CommandContext refuses to.
	if err := ctx.Err(); err != nil {
		return HostExecResult{}, err
	}
	call := &execCall{done: make(chan nodeproto.ExecDone, 1)}
	s.execs.mu.Lock()
	if s.execs.ended {
		s.execs.mu.Unlock()
		return HostExecResult{}, errHostExecClosed
	}
	s.execs.seq++
	req.ID = strconv.FormatUint(s.execs.seq, 10)
	s.execs.calls[req.ID] = call
	s.execs.mu.Unlock()
	s.send(req)
	cancelled := ctx.Done()
	for {
		select {
		case done := <-call.done:
			if req.Capture {
				return HostExecResult{Done: done, Stdout: call.stdout.Bytes(), Stderr: call.stderr.Bytes()}, nil
			}
			return HostExecResult{Done: done, Stdout: done.Stdout, Stderr: done.Stderr}, nil
		case <-cancelled:
			// safety: keep waiting so the result reports how the killed command ended.
			cancelled = nil
			s.send(nodeproto.ExecCancel{ID: req.ID})
		case <-s.execs.closed:
			return HostExecResult{}, errHostExecClosed
		}
	}
}

// safety: the reader is the only writer of a call's buffers, so a captured command sees its bytes in order.
func (s *server) deliverExec(line nodeproto.HostLine) error {
	s.execs.mu.Lock()
	defer s.execs.mu.Unlock()
	id := ""
	if line.Output != nil {
		id = line.Output.ID
	} else {
		id = line.Done.ID
	}
	call := s.execs.calls[id]
	if call == nil {
		return protocolError("host sent output for an unknown command")
	}
	if line.Output != nil {
		if line.Output.Stream == "stdout" {
			call.stdout.Write(line.Output.Data)
		} else {
			call.stderr.Write(line.Output.Data)
		}
		return nil
	}
	delete(s.execs.calls, id)
	call.done <- *line.Done
	return nil
}

func (s *server) endExecs() {
	s.execs.mu.Lock()
	defer s.execs.mu.Unlock()
	if !s.execs.ended {
		s.execs.ended = true
		close(s.execs.closed)
	}
}
