//go:build race

package orchestrator_test

// The race detector slows the orchestrator's dispatch paths several-fold, so
// a wall-clock budget written for a plain build fails on a healthy box.
const raceBudgetScale = 3
