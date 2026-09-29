package procusage

import (
	"fmt"
	"time"
)

type ScanWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// CPURateTiming retains the scans used for a sampled sum of process CPU rates.
// Their end times define a representative weighting interval, not CPU duration.
type CPURateTiming struct {
	PreviousScan ScanWindow `json:"previous_scan"`
	CurrentScan  ScanWindow `json:"current_scan"`
}

// Validate checks consecutive scan bounds and representable elapsed time.
func (t CPURateTiming) Validate() error {
	for _, scan := range []ScanWindow{t.PreviousScan, t.CurrentScan} {
		if scan.Start.IsZero() || scan.End.IsZero() || scan.End.Before(scan.Start) {
			return fmt.Errorf("CPU rate timing requires valid scan bounds")
		}
	}
	if t.PreviousScan.End.After(t.CurrentScan.Start) || !t.CurrentScan.End.After(t.PreviousScan.End) {
		return fmt.Errorf("CPU rate scans overlap or have no positive weighting interval")
	}
	span := t.CurrentScan.End.Sub(t.PreviousScan.Start)
	if !t.PreviousScan.Start.Add(span).Equal(t.CurrentScan.End) {
		return fmt.Errorf("CPU rate scan span exceeds its duration representation")
	}
	return nil
}
