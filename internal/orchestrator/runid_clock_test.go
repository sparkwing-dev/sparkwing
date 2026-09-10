package orchestrator

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestLocalRunIDsRemainUniqueAtSameInstant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		instant := time.Now()
		first, second := localNewRunID(), localNewRunID()
		if !time.Now().Equal(instant) {
			t.Fatal("clock advanced")
		}
		if first == second {
			t.Fatalf("same-instant runs share id %q", first)
		}
	})
}
