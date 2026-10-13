package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

// DescribeTimeout bounds one describe call from start to reply.
const DescribeTimeout = 30 * time.Second

// Describe starts the pipeline binary at binPath in dir with
// --sw-node-protocol, sends one describe request and returns the reply. A
// binary that speaks another protocol version is refused, and one that has not
// answered within [DescribeTimeout] is killed with every process it started.
func Describe(ctx context.Context, binPath, dir string) (sdkserve.DescribeDoc, error) {
	var doc sdkserve.DescribeDoc
	ctx, cancel := context.WithTimeoutCause(ctx, DescribeTimeout,
		fmt.Errorf("describe %s: no reply within %s: %w", binPath, DescribeTimeout, context.DeadlineExceeded))
	defer cancel()
	reqR, reqW, err := os.Pipe()
	if err != nil {
		return doc, err
	}
	defer func() { _ = reqW.Close() }()
	frameR, frameW, err := os.Pipe()
	if err != nil {
		_ = reqR.Close()
		return doc, err
	}
	defer func() { _ = frameR.Close() }()

	var stderr bytes.Buffer
	// #nosec G204 -- this executes the selected pipeline binary without a shell.
	cmd := exec.Command(binPath, protofd.Arg)
	cmd.Dir, cmd.Stderr = dir, &stderr
	// safety: a grandchild holding stderr must not hold Wait past the kill.
	cmd.WaitDelay = 100 * time.Millisecond
	tree, err := startTree(cmd, reqR, frameW)
	_, _ = reqR.Close(), frameW.Close()
	if err != nil {
		return doc, err
	}
	defer tree.release()
	killed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		tree.kill()
		close(killed)
	})

	reply, err := describeReply(reqW, frameR)
	if !stop() && ctx.Err() != nil {
		// safety: the group is signaled before Wait reaps its leader, never after.
		<-killed
	}
	if err != nil {
		// safety: the child may be blocked writing a frame no one reads, so it
		// dies before the wait.
		tree.kill()
	}
	// safety: the SDK drops replies once its input ends, so the request pipe
	// closes only after the reply is in; closing the frame pipe first fails
	// any write the SDK makes after it rather than leaving Wait behind it.
	_, _ = reqW.Close(), frameR.Close()
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return doc, context.Cause(ctx)
	case err != nil:
		return doc, describeFailure(err, waitErr, stderr.String())
	}
	if err := reply.Decode(&doc); err != nil {
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

const describeID = "1"

func describeReply(requests io.Writer, frames io.Reader) (nodeproto.Reply, error) {
	if err := nodeproto.NewWriter(requests).Send(nodeproto.Request{ID: describeID, Op: nodeproto.OpDescribe}); err != nil {
		return nodeproto.Reply{}, err
	}
	r := nodeproto.NewReader(frames)
	for {
		f, err := r.Next()
		switch {
		case errors.Is(err, io.EOF):
			return nodeproto.Reply{}, err
		case err != nil:
			return nodeproto.Reply{}, &nodeproto.Error{Code: nodeproto.CodeProtocolError, Message: err.Error()}
		case f.Kind != nodeproto.KindReply:
			continue
		case f.Reply.ID != describeID:
			msg := fmt.Sprintf("%v: %s", nodeproto.ErrUnknownReply, f.Reply.ID)
			return nodeproto.Reply{}, &nodeproto.Error{Code: nodeproto.CodeProtocolError, Message: msg}
		}
		return f.Reply, nil
	}
}

func describeFailure(err, waitErr error, stderr string) error {
	if errors.Is(err, io.EOF) {
		err = errors.New("pipeline entry exited without answering describe; rebuild .sparkwing against a matching SDK")
	}
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
