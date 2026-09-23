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

// StorageTierResponse is the body of PUT /api/v1/storage/teams/{team}/free-slot.
type StorageTierResponse struct {
	Team           string            `json:"team"`
	Tier           storagequota.Tier `json:"tier"`
	AllowanceBytes int64             `json:"allowance_bytes"`
}

// Default per-team daily download caps, by whether the team pays.
const (
	DefaultTeamDailyDownloadFreeBytes   int64 = 5 << 30
	DefaultTeamDailyDownloadFundedBytes int64 = 50 << 30
)

// WithTeamDownloadCaps sets what one team may download in a UTC day through
// the cache, by whether it pays. Zero is no cap.
func (s *Server) WithTeamDownloadCaps(free, funded int64) *Server {
	s.downloadFree, s.downloadFunded = free, funded
	return s
}

type counterCaller struct {
	service bool
	team    store.Team
}

// safety: the counter routes sit outside the principal middleware because
// the cache's operator token is not a principal; a forwarded credential is
// authenticated here exactly as the middleware would.
func (s *Server) counterCaller(w http.ResponseWriter, r *http.Request) (counterCaller, bool) {
	scheme, raw, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	raw = strings.TrimSpace(raw)
	if !strings.EqualFold(scheme, "bearer") || raw == "" {
		writeError(w, http.StatusUnauthorized, errors.New("the storage counter routes need a bearer"))
		return counterCaller{}, false
	}
	token := s.cacheToken
	if token == "" {
		token = bincache.CacheToken()
	}
	if token != "" && subtle.ConstantTimeCompare([]byte(raw), []byte(token)) == 1 {
		return counterCaller{service: true}, true
	}
	p, err := s.authMiddleware().Authenticate(raw)
	if err != nil || p == nil || !(p.HasScope(ScopeLogsWrite) || p.HasScope(ScopeAdmin)) {
		writeError(w, http.StatusUnauthorized, errors.New("the storage counter routes answer the cache's operator token or a logs.write credential"))
		return counterCaller{}, false
	}
	return counterCaller{team: store.NormalizeTeam(p.Team)}, true
}

func (c counterCaller) may(w http.ResponseWriter, team string, kind store.StorageKind) bool {
	if !teamblob.ValidTeam(team) {
		writeError(w, http.StatusBadRequest, errors.New("not a team slug"))
		return false
	}
	if !kind.Valid() {
		writeError(w, http.StatusBadRequest, errors.New("store names neither cache nor logs"))
		return false
	}
	if c.service {
		return true
	}
	if kind != store.StorageLogs || c.team != store.NormalizeTeam(store.Team(team)) {
		writeError(w, http.StatusForbidden, errors.New("a forwarded credential counts only its own team's logs"))
		return false
	}
	return true
}

type storageReserveReq struct {
	Team  string            `json:"team"`
	Store store.StorageKind `json:"store"`
	Bytes int64             `json:"bytes"`
	UpTo  bool              `json:"up_to"`
}

func (s *Server) handleStorageReserve(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.counterCaller(w, r)
	if !ok {
		return
	}
	var req storageReserveReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !caller.may(w, req.Team, req.Store) {
		return
	}
	if !s.MultiTeam() {
		writeJSON(w, http.StatusOK, store.StorageReservation{Tier: store.TeamTierFunded, Granted: max(req.Bytes, 0), Unlimited: true})
		return
	}
	res, err := s.store.ReserveStorage(r.Context(), store.StorageReserve{
		Team: store.Team(req.Team), Kind: req.Store, Bytes: req.Bytes, UpTo: req.UpTo, Now: time.Now(),
	})
	s.writeReservation(w, r, res, err)
}

type storageCommitReq struct {
	Team        string            `json:"team"`
	Store       store.StorageKind `json:"store"`
	Reservation string            `json:"reservation"`
	Bytes       int64             `json:"bytes"`
	NextBytes   int64             `json:"next_bytes,omitempty"`
}

func (s *Server) handleStorageCommit(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.counterCaller(w, r)
	if !ok {
		return
	}
	var req storageCommitReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !caller.may(w, req.Team, req.Store) {
		return
	}
	if !s.MultiTeam() {
		if req.NextBytes > 0 {
			writeJSON(w, http.StatusOK, store.StorageReservation{Tier: store.TeamTierFunded, Granted: req.NextBytes, Unlimited: true})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	commit := store.StorageCommit{
		ID: req.Reservation, Team: store.Team(req.Team), Kind: req.Store, Bytes: req.Bytes, Now: time.Now(),
	}
	if req.NextBytes <= 0 {
		err := s.store.CommitStorage(r.Context(), commit)
		switch {
		case errors.Is(err, store.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, err)
		case err != nil:
			s.writeInternalError(w, r, "commit team storage", err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	next, err := s.store.RenewStorage(r.Context(), commit, store.StorageReserve{
		Team: store.Team(req.Team), Kind: req.Store, Bytes: req.NextBytes, UpTo: true, Now: commit.Now,
	})
	s.writeReservation(w, r, next, err)
}

func (s *Server) writeReservation(w http.ResponseWriter, r *http.Request, res store.StorageReservation, err error) {
	var quota *store.StorageQuotaError
	switch {
	case errors.As(err, &quota):
		writeError(w, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, store.ErrFreeStoragePaused):
		writeError(w, http.StatusPaymentRequired, err)
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "reserve team storage", err)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

type storageReleaseReq struct {
	Team        string `json:"team"`
	Reservation string `json:"reservation"`
}

func (s *Server) handleStorageRelease(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.counterCaller(w, r)
	if !ok {
		return
	}
	var req storageReleaseReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !teamblob.ValidTeam(req.Team) {
		writeError(w, http.StatusBadRequest, errors.New("not a team slug"))
		return
	}
	if !caller.service && caller.team != store.NormalizeTeam(store.Team(req.Team)) {
		writeError(w, http.StatusForbidden, errors.New("a forwarded credential releases only its own team's reservations"))
		return
	}
	if s.MultiTeam() {
		if err := s.store.ReleaseStorage(r.Context(), store.Team(req.Team), req.Reservation, time.Now()); err != nil {
			s.writeInternalError(w, r, "release team storage", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type downloadChargeReq struct {
	Team   string `json:"team"`
	Bytes  int64  `json:"bytes"`
	Record bool   `json:"record"`
}

func (s *Server) handleDownloadCharge(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.counterCaller(w, r)
	if !ok {
		return
	}
	if !caller.service {
		writeError(w, http.StatusForbidden, errors.New("only the cache charges downloads"))
		return
	}
	var req downloadChargeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !teamblob.ValidTeam(req.Team) {
		writeError(w, http.StatusBadRequest, errors.New("not a team slug"))
		return
	}
	if !s.MultiTeam() {
		writeJSON(w, http.StatusOK, store.DownloadCharged{Tier: store.TeamTierFunded})
		return
	}
	got, err := s.store.ChargeDownload(r.Context(), store.DownloadCharge{
		Team: store.Team(req.Team), Bytes: req.Bytes, Record: req.Record, Now: time.Now(),
		FreeCapBytes: s.downloadFree, FundedCapBytes: s.downloadFunded,
	})
	var capErr *store.DownloadCapError
	switch {
	case errors.As(err, &capErr):
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(capErr.RetryAfter), 10))
		writeError(w, http.StatusTooManyRequests, err)
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "charge team download", err)
	default:
		writeJSON(w, http.StatusOK, got)
	}
}

func (s *Server) handleEgressTotals(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.counterCaller(w, r)
	if !ok {
		return
	}
	if !caller.service {
		writeError(w, http.StatusForbidden, errors.New("only the cache records egress totals"))
		return
	}
	var req store.EgressTotals
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	got, err := s.store.RecordEgressTotals(r.Context(), req)
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "record egress totals", err)
	default:
		writeJSON(w, http.StatusOK, got)
	}
}

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
	held, err := s.store.TeamStorage(r.Context(), team)
	if err != nil {
		return nil, err
	}
	return &teamStandingJSON{
		Team: string(team), Tier: storagequota.Tier(got.Tier), AllowanceBytes: got.AllowanceBytes,
		CacheShareBytes: store.FreeCacheShare(got.AllowanceBytes),
		LogShareBytes:   store.FreeLogShare(got.AllowanceBytes),
		EventShareBytes: store.FreeEventShare(got.AllowanceBytes),
		EventBytes:      got.EventBytes,
		CacheBytes:      held[store.StorageCache].UsedBytes + held[store.StorageCache].ReservedBytes,
		LogBytes:        held[store.StorageLogs].UsedBytes + held[store.StorageLogs].ReservedBytes,
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
