package wingd

import (
	"fmt"
	"time"
)

const (
	contentionMinSamples = 3

	contentionSlowFactor = 1.25

	contentionSaturationFraction = 0.90

	contentionSaturatedShare = 0.50

	contentionMinSamplesObserved = 2
)

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

func fmtContentionDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}
