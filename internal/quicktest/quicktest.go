// Package quicktest sizes testing/quick properties for go test -short.
package quicktest

import (
	"math/rand"
	"testing"
	"testing/quick"
)

const shortCount = 3

// Config returns full unchanged outside -short. Under -short it keeps full's
// value generator but checks shortCount cases from a fixed seed, so every
// boundary a property covers still runs, deterministically, in the short class.
func Config(full *quick.Config) *quick.Config {
	if !testing.Short() {
		return full
	}
	short := quick.Config{MaxCount: shortCount, Rand: rand.New(rand.NewSource(1))} // #nosec G404 -- a reproducible test seed, not a secret
	if full != nil {
		short.Values = full.Values
	}
	return &short
}
