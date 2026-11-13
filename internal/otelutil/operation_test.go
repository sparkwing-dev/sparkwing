package otelutil

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestOperationDiagnosticThresholdAndPhaseBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var records []OperationTiming
		ctx := WithTimingObserver(t.Context(), func(timing OperationTiming) { records = append(records, timing) })
		_, fast := StartOperation(ctx, "fast")
		time.Sleep(time.Second - time.Nanosecond)
		fast.Finish(nil)
		if len(records) != 0 {
			t.Fatal("fast operation emitted a diagnostic")
		}
		_, slow := StartOperation(ctx, "slow")
		for range 140 {
			slow.Phase("fixed.phase")
		}
		time.Sleep(time.Second)
		slow.Finish(errors.New("private-error-token"))
		if len(records) != 1 || records[0].Success || records[0].DurationMS != 1000 || len(records[0].Phases) != 128 || records[0].DroppedPhases != 12 {
			t.Fatalf("threshold or bound changed: %+v", records)
		}
	})
}
