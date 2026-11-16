### Added

- **logs:** Host-stamped lease keeper diagnostics end each log stream with a per-instance summary
  Before a stream seals, its queued keeper records drain and one `kind: summary`
  record per keeper instance reports the last delivered sequence, captured and
  lost totals, and whether the window lost nothing. The claim ends at the seal. A
  missing summary means incomplete diagnostics. A retried attempt's stream opens a
  new window. Renewal, finish authority and retry-tail fencing are unchanged.
