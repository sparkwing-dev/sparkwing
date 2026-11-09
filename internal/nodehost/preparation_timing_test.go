package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
)

func TestOwnerPreparationTimesPlanningBeforeAcceptance(t *testing.T) {
	for _, outcome := range []string{"success", "resume", "describe failure", "plan failure"} {
		t.Run(outcome, func(t *testing.T) {
			f, host, input := newOwnerFixture(t, outcome == "resume")
			var receipts []source.Timing
			host.Source.Timing = func(_ context.Context, receipt source.Timing) {
				for _, action := range f.trace {
					if action == "accept" {
						t.Fatal("timing arrived after the planning log's writer fence ended")
					}
				}
				receipts = append(receipts, receipt)
			}
			prepare := host.Prepare
			host.Prepare = func(ctx context.Context, input RunInput, entry *source.Entry) (OwnerPlanning, error) {
				if outcome == "describe failure" {
					return OwnerPlanning{}, errors.New("private describe diagnostic")
				}
				p, err := prepare(ctx, input, entry)
				if outcome == "plan failure" {
					p.Plan = func(context.Context) (json.RawMessage, error) { return nil, errors.New("private plan diagnostic") }
				}
				return p, err
			}
			_, err := host.Run(t.Context(), input)
			failed := outcome == "describe failure" || outcome == "plan failure"
			if (err != nil) != failed {
				t.Fatalf("unexpected outcome: %v", err)
			}
			var stages []string
			for _, receipt := range receipts {
				stages = append(stages, receipt.Stage)
				if receipt.StartedAt.IsZero() || receipt.DurationMS < 0 {
					t.Fatalf("invalid receipt: %+v", receipt)
				}
			}
			want := []string{"describe", "plan"}
			if outcome == "resume" {
				want = nil
			} else if outcome == "describe failure" {
				want = []string{"describe"}
			}
			if !reflect.DeepEqual(stages, want) || len(receipts) > 0 && receipts[len(receipts)-1].Success == failed {
				t.Fatalf("receipts=%+v, want stages %v, failed=%v", receipts, want, failed)
			}
		})
	}
}

func TestPreparationTimingSurvivesWithheldSDKOutputInCommittedRunLogs(t *testing.T) {
	cfg, _ := nativeConfigFixture(t)
	cfg.Principal.Capability.Unit = capability.UnitPlan
	cfg.Refresh = func(context.Context) (capability.Principal, error) { return cfg.Principal, nil }
	cfg.Storage = func(context.Context, capability.Principal) (StorageBinding, error) {
		data := &nativeStorageFixture{}
		return StorageBinding{Bytes: data, Records: data, Authority: blob.Authority{CapabilityRef: cfg.Principal.Capability.Prefix}}, nil
	}
	records := &nativeLogRecords{objects: map[string][]byte{}}
	cfg.Logs = func(_ context.Context, p capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		if p.Capability.Unit != capability.UnitPlan || subject.RunID != "run" || subject.Kind != blob.LogOwnerUnit {
			t.Fatalf("timing lost owner authority: %+v %+v", p, subject)
		}
		records.view.Subject = subject
		return records, nil
	}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
		return &processListener{closed: make(chan struct{})}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.Close() })
	owner, _, _, err := NewNativeHost(services.Config)
	if err != nil {
		t.Fatal(err)
	}
	logs := planningLogs{masker: services.Config.Masks.masker, sink: services.Config.PlanningLog}
	if err := logs.text(t.Context(), "info", "private compiler diagnostic", true); err != nil {
		t.Fatal(err)
	}
	var want []source.Timing
	observe := owner.Source.Timing
	if observe == nil {
		t.Fatal("native host omitted the persisted preparation timing adapter")
	}
	owner.Source.Timing = func(ctx context.Context, receipt source.Timing) {
		want = append(want, receipt)
		observe(ctx, receipt)
	}
	for _, stage := range []string{"checkout", "sdk_install", "publish", "describe", "plan"} {
		owner.Source.RecordTiming(t.Context(), stage, time.Now(), true, false)
	}
	if err := logs.finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := services.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	read, err := (logseg.CommittedReader{Records: records, Objects: records}).Read(t.Context(), runstate.Actor{}, blob.LogReadRequest{Subject: records.view.Subject}, &body)
	if err != nil || !read.Complete || bytes.Contains(body.Bytes(), []byte("private compiler diagnostic")) {
		t.Fatalf("timing log not safely persisted: %s %v", &body, err)
	}
	var got []source.Timing
	for _, line := range bytes.Split(bytes.TrimSpace(body.Bytes()), []byte("\n")) {
		var frame struct {
			Event string        `json:"event"`
			Attrs source.Timing `json:"attrs"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Event == "preparation" {
			got = append(got, frame.Attrs)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("committed receipts=%+v, want %+v; log=%s", got, want, &body)
	}
}
