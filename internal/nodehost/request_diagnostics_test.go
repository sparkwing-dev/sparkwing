package nodehost

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestRequestDiagnosticsCannotBlockTheCaller(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	d := newRequestDiagnostics(t.Context())
	observe := d.observer(func(ctx context.Context, _ nodewire.FrameLog) error {
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
	observe(otelutil.OperationTiming{Operation: "nodehost.annotation", DurationMS: 2000})
	<-entered
	for range 1000 {
		observe(otelutil.OperationTiming{Operation: "nodehost.annotation", DurationMS: 2000})
	}
	if d.dropped.Load() == 0 {
		t.Error("full diagnostic queue did not report loss")
	}
	close(release)
	d.close()
}

func TestRequestDiagnosticsSurviveExecutionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	received := make(chan nodewire.FrameLog, 1)
	d := newRequestDiagnostics(ctx)
	observe := d.observer(func(delivery context.Context, frame nodewire.FrameLog) error {
		if delivery.Err() != nil {
			t.Error("diagnostic inherited execution cancellation")
		}
		received <- frame
		return nil
	})
	cancel()
	observe(otelutil.OperationTiming{Operation: "nodehost.annotation", DurationMS: 1000})
	frame := <-received
	if frame.Event != "request_timing" {
		t.Fatal("slow cancellation diagnostic disappeared")
	}
	d.close()
}

func TestRequestDiagnosticsDrainQueuedRecordsAtClose(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var mu sync.Mutex
	var delivered []string
	d := newRequestDiagnostics(t.Context())
	observe := d.observer(func(ctx context.Context, frame nodewire.FrameLog) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		mu.Lock()
		delivered = append(delivered, string(frame.Attrs))
		mu.Unlock()
		return nil
	})
	observe(otelutil.OperationTiming{Operation: "first"})
	<-entered
	for range 5 {
		observe(otelutil.OperationTiming{Operation: "queued"})
	}
	closed := make(chan struct{})
	go func() { d.close(); close(closed) }()
	<-d.stop
	close(release)
	<-closed
	<-d.done
	observe(otelutil.OperationTiming{Operation: "after close"})
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 6 {
		t.Fatalf("close delivered %d of 6 accepted records", len(delivered))
	}
	if d.dropped.Load() != 1 {
		t.Fatalf("record after close was not counted as dropped: %d", d.dropped.Load())
	}
}

func TestRequestDiagnosticsCloseIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		d := newRequestDiagnostics(t.Context())
		observe := d.observer(func(context.Context, nodewire.FrameLog) error {
			close(entered)
			<-release
			return nil
		})
		observe(otelutil.OperationTiming{Operation: "stuck"})
		<-entered
		started := time.Now()
		d.close()
		if waited := time.Since(started); waited != requestDiagnosticsDrain {
			t.Fatalf("close waited %v, want the %v drain bound", waited, requestDiagnosticsDrain)
		}
		close(release)
		<-d.done
	})
}

func TestRequestTimingWriterKeepsItsAttemptFence(t *testing.T) {
	records := &nativeLogRecords{objects: map[string][]byte{}}
	principal := capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitNode, Binding: capability.Binding{RunID: "run", NodeID: "job", Generation: 1}}}
	logs := &localLogs{run: "run", masker: secrets.NewMasker(), principal: principal, upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		records.view.Subject = subject
		return records, nil
	}}
	logs.bind(principal, 1)
	writer := logs.requestTimingWriter("run", "job")
	if writer == nil {
		t.Fatal("bound attempt has no timing writer")
	}
	<-logs.finish(t.Context(), "job")
	logs.bind(principal, 2)
	if err := logs.write(t.Context(), "run", "job", nodewire.FrameLog{Level: "info", Message: "successor output"}); err != nil {
		t.Fatal(err)
	}
	err := writer(t.Context(), nodewire.FrameLog{Level: "debug", Event: "request_timing"})
	if !errors.Is(err, logseg.ErrClosed) {
		t.Errorf("old timing writer entered successor: %v", err)
	}
	<-logs.finish(t.Context(), "job")
	var body bytes.Buffer
	if _, err := (logseg.CommittedReader{Records: records, Objects: records}).Read(t.Context(), runstate.Actor{}, blob.LogReadRequest{Subject: records.view.Subject}, &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), "successor output") || strings.Contains(body.String(), "host_request_timing") {
		t.Fatalf("successor log contaminated: %s", &body)
	}
}
