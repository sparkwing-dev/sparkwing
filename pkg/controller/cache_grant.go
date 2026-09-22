package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CacheGrantResponse is the body of POST /api/v1/runs/{id}/cache-grant.
type CacheGrantResponse struct {
	// Grant is the bearer the runner sends to the cache in place of the
	// cache's operator token. It opens only Team's blob stores.
	Grant     string    `json:"grant"`
	Team      string    `json:"team"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleRunCacheGrant mints a cache grant for one run. A runner's token is
// never the cache's token, so the cache cannot resolve it; the controller,
// which signs with the cache token it already holds for its own hop, vouches
// for the team instead. teamOf names the team whose namespace the grant opens,
// and must answer the run's owning team, because the grant is the only
// boundary between teams inside the cache.
func (s *Server) handleRunCacheGrant(teamOf func(*http.Request) (store.Team, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		if runID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run id required"))
			return
		}
		key := s.cacheToken
		if key == "" {
			key = bincache.CacheToken()
		}
		if key == "" {
			writeError(w, http.StatusNotFound, errors.New("this controller holds no cache token, so it mints no cache grants"))
			return
		}
		team, err := teamOf(r)
		if err != nil || team == "" {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:    "no_team",
				Message: "the credential acts for no registered team",
			})
			return
		}
		now := time.Now()
		grant, err := authwire.MintCacheGrant(key, string(team), runID, now, authwire.CacheGrantTTL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, CacheGrantResponse{
			Grant:     grant,
			Team:      string(team),
			ExpiresAt: now.Add(authwire.CacheGrantTTL).UTC(),
		})
	})
}
