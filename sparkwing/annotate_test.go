package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestAnnotate_EmitsStructuredRecord(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{name: "plain", msg: "processed 1234 records · 12 failed"},
		{name: "empty", msg: ""},
		{name: "unicode", msg: "imported  · 99% coverage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingEmitter{}
			ctx := sparkwing.WithLogger(context.Background(), rec)
			ctx = sparkwing.WithNode(ctx, "ingest")
			sparkwing.Annotate(ctx, tc.msg)

			if len(rec.records) != 1 {
				t.Fatalf("got %d records, want 1", len(rec.records))
			}
			r := rec.records[0]
			if r.Event != sparkwing.EventNodeAnnotation {
				t.Errorf("Event = %q, want %q", r.Event, sparkwing.EventNodeAnnotation)
			}
			if r.Msg != tc.msg {
				t.Errorf("Msg = %q, want %q", r.Msg, tc.msg)
			}
			if got, _ := r.Attrs["message"].(string); got != tc.msg {
				t.Errorf("Attrs[message] = %q, want %q", got, tc.msg)
			}
			if r.JobID != "ingest" {
				t.Errorf("JobID = %q, want %q", r.JobID, "ingest")
			}
			if r.TS.IsZero() {
				t.Error("TS should be set")
			}
		})
	}
}

func TestAnnotate_NoLogger_NoPanic(t *testing.T) {
	sparkwing.Annotate(context.Background(), "should be a no-op")
}

func TestAnnotate_MultipleCallsAccumulate(t *testing.T) {
	rec := &recordingEmitter{}
	ctx := sparkwing.WithLogger(context.Background(), rec)
	ctx = sparkwing.WithNode(ctx, "n")
	sparkwing.Annotate(ctx, "first")
	sparkwing.Annotate(ctx, "second")
	sparkwing.Annotate(ctx, "third")

	if len(rec.records) != 3 {
		t.Fatalf("got %d records, want 3", len(rec.records))
	}
	wantMsgs := []string{"first", "second", "third"}
	for i, want := range wantMsgs {
		if rec.records[i].Msg != want {
			t.Errorf("records[%d].Msg = %q, want %q", i, rec.records[i].Msg, want)
		}
		if rec.records[i].Event != sparkwing.EventNodeAnnotation {
			t.Errorf("records[%d].Event = %q, want %q", i, rec.records[i].Event, sparkwing.EventNodeAnnotation)
		}
	}
}
