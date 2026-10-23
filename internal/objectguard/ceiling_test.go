package objectguard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func ceilingLimiter(t *testing.T, limit CeilingLimit) *Limiter {
	t.Helper()
	cfg := testConfig(0, 0)
	cfg.Ceiling = CeilingConfig{Limit: limit, Reconcile: time.Hour}
	return New(cfg)
}

func TestCeilingDefaultsToUnlimited(t *testing.T) {
	l := New(testConfig(10, 100))
	state := l.State().Ceiling
	if state.Enforced {
		t.Fatal("a limiter with no ceiling configured reports one as enforced")
	}
	l.Ceiling().record(1<<40, 1_000_000)
	if err := l.Allow(classPut); err != nil {
		t.Fatalf("an unlimited bucket refused a write after a terabyte: %v", err)
	}
}

func TestCeilingFreezesWritesOnBytesAndNamesTheLimit(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxBytes: 1000})
	l.Ceiling().record(1200, 3)

	err := l.Allow(classPut)
	if err == nil {
		t.Fatal("a bucket over its byte ceiling let a write through")
	}
	if !errors.Is(err, errCeilingFrozen) {
		t.Fatalf("refusal %v does not match ErrCeilingFrozen", err)
	}
	var ce *ceilingError
	if !errors.As(err, &ce) {
		t.Fatalf("refusal %v is not a *CeilingError", err)
	}
	if ce.Reason != ceilingBytes || ce.Limit != 1000 || ce.Observed != 1200 {
		t.Errorf("refusal reports %s %d/%d, want bytes 1000 measured at 1200", ce.Reason, ce.Observed, ce.Limit)
	}
	for _, want := range []string{"1000", "1200", "--max-bucket-bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal message %q does not name %q", err, want)
		}
	}
}

func TestCeilingFreezesOnObjectCount(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxObjects: 4})
	for range 4 {
		l.Ceiling().record(1, 1)
	}
	var ce *ceilingError
	if err := l.Allow(classPut); !errors.As(err, &ce) {
		t.Fatalf("a bucket at its object ceiling refused with %v, want a *CeilingError", err)
	}
	if ce.Reason != ceilingObjects {
		t.Errorf("freeze reason is %q, want objects", ce.Reason)
	}
}

func TestCeilingLeavesDeletesAndReadsAlone(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxBytes: 10})
	l.Ceiling().record(99, 1)

	for _, c := range []Class{classDelete, classGet, ClassList} {
		if err := l.Allow(c); err != nil {
			t.Errorf("a frozen bucket refused a %s request: %v", c, err)
		}
	}
}

func TestCeilingThawsWhenAMeasurementFallsBackUnder(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxBytes: 1000})
	c := l.Ceiling()
	c.record(2000, 5)
	if err := l.Allow(classPut); err == nil {
		t.Fatal("a bucket over its ceiling let a write through")
	}

	c.Observe(Usage{Bytes: 400, Objects: 2, ObservedAt: time.Now()})
	if c.isFrozen() {
		t.Fatal("a measurement under the ceiling left the bucket frozen")
	}
	if err := l.Allow(classPut); err != nil {
		t.Fatalf("a thawed bucket still refuses writes: %v", err)
	}

	c.Observe(Usage{Bytes: 5000, Objects: 9})
	if !c.isFrozen() {
		t.Fatal("a measurement back over the ceiling did not freeze writes again")
	}
	if got := c.State().Freezes; got != 2 {
		t.Errorf("freezes_total = %d after a freeze, a thaw and a refreeze, want 2", got)
	}
}

func TestCeilingStateReportsWarningBeforeFreezing(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxBytes: 1000, WarnBytes: 500})
	c := l.Ceiling()
	c.record(600, 1)

	state := c.State()
	if !state.Warning {
		t.Error("a bucket past its warning mark does not report a warning")
	}
	if state.Frozen {
		t.Error("a bucket past its warning mark but under its ceiling froze writes")
	}
	if err := l.Allow(classPut); err != nil {
		t.Errorf("a warning bucket refused a write: %v", err)
	}
}

func TestCeilingCountsRefusals(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxBytes: 1})
	l.Ceiling().record(2, 1)
	for range 3 {
		_ = l.Allow(classPut)
	}
	if got := l.State().Ceiling.Refused; got != 3 {
		t.Errorf("refused_total = %d after three refused writes, want 3", got)
	}
}

func TestRecordWriteCountsPutsAndGivesObjectsBackOnDelete(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxObjects: 100})
	l.recordWrite("PutObject", 512)
	l.recordWrite("PutObject", 512)
	l.recordWrite("DeleteObject", 0)
	l.recordWrite("GetObject", 4096)

	state := l.State().Ceiling
	if state.Bytes != 1024 {
		t.Errorf("bucket bytes = %d after two 512-byte writes and one read, want 1024", state.Bytes)
	}
	if state.Objects != 1 {
		t.Errorf("bucket objects = %d after two writes and one delete, want 1", state.Objects)
	}
}

func TestRecordWriteCountsOneMultipartUploadOnce(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxObjects: 100})
	l.recordWrite("CreateMultipartUpload", 0)
	for range 4 {
		l.recordWrite("UploadPart", 1<<20)
	}
	l.recordWrite("CompleteMultipartUpload", 0)

	state := l.State().Ceiling
	if state.Objects != 1 {
		t.Errorf("a four-part upload counted %d objects, want 1", state.Objects)
	}
	if state.Bytes != 4<<20 {
		t.Errorf("a four-part upload counted %d bytes, want %d", state.Bytes, 4<<20)
	}
}

func TestAbortedMultipartUploadIsAllowedWhileFrozenAndCountsNothing(t *testing.T) {
	l := ceilingLimiter(t, CeilingLimit{MaxBytes: 100})
	l.Ceiling().Observe(Usage{Bytes: 4096, Objects: 2})

	if got := classForOperation("AbortMultipartUpload"); got != classDelete {
		t.Errorf("AbortMultipartUpload bills as %q, want the delete class so a frozen bucket can be cleaned up", got)
	}
	if err := l.Allow(classForOperation("AbortMultipartUpload")); err != nil {
		t.Fatalf("a frozen bucket refused an aborted upload: %v", err)
	}
	before := l.State().Ceiling
	l.recordWrite("AbortMultipartUpload", 0)
	if after := l.State().Ceiling; after.Objects != before.Objects || after.Bytes != before.Bytes {
		t.Errorf("an abort changed the bucket total from %d/%d to %d/%d",
			before.Bytes, before.Objects, after.Bytes, after.Objects)
	}
}

func TestReconcileWithSkipsMeasuringAnUnlimitedBucket(t *testing.T) {
	c := NewCeiling(CeilingConfig{})
	calls := 0
	src := func(context.Context) (Usage, error) {
		calls++
		return Usage{Bytes: 1}, nil
	}
	if err := c.ReconcileWith(context.Background(), src); err != nil {
		t.Fatalf("ReconcileWith: %v", err)
	}
	if calls != 0 {
		t.Errorf("an unlimited bucket listed itself %d times, want 0", calls)
	}
}

func TestReconcileWithFoldsAMeasurementIn(t *testing.T) {
	c := NewCeiling(CeilingConfig{Limit: CeilingLimit{MaxBytes: 100}})
	src := func(context.Context) (Usage, error) {
		return Usage{Bytes: 250, Objects: 7}, nil
	}
	if err := c.ReconcileWith(context.Background(), src); err != nil {
		t.Fatalf("ReconcileWith: %v", err)
	}
	state := c.State()
	if state.Bytes != 250 || state.Objects != 7 {
		t.Errorf("measured bucket = %d bytes / %d objects, want 250/7", state.Bytes, state.Objects)
	}
	if !state.Frozen {
		t.Error("a measurement over the ceiling did not freeze writes")
	}
	if state.ReconciledAt.IsZero() {
		t.Error("a reconciled ceiling reports no reconciliation time")
	}
}

func TestReconcileWithReportsAFailedMeasurement(t *testing.T) {
	c := NewCeiling(CeilingConfig{Limit: CeilingLimit{MaxBytes: 100}})
	want := errors.New("bucket unreachable")
	err := c.ReconcileWith(context.Background(), func(context.Context) (Usage, error) {
		return Usage{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("ReconcileWith error = %v, want it to wrap %v", err, want)
	}
	if c.isFrozen() {
		t.Error("a failed measurement froze the bucket")
	}
}

func TestCeilingReconcileZeroMeansMeasureOnce(t *testing.T) {
	if got := NewCeiling(CeilingConfig{Reconcile: 0}).Reconcile(); got != 0 {
		t.Errorf("the ceiling reports %s, want the 0 it was configured with", got)
	}
}

func TestPartialMeasurementIsDiscardedAndReported(t *testing.T) {
	c := NewCeiling(CeilingConfig{
		Limit:     CeilingLimit{MaxBytes: 1000},
		Reconcile: time.Hour,
	})
	c.Observe(Usage{Bytes: 4096, Objects: 9})

	err := c.ReconcileWith(context.Background(), func(context.Context) (Usage, error) {
		return Usage{Bytes: 10, Objects: 1, Partial: true}, nil
	})
	if !errors.Is(err, errMeasurementPartial) {
		t.Fatalf("ReconcileWith error = %v, want it to match ErrMeasurementPartial", err)
	}
	state := c.State()
	if state.Bytes != 4096 {
		t.Errorf("a partial total of 10 bytes replaced the measured 4096: %d", state.Bytes)
	}
	if !state.Frozen {
		t.Error("a partial measurement thawed a store that is still over its ceiling")
	}
	if !state.Incomplete {
		t.Error("the ceiling does not report its measurement as incomplete")
	}

	if err := c.ReconcileWith(context.Background(), func(context.Context) (Usage, error) {
		return Usage{Bytes: 10, Objects: 1}, nil
	}); err != nil {
		t.Fatalf("ReconcileWith: %v", err)
	}
	if c.State().Incomplete {
		t.Error("a complete measurement left the incomplete mark standing")
	}
}

func TestFailedMeasurementMarksTheCeilingIncomplete(t *testing.T) {
	c := NewCeiling(CeilingConfig{
		Limit:     CeilingLimit{MaxBytes: 1000},
		Reconcile: time.Hour,
	})
	if err := c.ReconcileWith(context.Background(), func(context.Context) (Usage, error) {
		return Usage{}, errors.New("bucket unreachable")
	}); err == nil {
		t.Fatal("a failed measurement reported success")
	}
	if !c.State().Incomplete {
		t.Error("a failed measurement left the ceiling reporting a good total")
	}
}
