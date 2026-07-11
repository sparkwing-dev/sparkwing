// Package chaos is the adversarial acceptance suite for local admission.
// It runs the real system -- a real [wingd] daemon over real unix sockets,
// real crashdummy run processes, and real SIGKILLs -- inside an isolated
// SPARKWING_HOME temp directory, and asserts the admission invariants after
// every event and again once the system quiesces.
//
// # What it exercises
//
// A seeded schedule interleaves: spawning synthetic runs with randomized
// host and semaphore costs and at-limit policies; SIGKILLing holders and
// waiters; SIGKILLing the daemon mid-grant so live clients must re-elect a
// successor and reattach; version-takeover cycles that drain a running
// daemon and bring up a newer one; wedged holders that sit idle while
// waiters queue behind them; malformed wire frames and connect/disconnect
// storms against the socket; and concurrent read-only CLI verbs.
//
// # Oracles
//
// After each event the harness cross-checks the daemon's [wingwire.QueueState]:
// granted cost never exceeds capacity, holders and waiters stay disjoint
// and duplicate-free (ledger truth); the set of granted leases matches the
// set of live crashdummy processes within a settle bound (OS truth); and
// once injection stops the system returns to zero leases, zero waiters, and
// an idle-exited daemon with no human intervention (convergence). The
// daemon's own ledger panics on over-admission, so a panic in its log is a
// caught bug.
//
// # Determinism
//
// The seed governs the scenario schedule, not OS timing: process starts and
// signal delivery are not reproducible to the millisecond, so oracles use
// settle bounds -- they assert convergence within a window, never
// instantaneous state. A failing run prints its seed and journal path;
// re-running with the same seed replays the schedule.
//
// # Modes
//
// [TestChaos_CI] runs a bounded (~35s active) scenario as part of `go test`;
// it is skipped under -short so the fast unit loop is not slowed, and it is
// wired into the repo's pre-push gate. [TestChaos_Soak] runs only when
// SPARKWING_CHAOS_SOAK is set to a duration and injects faults more
// aggressively for nightly or manual runs:
//
//	SPARKWING_CHAOS_SOAK=30m go test -run TestChaos_Soak ./internal/chaos
//
// Both accept SPARKWING_CHAOS_SEED to pin the schedule for replay; when
// unset the harness derives a seed from the clock and prints it at start.
package chaos
