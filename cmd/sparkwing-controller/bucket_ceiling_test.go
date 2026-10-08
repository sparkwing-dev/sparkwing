package main

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func TestApplyBucketCeilingConfiguresTheSharedLimiter(t *testing.T) {
	if err := applyBucketCeiling(objectguard.CeilingConfig{
		Limit: objectguard.CeilingLimit{MaxBytes: 1 << 30},
	}); err != nil {
		t.Fatalf("applyBucketCeiling: %v", err)
	}
	limiter := objectguard.Shared()
	t.Cleanup(func() { limiter.Ceiling().Configure(objectguard.CeilingConfig{}) })
	if got := limiter.Ceiling().Reconcile(); got != 0 {
		t.Errorf("reconcile interval = %s, want the 0 that was configured", got)
	}
}

func TestApplyBucketCeilingRefusesANegativeBound(t *testing.T) {
	err := applyBucketCeiling(objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: -1},
		Reconcile: time.Hour,
	})
	if err == nil {
		t.Fatal("a negative ceiling was accepted")
	}
}
