package nodehost

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestRequestDiagnosticsCannotBlockTheCaller(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	d := newRequestDiagnostics(t.Context(), func(ctx context.Context, _, _ string, _ nodewire.FrameLog) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	defer d.cancel()
	d.observe("run", "job", otelutil.OperationTiming{Operation: "nodehost.annotation", DurationMS: 2000})
	<-entered
	for range 1000 {
		d.observe("run", "job", otelutil.OperationTiming{Operation: "nodehost.annotation", DurationMS: 2000})
	}
	if d.dropped.Load() == 0 {
		t.Error("full diagnostic queue did not report loss")
	}
	d.cancel()
	close(release)
	<-d.done
}

func TestRequestDiagnosticsSurviveExecutionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	received := make(chan nodewire.FrameLog, 1)
	d := newRequestDiagnostics(ctx, func(delivery context.Context, _, _ string, frame nodewire.FrameLog) error {
		if delivery.Err() != nil {
			t.Error("diagnostic inherited execution cancellation")
		}
		received <- frame
		return nil
	})
	cancel()
	d.observe("run", "job", otelutil.OperationTiming{Operation: "nodehost.annotation", DurationMS: 1000})
	frame := <-received
	if frame.Event != "request_timing" {
		t.Fatal("slow cancellation diagnostic disappeared")
	}
	d.cancel()
	<-d.done
}
