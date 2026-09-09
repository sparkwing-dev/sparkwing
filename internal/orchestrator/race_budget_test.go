package orchestrator_test

import "time"

// timingBudget widens a wall-clock assertion for the race build while keeping
// it tight enough to catch the multi-second stalls the assertion guards.
func timingBudget(d time.Duration) time.Duration { return d * raceBudgetScale }
