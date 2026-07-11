//go:build !linux && !darwin

package nodemetrics

// processRSS reports not-sampled where no per-process RSS source is wired;
// the sampler falls back to the Go runtime's system reservation.
func processRSS() (int64, bool) { return 0, false }
