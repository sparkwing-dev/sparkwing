package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// SessionTimeout bounds one conversation with a pipeline binary, from start
// to its last reply.
const SessionTimeout = 30 * time.Second

// Session is one pipeline process answering requests in turn.
type Session struct {
	requests *nodeproto.Writer
	frames   *nodeproto.Reader
	next     int
	broken   error
	masker   *secrets.Masker
	gate     *runspec.DeclarationGate
}

// Call sends req with the next request id and decodes the reply's result into
// result. A failed reply returns its [*apierr.Error]; the process stays up
// for the next request.
func (s *Session) Call(req nodeproto.Request, result any) error {
	if s.broken != nil {
		return s.broken
	}
	s.next++
	req.ID = strconv.Itoa(s.next)
	reply, err := exchange(s.requests, s.frames, req)
	if err != nil {
		s.broken = err
		return err
	}
	return reply.Decode(result)
}

// Describe sends describe after the session's protocol handshake.
func (s *Session) Describe() (*runspec.DeclarationGate, error) {
	var doc nodeproto.DescribeDoc
	err := s.Call(nodeproto.Request{Op: nodeproto.OpDescribe}, &doc)
	if err != nil {
		return nil, err
	}
	s.gate, err = runspec.NewDeclarationGate(doc, s.masker)
	if err != nil {
		return nil, err
	}
	return s.gate, nil
}

// Describe starts the pipeline entry in dir and returns its describe reply.
func Describe(ctx context.Context, entry pipelineentry.Command, dir string) (*runspec.DeclarationGate, error) {
	var doc *runspec.DeclarationGate
	err := Converse(ctx, entry, dir, func(s *Session) (err error) {
		doc, err = s.Describe()
		return err
	})
	return doc, err
}

// Converse starts the pipeline entry in dir with --sw-node-protocol and hands
// talk a [Session] on it. A process that has not finished within
// [SessionTimeout] is killed with every process it started.
func Converse(ctx context.Context, entry pipelineentry.Command, dir string, talk func(*Session) error) (result error) {
	ctx, cancel := context.WithTimeoutCause(ctx, SessionTimeout,
		fmt.Errorf("%s: no reply within %s: %w", entry[len(entry)-1], SessionTimeout, context.DeadlineExceeded))
	defer cancel()
	masker := secrets.NewMasker()
	var s *Session
	defer func() {
		if result != nil {
			var gate *runspec.DeclarationGate
			if s != nil {
				gate = s.gate
			}
			result = errors.Join(context.Cause(ctx), gate.Error(result))
		}
	}()
	environment, err := procenv.Build(procenv.Plan, procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}, procenv.Runtime{Runner: procenv.RunnerInfo{Name: "describe", Type: "local"}})
	if err != nil {
		return err
	}
	reqR, reqW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = reqW.Close() }()
	frameR, frameW, err := os.Pipe()
	if err != nil {
		_ = reqR.Close()
		return err
	}
	defer func() { _ = frameR.Close() }()

	var stderr bytes.Buffer
	cmd := entry.Exec(environment)
	cmd.Dir, cmd.Stderr = dir, &stderr
	// safety: a grandchild holding stderr must not hold Wait past the kill.
	cmd.WaitDelay = 100 * time.Millisecond
	tree, err := startTree(cmd, reqR, frameW)
	_, _ = reqR.Close(), frameW.Close()
	if err != nil {
		return err
	}
	defer tree.release()
	killed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		tree.kill()
		close(killed)
	})

	s = &Session{requests: nodeproto.NewWriter(reqW), frames: nodeproto.NewReader(frameR), masker: masker}
	var hello nodeproto.HelloResult
	talkErr := s.Call(nodeproto.Request{
		Op: nodeproto.OpHello, Protocol: nodeproto.Version,
		Engine: &nodeproto.EngineIdentity{Version: buildinfo.Read("sparkwing", "").Version},
		Runner: &nodeproto.RunnerIdentity{Name: "describe", Type: "local", Labels: []string{}},
	}, &hello)
	if talkErr == nil {
		talkErr = talk(s)
	}
	if !stop() && ctx.Err() != nil {
		// safety: the group is signaled before Wait reaps its leader, never after.
		<-killed
	}
	if s.broken != nil {
		// safety: the child may be blocked writing a frame no one reads, so it
		// dies before the wait.
		tree.kill()
	}
	// safety: the SDK drops replies once its input ends, so the request pipe
	// closes only after the last reply is in; closing the frame pipe first
	// fails any write the SDK makes after it rather than leaving Wait behind it.
	_, _ = reqW.Close(), frameR.Close()
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return context.Cause(ctx)
	case s.broken != nil:
		return sessionFailure(s.broken, waitErr, masker.Mask(stderr.String()))
	}
	return talkErr
}

func exchange(requests *nodeproto.Writer, r *nodeproto.Reader, req nodeproto.Request) (nodeproto.Reply, error) {
	if err := requests.Send(req); err != nil {
		return nodeproto.Reply{}, err
	}
	for {
		f, err := r.Next()
		switch {
		case errors.Is(err, io.EOF):
			return nodeproto.Reply{}, fmt.Errorf("pipeline entry exited without answering %s; rebuild .sparkwing against a matching SDK", req.Op)
		case err != nil:
			return nodeproto.Reply{}, &apierr.Error{Code: apierr.ProtocolError, Message: err.Error()}
		case f.Reply == nil:
			continue
		case f.Reply.ID != req.ID:
			msg := fmt.Sprintf("%v: %s", nodeproto.ErrUnknownReply, f.Reply.ID)
			return nodeproto.Reply{}, &apierr.Error{Code: apierr.ProtocolError, Message: msg}
		}
		return *f.Reply, nil
	}
}

func sessionFailure(err, waitErr error, stderr string) error {
	if waitErr != nil {
		err = fmt.Errorf("%w (%w)", err, waitErr)
	}
	if s := strings.TrimSpace(stderr); s != "" {
		const max = 2048
		if len(s) > max {
			s = s[len(s)-max:]
		}
		err = fmt.Errorf("%w: %s", err, s)
	}
	return err
}
