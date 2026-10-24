package main

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func TestValidateBucketCeilingAcceptsZeroBounds(t *testing.T) {
	if err := validateBucketCeiling(objectguard.CeilingConfig{}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBucketCeilingRefusesANegativeBound(t *testing.T) {
	err := validateBucketCeiling(objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: -1},
		Reconcile: time.Hour,
	})
	if err == nil {
		t.Fatal("a negative ceiling was accepted")
	}
}
