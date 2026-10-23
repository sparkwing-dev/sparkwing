package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

type frameOperations struct {
	sdkserve.Operations
	message  string
	document nodeproto.DescribeDoc
}

func (f *frameOperations) Context(ctx context.Context, _ nodeproto.RunContext, _ nodeproto.RunnerIdentity, services sdkserve.Services) context.Context {
	return sdkserve.WithServices(ctx, services)
}

func (f *frameOperations) Describe(ctx context.Context) (nodeproto.DescribeDoc, error) {
	if f.document.Pipelines != nil {
		return f.document, nil
	}
	sdkserve.ServicesFrom(ctx).Emit(nodeproto.FrameLog{TS: "2026-10-14T00:00:00Z", Message: f.message})
	return nodeproto.DescribeDoc{}, nil
}

func testGoSDKReplyFrameLimit(t *testing.T) {
	for _, id := range []string{"describe", strings.Repeat("quoted-\"", 128)} {
		for _, excess := range []int{0, 1} {
			t.Run(map[int]string{0: "at-limit", 1: "over-limit"}[excess], func(t *testing.T) {
				recordLeaf(t)
				doc := nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "fixture", Help: "x", Args: []nodeproto.ArgumentDescription{}}}}
				raw, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				envelope, err := json.Marshal(nodeproto.Reply{ID: id, OK: true, Result: raw})
				if err != nil {
					t.Fatal(err)
				}
				doc.Pipelines[0].Help = strings.Repeat("x", nodeproto.MaxLineBytes-len(envelope)+excess)
				fake := &frameOperations{document: doc}
				hello, err := json.Marshal(probeHello())
				if err != nil {
					t.Fatal(err)
				}
				request, err := json.Marshal(nodeproto.Request{ID: id, Op: nodeproto.OpDescribe})
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				err = sdkserve.Serve(t.Context(), strings.NewReader(string(hello)+"\n"+string(request)+"\n"), &output, sdkserve.Config{SDKVersion: "test", Operations: fake})
				if (err != nil) != (excess != 0) {
					t.Fatalf("reply at frame limit + %d: %v", excess, err)
				}
				reader := nodeproto.NewReader(&output)
				if _, err := reader.Next(); err != nil {
					t.Fatalf("hello reply: %v", err)
				}
				line, err := reader.ReadLine()
				if excess != 0 {
					if err != io.EOF {
						t.Fatalf("oversized reply wrote %d bytes: %v", len(line), err)
					}
					return
				}
				if err != nil || len(line)+1 != nodeproto.MaxLineBytes {
					t.Fatalf("reply frame size = %d, error = %v", len(line)+1, err)
				}
				frame, err := nodeproto.ParseFrame(line)
				if err != nil {
					t.Fatal(err)
				}
				var got nodeproto.DescribeDoc
				if frame.Reply == nil || frame.Reply.ID != id || !frame.Reply.OK {
					t.Fatal("SDK refused a reply within the frame limit")
				}
				if err := frame.Reply.Decode(&got); err != nil || len(got.Pipelines) != 1 || got.Pipelines[0].Help != doc.Pipelines[0].Help {
					t.Fatalf("reply content changed: %v", err)
				}
			})
		}
	}
}

func testGoSDKFrameLimit(t *testing.T) {
	for _, excess := range []int{0, 1} {
		t.Run(map[int]string{0: "at-limit", 1: "over-limit"}[excess], func(t *testing.T) {
			recordLeaf(t)
			empty, err := json.Marshal(nodeproto.FrameLog{TS: "2026-10-14T00:00:00Z", Message: "x"})
			if err != nil {
				t.Fatal(err)
			}
			fake := &frameOperations{message: strings.Repeat("x", nodeproto.MaxLineBytes-len(empty)+excess)}
			var output bytes.Buffer
			hello, err := json.Marshal(probeHello())
			if err != nil {
				t.Fatal(err)
			}
			requests := string(hello) + "\n" + `{"id":"describe","op":"describe"}` + "\n"
			err = sdkserve.Serve(t.Context(), strings.NewReader(requests), &output, sdkserve.Config{SDKVersion: "test", Operations: fake})
			if excess != 0 && err == nil {
				t.Fatal("SDK published a frame beyond the reader's line limit")
			}
			if excess == 0 && err != nil {
				t.Fatal(err)
			}
			reader := nodeproto.NewReader(&output)
			logs := 0
			for {
				frame, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("SDK published an unreadable frame: %v", err)
				}
				if frame.Log != nil {
					logs++
					if frame.Log.Message != fake.message {
						t.Fatal("SDK changed a frame within the limit")
					}
				}
			}
			if logs != 1-excess {
				t.Fatalf("published %d logs, want %d", logs, 1-excess)
			}
		})
	}
}
