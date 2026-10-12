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

// Describe starts the pipeline binary at binPath in dir with
// --sw-node-protocol, sends one describe request and returns the reply. A
// binary that speaks another protocol version is refused.
func Describe(ctx context.Context, binPath, dir string) (sdkserve.DescribeDoc, error) {
	var doc sdkserve.DescribeDoc
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
	cmd := exec.CommandContext(ctx, binPath, protofd.Arg)
	cmd.Dir, cmd.Stderr = dir, &stderr
	// safety: a grandchild holding stderr must not extend the describe deadline.
	cmd.WaitDelay = 100 * time.Millisecond
	if err = passPipes(cmd, reqR, frameW); err == nil {
		err = cmd.Start()
	}
	_, _ = reqR.Close(), frameW.Close()
	if err != nil {
		return doc, err
	}
	// safety: the frame read below is the only wait that ignores ctx otherwise.
	defer context.AfterFunc(ctx, func() { _ = frameR.Close() })()

	reply, err := describeReply(reqW, frameR)
	// safety: the SDK drops replies once its input ends, so the request pipe
	// closes only after the reply is in.
	_ = reqW.Close()
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return doc, ctx.Err()
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

func describeReply(requests io.Writer, frames io.Reader) (nodeproto.Reply, error) {
	if err := nodeproto.NewWriter(requests).Send(nodeproto.Request{ID: "1", Op: nodeproto.OpDescribe}); err != nil {
		return nodeproto.Reply{}, err
	}
	r := nodeproto.NewReader(frames)
	for {
		f, err := r.Next()
		if err != nil {
			return nodeproto.Reply{}, err
		}
		if f.Kind == nodeproto.KindReply && f.Reply.ID == "1" {
			return f.Reply, nil
		}
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
