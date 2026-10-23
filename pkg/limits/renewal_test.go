package limits

import (
	"math"
	"testing"
	"time"
)

func TestRenewalBoundaries(t *testing.T) {
	for _, tc := range []struct {
		every, ttl time.Duration
		valid      bool
	}{
		{time.Second, 3 * time.Second, true}, {time.Second, 2 * time.Second, false},
		{time.Second, 2*time.Second + 1, true}, {0, time.Minute, false}, {-1, time.Minute, false}, {1, 0, false},
		{time.Duration(math.MaxInt64), time.Duration(math.MaxInt64), false},
		{time.Duration(math.MaxInt64 / 3), time.Duration(math.MaxInt64), true},
	} {
		if err := CheckRenewal(tc.every, tc.ttl); (err == nil) != tc.valid {
			t.Errorf("CheckRenewal(%v,%v)=%v", tc.every, tc.ttl, err)
		}
	}
	if got := Renew(time.Minute); got != 20*time.Second {
		t.Fatalf("Renew = %v", got)
	}
}
