package limits

import "time"

// ControllerCompatibilityMonths is a calendar-month promise from each release's publication.
// A support-window end alone never revokes an engine or prevents its execution.
const ControllerCompatibilityMonths = 12

// VersionIndexRefresh bounds routine policy-cache freshness; verified fallback has no age cutoff.
const VersionIndexRefresh time.Duration = 5 * time.Minute
