//go:build race

package orchestrator_test

// safety: the race detector slows RunLocal several-fold, so a wall-clock
// budget written for a plain build fails on a loaded box.
const raceBudgetScale = 4
