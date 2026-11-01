package controllerwire

import "github.com/sparkwing-dev/sparkwing/internal/settings"

// WarmCapacitySettings shares validation across operator transports and the store.
type WarmCapacitySettings = settings.WarmCapacitySettings

// WarmStartPercentiles preserves the absence of start observations.
type WarmStartPercentiles struct {
	P50Seconds *float64 `json:"p50_seconds"`
	P95Seconds *float64 `json:"p95_seconds"`
}

// WarmCapacityClass reports one policy and its observed demand.
type WarmCapacityClass struct {
	WarmCapacitySettings
	Warm          int64                `json:"warm"`
	Desired       int64                `json:"desired"`
	Start1h       WarmStartPercentiles `json:"start_1h"`
	Start24h      WarmStartPercentiles `json:"start_24h"`
	ColdStarts24h int64                `json:"cold_starts_24h"`
	ColdStarts7d  int64                `json:"cold_starts_7d"`
	Upsizes24h    int64                `json:"upsizes_24h"`
	Upsizes7d     int64                `json:"upsizes_7d"`
}

// WarmCapacity reports policies and deployment activity.
type WarmCapacity struct {
	Classes        []WarmCapacityClass `json:"classes"`
	NewSignups24h  int64               `json:"new_signups_24h"`
	NewSignups7d   int64               `json:"new_signups_7d"`
	ActiveTeams24h int64               `json:"active_teams_24h"`
	ActiveTeams7d  int64               `json:"active_teams_7d"`
}
