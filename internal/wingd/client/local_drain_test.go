package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLocalStopPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Stop(ctx, Options{Home: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop cancellation=%v", err)
	}
}

func TestLocalDrainRequiresAcknowledgement(t *testing.T) {
	for _, reply := range []wingwire.Message{&wingwire.DrainAck{}, &wingwire.DrainAck{HoldersRemaining: -1}, &wingwire.QueueState{}, &wingwire.Unsupported{Type: string(wingwire.TypeDrainRequest)}} {
		peer, server := net.Pipe()
		done := localCommandPeer(t, server, func(hello *wingwire.Hello, request wingwire.Message) wingwire.Message {
			if hello.HealthProbe {
				t.Error("drain used a health probe")
			}
			if _, ok := request.(*wingwire.DrainRequest); !ok {
				t.Errorf("expected drain, got %T", request)
			}
			return reply
		})
		err := exchangeLocalDrain(t.Context(), peer, "fixture")
		peer.Close()
		<-done
		ack, valid := reply.(*wingwire.DrainAck)
		valid = valid && ack.HoldersRemaining >= 0
		if (err == nil) != valid {
			t.Fatalf("reply=%T err=%v", reply, err)
		}
	}
}

func TestLocalDrainCancellationInterruptsHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, server := net.Pipe()
		defer peer.Close()
		defer server.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- exchangeLocalDrain(ctx, peer, "fixture") }()
		if _, err := newFrameReader(server).read(); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked drain: %v", err)
		}
	})
}
