package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Default per-team daily download caps, by whether the team pays.
const (
	DefaultTeamDailyDownloadFreeBytes   int64 = 5 << 30
	DefaultTeamDailyDownloadFundedBytes int64 = 50 << 30
)

// WithTeamDownloadCaps sets per-team daily download caps; zero disables a cap.
func (s *Server) WithTeamDownloadCaps(free, funded int64) *Server {
	s.downloadFree, s.downloadFunded = free, funded
	return s
}

func (s *Server) teamStorageStanding(r *http.Request, team store.Team) (*teamStandingJSON, error) {
	if !s.deployment.MultiTeam() || team == "" || team == store.DefaultTeam {
		return nil, nil
	}
	tenant, err := s.tenantForTeam(r.Context(), team)
	if err != nil {
		return nil, err
	}
	got, err := tenant.StorageStanding(r.Context())
	if err != nil {
		return nil, err
	}
	return &teamStandingJSON{
		Team: string(team), Tier: got.Tier, AllowanceBytes: got.AllowanceBytes,
		EventBytes: got.EventBytes,
		CacheBytes: got.BlobBytes + got.ReservedBlobBytes,
		LogBytes:   got.LogBytes + got.ReservedLogBytes,
	}, nil
}

type teamStandingJSON struct {
	Team           string                `json:"team"`
	Tier           store.TeamStorageTier `json:"tier"`
	AllowanceBytes int64                 `json:"allowance_bytes"`
	EventBytes     int64                 `json:"event_bytes"`
	CacheBytes     int64                 `json:"cache_bytes"`
	LogBytes       int64                 `json:"log_bytes"`
}
