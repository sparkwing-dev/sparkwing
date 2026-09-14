package main

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func TestApplyBucketCeilingAcceptsTheEnvironmentItReads(t *testing.T) {
	cfg, err := objectguard.ConfigFromEnv(func(name string) string {
		if name == objectguard.EnvBucketReconcile {
			return "0"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	cfg.Ceiling.Limit.MaxBytes = 1 << 30
	if err := applyBucketCeiling(cfg.Ceiling); err != nil {
		t.Fatalf("the controller refused the ceiling its own environment produced: %v", err)
	}
	limiter, err := objectguard.Shared()
	if err != nil {
		t.Fatalf("shared limiter: %v", err)
	}
	t.Cleanup(func() { limiter.Ceiling().Configure(objectguard.CeilingConfig{}) })
	if got := limiter.Ceiling().Reconcile(); got != 0 {
		t.Errorf("reconcile interval = %s, want the 0 the environment asked for", got)
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
