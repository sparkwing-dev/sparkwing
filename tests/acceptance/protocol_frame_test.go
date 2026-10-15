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
	message string
}

func (f *frameOperations) Context(ctx context.Context, _ nodeproto.RunContext, _ nodeproto.RunnerIdentity, services sdkserve.Services) context.Context {
	return sdkserve.WithServices(ctx, services)
}

func (f *frameOperations) Describe(ctx context.Context) (nodeproto.DescribeDoc, error) {
	sdkserve.ServicesFrom(ctx).Emit(nodeproto.FrameLog{TS: "2026-10-14T00:00:00Z", Message: f.message})
	return nodeproto.DescribeDoc{}, nil
}

func testGoSDKFrameLimit(t *testing.T) {
	for _, excess := range []int{0, 1} {
		t.Run(map[int]string{0: "at-limit", 1: "over-limit"}[excess], func(t *testing.T) {
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
