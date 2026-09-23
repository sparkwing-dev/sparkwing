package controller

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StorageTierResponse is the body of GET /internal/teams/{team}/storage-tier.
type StorageTierResponse struct {
	Team           string            `json:"team"`
	Tier           storagequota.Tier `json:"tier"`
	AllowanceBytes int64             `json:"allowance_bytes"`
}

// storageStanding answers funded for every team of a single-team
// controller, which has no free tier.
func (s *Server) storageStanding(r *http.Request, team store.Team) (store.StorageStanding, error) {
	if !s.MultiTeam() {
		return store.StorageStanding{Tier: store.TeamTierFunded}, nil
	}
	return s.store.StorageStandingFor(r.Context(), team)
}

// handleStorageTier tells the cache what a team may store. The cache proves
// itself with the operator token the controller already calls it with, so
// no new credential exists for this route.
func (s *Server) handleStorageTier(w http.ResponseWriter, r *http.Request) {
	token := s.cacheToken
	if token == "" {
		token = bincache.CacheToken()
	}
	scheme, got, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if token == "" || !strings.EqualFold(scheme, "bearer") ||
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) != 1 {
		writeError(w, http.StatusUnauthorized, errors.New("the storage tier route answers the cache's operator token only"))
		return
	}
	team := store.Team(r.PathValue("team"))
	if !teamblob.ValidTeam(string(team)) {
		writeError(w, http.StatusBadRequest, errors.New("not a team slug"))
		return
	}
	standing, err := s.storageStanding(r, team)
	if err != nil {
		s.writeInternalError(w, r, "read storage tier", err)
		return
	}
	writeJSON(w, http.StatusOK, StorageTierResponse{Team: string(team), Tier: storagequota.Tier(standing.Tier), AllowanceBytes: standing.AllowanceBytes})
}

// safety: the logs service reads the tier off the claim check it already
// makes for every append, so a failed check refuses the append and a tier is
// never assumed from silence.
func (s *Server) setStorageTierHeaders(w http.ResponseWriter, r *http.Request, runID string) error {
	if !s.MultiTeam() {
		return nil
	}
	got, err := s.store.StorageStandingForRun(r.Context(), runID)
	if err != nil {
		return err
	}
	w.Header().Set(storagequota.TierHeader, string(got.Tier))
	w.Header().Set(storagequota.AllowanceHeader, strconv.FormatInt(got.AllowanceBytes, 10))
	return nil
}

// handleGrantFreeSlot admits a team to the free tier whether or not a slot
// is free.
func (s *Server) handleGrantFreeSlot(w http.ResponseWriter, r *http.Request) {
	team := store.Team(r.PathValue("team"))
	err := s.store.GrantFreeSlot(r.Context(), team, time.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
		return
	case errors.Is(err, store.ErrInvalidSlug), errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "grant free slot", err)
		return
	}
	s.logger.Info("free-tier slot granted", "team", team)
	got, err := s.store.StorageStandingFor(r.Context(), team)
	if err != nil {
		s.writeInternalError(w, r, "read storage tier", err)
		return
	}
	writeJSON(w, http.StatusOK, StorageTierResponse{Team: string(team), Tier: storagequota.Tier(got.Tier), AllowanceBytes: got.AllowanceBytes})
}

// safety: a signed-up team reads its own standing and nothing of any other
// team's; the operator's team has no free tier to report.
func (s *Server) callerStorageStanding(r *http.Request) (*teamStandingJSON, error) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil || !s.MultiTeam() {
		return nil, nil
	}
	team := store.NormalizeTeam(p.Team)
	if team == "" || team == store.DefaultTeam {
		return nil, nil
	}
	got, err := s.store.StorageStandingFor(r.Context(), team)
	if err != nil {
		return nil, err
	}
	return &teamStandingJSON{
		Team: string(team), Tier: storagequota.Tier(got.Tier), AllowanceBytes: got.AllowanceBytes,
		CacheShareBytes: storagequota.CacheShare(got.AllowanceBytes),
		LogShareBytes:   storagequota.LogShare(got.AllowanceBytes),
		EventShareBytes: storagequota.EventShare(got.AllowanceBytes),
		EventBytes:      got.EventBytes,
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
}
