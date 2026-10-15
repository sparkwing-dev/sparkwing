package sdkserve

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type preparedOperations struct {
	wireOperations
	prepared []nodeproto.Request
}

func (f *preparedOperations) Prepare(_ any, request nodeproto.Request) error {
	f.prepared = append(f.prepared, request)
	return nil
}

func (f *preparedOperations) Describe(ctx context.Context) (nodeproto.DescribeDoc, error) {
	select {
	case <-f.stepStopped:
	case <-ctx.Done():
	}
	return nodeproto.DescribeDoc{}, nil
}

func TestServeRefusesForeignJobBeforePreparation(t *testing.T) {
	fake := &preparedOperations{}
	requests := strings.Join([]string{
		wireHello("hello"), wirePlan("plan"),
		`{"id":"foreign","op":"run_step","job":"other","step":"s","attempt":1}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := Serve(t.Context(), strings.NewReader(requests), &output, Config{Operations: fake}); err != nil {
		t.Fatal(err)
	}
	if reply := readWireReplies(t, output.Bytes())["foreign"]; reply.Error == nil || reply.Error.Code != apierr.ProtocolError {
		t.Fatalf("foreign job reply: %+v", reply)
	}
	if len(fake.prepared) != 0 {
		t.Fatal("foreign job changed the process environment before binding validation")
	}
}

func TestServeMetadataDoesNotBlockCancellation(t *testing.T) {
	for _, metadata := range []string{`{"id":"metadata","op":"describe"}`, wirePlan("metadata")} {
		t.Run(metadata, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := &preparedOperations{wireOperations: wireOperations{stepStarted: make(chan struct{}), stepStopped: make(chan struct{})}}
				in, writer := io.Pipe()
				var output bytes.Buffer
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- Serve(ctx, in, &output, Config{Operations: fake}) }()
				defer func() { cancel(); _ = writer.Close(); <-done }()
				for _, request := range []string{wireHello("hello"), wirePlan("plan"), `{"id":"step","op":"run_step","job":"j","step":"s","attempt":1}`} {
					if _, err := io.WriteString(writer, request+"\n"); err != nil {
						t.Fatal(err)
					}
				}
				<-fake.stepStarted
				written := make(chan error, 1)
				go func() {
					_, err := io.WriteString(writer, metadata+"\n"+`{"id":"duplicate","op":"run_step","job":"j","step":"s","attempt":1}`+"\n"+`{"id":"cancel","op":"cancel","job":"j","step":"s"}`+"\n")
					written <- err
				}()
				synctest.Wait()
				select {
				case <-fake.stepStopped:
				default:
					t.Fatal("metadata request blocked cancellation of the active step")
				}
				if err := <-written; err != nil {
					t.Fatal(err)
				}
				if len(fake.prepared) != 1 {
					t.Fatalf("prepared %d requests; only the original step should prepare", len(fake.prepared))
				}
				replies := readWireReplies(t, output.Bytes())
				if len(replies) != 6 || !replies["step"].OK || !replies["cancel"].OK || replies["duplicate"].Error == nil {
					t.Fatalf("request replies: %+v", replies)
				}
			})
		})
	}
}
