package store_test

import (
	"os"
	"strconv"
	"testing"
)

func fuzzSeeds(t *testing.T) []int64 {
	t.Helper()
	if env := os.Getenv("SPARKWING_CONC_FUZZ_SEED"); env != "" {
		seed, err := strconv.ParseInt(env, 10, 64)
		if err != nil {
			t.Fatalf("SPARKWING_CONC_FUZZ_SEED=%q: %v", env, err)
		}
		return []int64{seed}
	}
	if testing.Short() {
		return []int64{1}
	}
	return []int64{1, 2, 3, 4, 5}
}
