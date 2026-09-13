//go:build race

package store_test

// safety: the race detector slows the claim scan several-fold, so a
// wall-clock budget written for a plain build fails on a healthy box.
const raceBudgetScale = 4
