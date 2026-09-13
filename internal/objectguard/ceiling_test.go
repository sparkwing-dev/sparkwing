package objectguard_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func ceilingLimiter(t *testing.T, limit objectguard.CeilingLimit) *objectguard.Limiter {
	t.Helper()
	cfg := testConfig(0, 0)
	cfg.Ceiling = objectguard.CeilingConfig{Limit: limit}
	return objectguard.New(cfg)
}

func TestCeilingDefaultsToUnlimited(t *testing.T) {
	l := objectguard.New(testConfig(10, 100))
	state := l.State().Ceiling
	if state.Enforced {
		t.Fatal("a limiter with no ceiling configured reports one as enforced")
	}
	l.Ceiling().Record(1<<40, 1_000_000)
	if err := l.Allow(objectguard.ClassPut); err != nil {
		t.Fatalf("an unlimited bucket refused a write after a terabyte: %v", err)
	}
}

func TestCeilingFreezesWritesOnBytesAndNamesTheLimit(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxBytes: 1000})
	l.Ceiling().Record(1200, 3)

	err := l.Allow(objectguard.ClassPut)
	if err == nil {
		t.Fatal("a bucket over its byte ceiling let a write through")
	}
	if !errors.Is(err, objectguard.ErrCeilingFrozen) {
		t.Fatalf("refusal %v does not match ErrCeilingFrozen", err)
	}
	var ce *objectguard.CeilingError
	if !errors.As(err, &ce) {
		t.Fatalf("refusal %v is not a *CeilingError", err)
	}
	if ce.Reason != objectguard.CeilingBytes || ce.Limit != 1000 || ce.Observed != 1200 {
		t.Errorf("refusal reports %s %d/%d, want bytes 1000 measured at 1200", ce.Reason, ce.Observed, ce.Limit)
	}
	for _, want := range []string{"1000", "1200", "SPARKWING_OBJECT_STORE_MAX_BUCKET_BYTES", "reset-breaker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal message %q does not name %q", err, want)
		}
	}
}

func TestCeilingFreezesOnObjectCount(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxObjects: 4})
	for range 4 {
		l.Ceiling().Record(1, 1)
	}
	var ce *objectguard.CeilingError
	if err := l.Allow(objectguard.ClassPut); !errors.As(err, &ce) {
		t.Fatalf("a bucket at its object ceiling refused with %v, want a *CeilingError", err)
	}
	if ce.Reason != objectguard.CeilingObjects {
		t.Errorf("freeze reason is %q, want objects", ce.Reason)
	}
}

func TestCeilingLeavesDeletesAndReadsAlone(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxBytes: 10})
	l.Ceiling().Record(99, 1)

	for _, c := range []objectguard.Class{objectguard.ClassDelete, objectguard.ClassGet, objectguard.ClassList} {
		if err := l.Allow(c); err != nil {
			t.Errorf("a frozen bucket refused a %s request: %v", c, err)
		}
	}
}

func TestCeilingThawsWhenAMeasurementFallsBackUnder(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxBytes: 1000})
	c := l.Ceiling()
	c.Record(2000, 5)
	if err := l.Allow(objectguard.ClassPut); err == nil {
		t.Fatal("a bucket over its ceiling let a write through")
	}

	c.Observe(objectguard.Usage{Bytes: 400, Objects: 2, ObservedAt: time.Now()})
	if c.Frozen() {
		t.Fatal("a measurement under the ceiling left the bucket frozen")
	}
	if err := l.Allow(objectguard.ClassPut); err != nil {
		t.Fatalf("a thawed bucket still refuses writes: %v", err)
	}

	c.Observe(objectguard.Usage{Bytes: 5000, Objects: 9})
	if !c.Frozen() {
		t.Fatal("a measurement back over the ceiling did not freeze writes again")
	}
	if got := c.State().Freezes; got != 2 {
		t.Errorf("freezes_total = %d after a freeze, a thaw and a refreeze, want 2", got)
	}
}

func TestCeilingThawIsManualAndRefreezesOnTheNextMeasurement(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxObjects: 2})
	c := l.Ceiling()
	c.Observe(objectguard.Usage{Bytes: 1, Objects: 5})

	if !c.Thaw() {
		t.Fatal("Thaw reports nothing was frozen on a frozen bucket")
	}
	if err := l.Allow(objectguard.ClassPut); err != nil {
		t.Fatalf("a manually thawed bucket still refuses writes: %v", err)
	}
	if c.Thaw() {
		t.Error("Thaw reports a freeze cleared on a bucket that was already thawed")
	}

	c.Observe(objectguard.Usage{Bytes: 1, Objects: 5})
	if !c.Frozen() {
		t.Fatal("the measurement after a manual thaw left a bucket over its ceiling writable")
	}
}

func TestCeilingStateReportsWarningBeforeFreezing(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxBytes: 1000, WarnBytes: 500})
	c := l.Ceiling()
	c.Record(600, 1)

	state := c.State()
	if !state.Warning {
		t.Error("a bucket past its warning mark does not report a warning")
	}
	if state.Frozen {
		t.Error("a bucket past its warning mark but under its ceiling froze writes")
	}
	if err := l.Allow(objectguard.ClassPut); err != nil {
		t.Errorf("a warning bucket refused a write: %v", err)
	}
}

func TestCeilingCountsRefusals(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxBytes: 1})
	l.Ceiling().Record(2, 1)
	for range 3 {
		_ = l.Allow(objectguard.ClassPut)
	}
	if got := l.State().Ceiling.Refused; got != 3 {
		t.Errorf("refused_total = %d after three refused writes, want 3", got)
	}
}

func TestRecordWriteCountsPutsAndGivesObjectsBackOnDelete(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxObjects: 100})
	l.RecordWrite("PutObject", 512)
	l.RecordWrite("PutObject", 512)
	l.RecordWrite("DeleteObject", 0)
	l.RecordWrite("GetObject", 4096)

	state := l.State().Ceiling
	if state.Bytes != 1024 {
		t.Errorf("bucket bytes = %d after two 512-byte writes and one read, want 1024", state.Bytes)
	}
	if state.Objects != 1 {
		t.Errorf("bucket objects = %d after two writes and one delete, want 1", state.Objects)
	}
}

func TestRecordWriteCountsOneMultipartUploadOnce(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxObjects: 100})
	l.RecordWrite("CreateMultipartUpload", 0)
	for range 4 {
		l.RecordWrite("UploadPart", 1<<20)
	}
	l.RecordWrite("CompleteMultipartUpload", 0)

	state := l.State().Ceiling
	if state.Objects != 1 {
		t.Errorf("a four-part upload counted %d objects, want 1", state.Objects)
	}
	if state.Bytes != 4<<20 {
		t.Errorf("a four-part upload counted %d bytes, want %d", state.Bytes, 4<<20)
	}
}

func TestAbortedMultipartUploadIsAllowedWhileFrozenAndCountsNothing(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxBytes: 100})
	l.Ceiling().Observe(objectguard.Usage{Bytes: 4096, Objects: 2})

	if got := objectguard.ClassForOperation("AbortMultipartUpload"); got != objectguard.ClassDelete {
		t.Errorf("AbortMultipartUpload bills as %q, want the delete class so a frozen bucket can be cleaned up", got)
	}
	if err := l.Allow(objectguard.ClassForOperation("AbortMultipartUpload")); err != nil {
		t.Fatalf("a frozen bucket refused an aborted upload: %v", err)
	}
	before := l.State().Ceiling
	l.RecordWrite("AbortMultipartUpload", 0)
	if after := l.State().Ceiling; after.Objects != before.Objects || after.Bytes != before.Bytes {
		t.Errorf("an abort changed the bucket total from %d/%d to %d/%d",
			before.Bytes, before.Objects, after.Bytes, after.Objects)
	}
}

func TestThawHoldsUntilTheNextMeasurement(t *testing.T) {
	l := ceilingLimiter(t, objectguard.CeilingLimit{MaxBytes: 1000})
	c := l.Ceiling()
	c.Observe(objectguard.Usage{Bytes: 4096, Objects: 2})
	if !c.Thaw() {
		t.Fatal("Thaw reports nothing was frozen on a frozen bucket")
	}

	for range 5 {
		if err := l.Allow(objectguard.ClassPut); err != nil {
			t.Fatalf("a thawed bucket refused a write: %v", err)
		}
		l.RecordWrite("PutObject", 4096)
	}
	if !c.State().Thawed {
		t.Error("the ceiling stopped reporting the thaw while it was still holding")
	}

	c.Observe(objectguard.Usage{Bytes: 24576, Objects: 7})
	if !c.Frozen() {
		t.Fatal("the measurement after a thaw left a bucket over its ceiling writable")
	}
	if c.State().Thawed {
		t.Error("the ceiling still reports a thaw after the measurement that ended it")
	}
}

func TestReconcileWithSkipsMeasuringAnUnlimitedBucket(t *testing.T) {
	c := objectguard.NewCeiling(objectguard.CeilingConfig{})
	calls := 0
	src := func(context.Context) (objectguard.Usage, error) {
		calls++
		return objectguard.Usage{Bytes: 1}, nil
	}
	if err := c.ReconcileWith(context.Background(), src); err != nil {
		t.Fatalf("ReconcileWith: %v", err)
	}
	if calls != 0 {
		t.Errorf("an unlimited bucket listed itself %d times, want 0", calls)
	}
}

func TestReconcileWithFoldsAMeasurementIn(t *testing.T) {
	c := objectguard.NewCeiling(objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{MaxBytes: 100}})
	src := func(context.Context) (objectguard.Usage, error) {
		return objectguard.Usage{Bytes: 250, Objects: 7}, nil
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
	c := objectguard.NewCeiling(objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{MaxBytes: 100}})
	want := errors.New("bucket unreachable")
	err := c.ReconcileWith(context.Background(), func(context.Context) (objectguard.Usage, error) {
		return objectguard.Usage{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("ReconcileWith error = %v, want it to wrap %v", err, want)
	}
	if c.Frozen() {
		t.Error("a failed measurement froze the bucket")
	}
}

func TestCeilingConfigFromEnv(t *testing.T) {
	cfg, err := objectguard.ConfigFromEnv(envFrom(map[string]string{
		"SPARKWING_OBJECT_STORE_MAX_BUCKET_BYTES":   "4096",
		"SPARKWING_OBJECT_STORE_WARN_BUCKET_BYTES":  "2048",
		"SPARKWING_OBJECT_STORE_MAX_BUCKET_OBJECTS": "9",
		"SPARKWING_OBJECT_STORE_BUCKET_RECONCILE":   "15m",
	}))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	want := objectguard.CeilingLimit{MaxBytes: 4096, MaxObjects: 9, WarnBytes: 2048}
	if cfg.Ceiling.Limit != want {
		t.Errorf("ceiling limit = %+v, want %+v", cfg.Ceiling.Limit, want)
	}
	if cfg.Ceiling.Reconcile != 15*time.Minute {
		t.Errorf("reconcile interval = %s, want 15m", cfg.Ceiling.Reconcile)
	}
}

func TestCeilingReconcileEnvZeroMeansMeasureOnce(t *testing.T) {
	cfg, err := objectguard.ConfigFromEnv(envFrom(map[string]string{
		"SPARKWING_OBJECT_STORE_BUCKET_RECONCILE": "0",
	}))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Ceiling.Reconcile != 0 {
		t.Errorf("reconcile interval = %s for an environment asking for one measurement, want 0", cfg.Ceiling.Reconcile)
	}
	if got := objectguard.NewCeiling(cfg.Ceiling).Reconcile(); got != 0 {
		t.Errorf("the ceiling reports %s, want the 0 it was configured with", got)
	}
}

func TestCeilingConfigFromEnvDefaultsToUnlimitedAndHourly(t *testing.T) {
	cfg, err := objectguard.ConfigFromEnv(envFrom(nil))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Ceiling.Limit.Enforced() {
		t.Errorf("an unconfigured bucket carries a ceiling: %+v", cfg.Ceiling.Limit)
	}
	if got := objectguard.NewCeiling(cfg.Ceiling).Reconcile(); got != time.Hour {
		t.Errorf("default reconcile interval = %s, want 1h", got)
	}
}

func TestCeilingConfigFromEnvRejectsAMalformedCeiling(t *testing.T) {
	for name, value := range map[string]string{
		"SPARKWING_OBJECT_STORE_MAX_BUCKET_BYTES": "10GB",
		"SPARKWING_OBJECT_STORE_BUCKET_RECONCILE": "hourly",
	} {
		_, err := objectguard.ConfigFromEnv(envFrom(map[string]string{name: value}))
		if err == nil {
			t.Errorf("%s=%q was accepted", name, value)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}
