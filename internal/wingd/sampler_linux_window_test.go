//go:build linux

package wingd

import (
	"os"
	"testing"
	"time"
)

func TestLinuxSampleOwned_RecordsTheListingBoundAndTheReadBoundApart(t *testing.T) {
	base := time.Now()
	readings := 0
	clock := func() time.Time {
		readings++
		return base.Add(time.Duration(readings) * time.Second)
	}

	sampler := newOwnedCPUSampler()
	if _, ok := sampler.sampleOwnedFrom(clock, []OwnedRoot{{PID: os.Getpid(), HeldSince: base}}, 8); !ok {
		t.Fatal("the scan could not list this machine's processes, so it never reached the bounds this test is about")
	}

	if readings != 2 {
		t.Fatalf("the scan read the clock %d times; want two, one bound either side of the listing", readings)
	}
	if got, want := sampler.seenSince, base.Add(time.Second); !got.Equal(want) {
		t.Errorf("the scan recorded %v as when it began listing; want the reading taken before the listing, %v: the later reading is when the listing finished, and dating against it refuses a credit the tree earned",
			got, want)
	}
	if got, want := sampler.lastAt, base.Add(2*time.Second); !got.Equal(want) {
		t.Errorf("the scan recorded %v as when it read; want the reading taken after the listing, %v: an earlier one shortens the window every first-sight credit is divided by and inflates each of them",
			got, want)
	}
}
