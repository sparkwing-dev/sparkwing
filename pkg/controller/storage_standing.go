package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func (s *Server) callerStorageStanding(r *http.Request) (*teamStandingJSON, error) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil || !s.MultiTeam() {
		return nil, nil
	}
	team := store.NormalizeTeam(p.Team)
	if team == "" || team == store.DefaultTeam {
		return nil, nil
	}
	tenant, err := s.store.ForTeam(r.Context(), team)
	if err != nil {
		return nil, err
	}
	got, err := tenant.StorageStanding(r.Context())
	if err != nil {
		return nil, err
	}
	return &teamStandingJSON{
		Team: string(team), Tier: storagequota.Tier(got.Tier), AllowanceBytes: got.AllowanceBytes,
		CacheShareBytes: store.FreeCacheShare(got.AllowanceBytes),
		LogShareBytes:   store.FreeLogShare(got.AllowanceBytes),
		EventShareBytes: store.FreeEventShare(got.AllowanceBytes),
		EventBytes:      got.EventBytes,
		CacheBytes:      got.BlobBytes + got.ReservedBlobBytes,
		LogBytes:        got.LogBytes,
	}, nil
}

type teamStandingJSON struct {
	Team            string            `json:"team"`
	Tier            storagequota.Tier `json:"tier"`
	AllowanceBytes  int64             `json:"allowance_bytes"`
	CacheShareBytes int64             `json:"cache_share_bytes"`
	LogShareBytes   int64             `json:"log_share_bytes"`
	EventShareBytes int64             `json:"event_share_bytes"`
	EventBytes      int64             `json:"event_bytes"`
	CacheBytes      int64             `json:"cache_bytes"`
	LogBytes        int64             `json:"log_bytes"`
}
