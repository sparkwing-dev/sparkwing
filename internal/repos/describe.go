package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
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
}

// Call sends req with the next request id and decodes the reply's result into
// result. A failed reply returns its [*nodeproto.Error]; the process stays up
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

// Describe sends describe and refuses a binary that speaks another protocol
// version.
func (s *Session) Describe() (sdkserve.DescribeDoc, error) {
	var doc sdkserve.DescribeDoc
	if err := s.Call(nodeproto.Request{Op: nodeproto.OpDescribe}, &doc); err != nil {
		return doc, err
	}
	if doc.Protocol != nodeproto.Version {
		sdk := "unknown SDK"
		if doc.SDK != nil {
			sdk = doc.SDK.Language + " SDK " + doc.SDK.Version
		}
		return doc, fmt.Errorf("pipeline entry speaks node protocol %q (%s); this engine speaks %q; rebuild .sparkwing against a matching SDK",
			doc.Protocol, sdk, nodeproto.Version)
	}
	return doc, nil
}

// Describe starts the pipeline binary at binPath in dir and returns its
// describe reply.
func Describe(ctx context.Context, binPath, dir string) (sdkserve.DescribeDoc, error) {
	var doc sdkserve.DescribeDoc
	err := Converse(ctx, binPath, dir, func(s *Session) (err error) {
		doc, err = s.Describe()
		return err
	})
	return doc, err
}

// Converse starts the pipeline binary at binPath in dir with
// --sw-node-protocol and hands talk a [Session] on it. A process that has not
// finished within [SessionTimeout] is killed with every process it started.
func Converse(ctx context.Context, binPath, dir string, talk func(*Session) error) error {
	ctx, cancel := context.WithTimeoutCause(ctx, SessionTimeout,
		fmt.Errorf("%s: no reply within %s: %w", binPath, SessionTimeout, context.DeadlineExceeded))
	defer cancel()
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
	// #nosec G204 -- this executes the selected pipeline binary without a shell.
	cmd := exec.Command(binPath, protofd.Arg)
	cmd.Dir, cmd.Stderr = dir, &stderr
	// bug: a CLI run from inside a node inherits that node's route URL without its bearer, and an SDK
	// process that finds the one without the other refuses to start; this describe serves no node.
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, protofd.NodeURLEnv+"=") || strings.HasPrefix(kv, protofd.NodeTokenEnv+"=")
	})
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

	s := &Session{requests: nodeproto.NewWriter(reqW), frames: nodeproto.NewReader(frameR)}
	talkErr := talk(s)
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
		return sessionFailure(s.broken, waitErr, stderr.String())
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
			return nodeproto.Reply{}, &nodeproto.Error{Code: nodeproto.CodeProtocolError, Message: err.Error()}
		case f.Kind != nodeproto.KindReply:
			continue
		case f.Reply.ID != req.ID:
			msg := fmt.Sprintf("%v: %s", nodeproto.ErrUnknownReply, f.Reply.ID)
			return nodeproto.Reply{}, &nodeproto.Error{Code: nodeproto.CodeProtocolError, Message: msg}
		}
		return f.Reply, nil
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
