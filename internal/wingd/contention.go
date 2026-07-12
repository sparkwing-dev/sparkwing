package wingd

import (
	"fmt"
	"time"
)

// Contention-detector thresholds. All conservative on purpose: a holder
// is flagged throttled only when it is well past its own worst measured
// run and the host was saturated for much of the run, so a merely-slow or
// legitimately-long run is never mistaken for contention.
const (
	// contentionMinSamples is the fewest duration samples a run's profile
	// must have before its p99 is trusted as a baseline. Below it the run
	// is treated as unprofiled and never flagged.
	contentionMinSamples = 3
	// contentionSlowFactor is how far past its measured p99 a holder's
	// elapsed time must run before the duration signal fires (1.25 = 25%
	// beyond the 99th percentile).
	contentionSlowFactor = 1.25
	// contentionSaturationFraction is the share of the grantable headroom
	// (host capacity minus reserve) that external, non-sparkwing load must
	// occupy for a host sample to count as saturated.
	contentionSaturationFraction = 0.90
	// contentionSaturatedShare is the fraction of a holder's sampled time
	// that must have been saturated before the host-pressure signal fires.
	contentionSaturatedShare = 0.50
	// contentionMinSamplesObserved is the fewest host samples a holder must
	// have accrued before its saturation share is meaningful, so a run that
	// has barely started is never flagged.
	contentionMinSamplesObserved = 2
)

// updateContentionLocked folds one host sample into every holder's
// contention accounting and refreshes each holder's throttled verdict. A
// holder is flagged contended when it is measurably slower than its
// profiled p99 (sample-gated) and the host was saturated for a sustained
// share of the run. The verdict latches; the first time a holder flips
// contended, the event is recorded in the window. The caller holds d.mu.
func (d *Daemon) updateContentionLocked(saturated bool, intervalMS int64, now time.Time) {
	for c := range d.conns {
		if c.role != roleHolder || !c.finalizable {
			continue
		}
		c.holdSampledMS += intervalMS
		if saturated {
			c.holdSaturatedMS += intervalMS
		}
		if c.contended {
			continue
		}
		elapsedMS := int64(0)
		if !c.startAt.IsZero() {
			elapsedMS = now.Sub(c.startAt).Milliseconds()
		}
		minSampledMS := contentionMinSamplesObserved * intervalMS
		if reason, ok := contentionVerdict(elapsedMS, c.expectedP99MS, c.sampleCount,
			c.holdSampledMS, c.holdSaturatedMS, minSampledMS); ok {
			c.contended = true
			c.contentionReason = reason
			d.events.record(now, admissionEvent{Kind: eventContended})
		}
	}
}

// contentionVerdict decides whether a holder is throttled by contention
// and, when so, its one-line explanation. It requires a trusted p99
// baseline (at least contentionMinSamples samples and a positive p99), an
// elapsed time past contentionSlowFactor of that p99, and a saturated
// share over contentionSaturatedShare across at least minSampledMS of
// observed host samples. Any gate unmet returns ok=false, so unprofiled,
// fast, or idle-host holders are never flagged.
func contentionVerdict(elapsedMS, p99MS int64, sampleCount int, sampledMS, saturatedMS, minSampledMS int64) (string, bool) {
	if p99MS <= 0 || sampleCount < contentionMinSamples {
		return "", false
	}
	if float64(elapsedMS) < contentionSlowFactor*float64(p99MS) {
		return "", false
	}
	if sampledMS <= 0 || sampledMS < minSampledMS {
		return "", false
	}
	share := float64(saturatedMS) / float64(sampledMS)
	if share < contentionSaturatedShare {
		return "", false
	}
	pct := int(share*100 + 0.5)
	reason := fmt.Sprintf("elapsed %s past p99 %s; host saturated %d%% of the run",
		fmtContentionDur(elapsedMS), fmtContentionDur(p99MS), pct)
	return reason, true
}

// fmtContentionDur renders a millisecond duration compactly for the
// contention explanation, matching the queue view's elapsed formatting.
func fmtContentionDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}
