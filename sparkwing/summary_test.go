package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestSummary_EmitsStructuredRecord(t *testing.T) {
	cases := []struct {
		name string
		md   string
	}{
		{name: "plain", md: "## Deployed\n- version: v1.2.3\n- replicas: 3"},
		{name: "empty", md: ""},
		{name: "unicode", md: "##  Done\n- coverage **99%**"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingEmitter{}
			ctx := sparkwing.WithLogger(context.Background(), rec)
			ctx = sparkwing.WithNode(ctx, "deploy")
			sparkwing.Summary(ctx, tc.md)

			if len(rec.records) != 1 {
				t.Fatalf("got %d records, want 1", len(rec.records))
			}
			r := rec.records[0]
			if r.Event != sparkwing.EventNodeSummary {
				t.Errorf("Event = %q, want %q", r.Event, sparkwing.EventNodeSummary)
			}
			if r.Msg != tc.md {
				t.Errorf("Msg = %q, want %q", r.Msg, tc.md)
			}
			if got, _ := r.Attrs["markdown"].(string); got != tc.md {
				t.Errorf("Attrs[markdown] = %q, want %q", got, tc.md)
			}
			if r.JobID != "deploy" {
				t.Errorf("JobID = %q, want %q", r.JobID, "deploy")
			}
			if r.TS.IsZero() {
				t.Error("TS should be set")
			}
		})
	}
}

func TestSummary_NoLogger_NoPanic(t *testing.T) {
	sparkwing.Summary(context.Background(), "## should be a no-op")
}

func TestSummary_CarriesStepFromContext(t *testing.T) {
	rec := &recordingEmitter{}
	ctx := sparkwing.WithLogger(context.Background(), rec)
	ctx = sparkwing.WithNode(ctx, "deploy")
	ctx = sparkwing.WithStep(ctx, "rollout")
	sparkwing.Summary(ctx, "## Rollout complete")

	if len(rec.records) != 1 {
		t.Fatalf("got %d records, want 1", len(rec.records))
	}
	r := rec.records[0]
	if r.Step != "rollout" {
		t.Errorf("Step = %q, want %q", r.Step, "rollout")
	}
	if r.JobID != "deploy" {
		t.Errorf("JobID = %q, want %q", r.JobID, "deploy")
	}
}
