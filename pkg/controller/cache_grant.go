package controller

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CacheGrantResponse is the body of POST /api/v1/runs/{id}/cache-grant.
type CacheGrantResponse struct {
	// Grant is the bearer the runner sends to the cache in place of the
	// cache's operator token. It opens only Team's blob stores, until
	// ExpiresAt: six hours, or the requesting credential's own expiry if
	// that comes first.
	Grant     string    `json:"grant"`
	Team      string    `json:"team"`
	ExpiresAt time.Time `json:"expires_at"`
}

// safety: The cache cannot resolve runner tokens, so the controller vouches for the run's owning team.
// teamOf must name that team: the grant is the cache's only team boundary and expires with the credential.
func (s *Server) handleRunCacheGrant(teamOf func(*http.Request) (store.Team, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		if runID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run id required"))
			return
		}
		p, _ := PrincipalFromContext(r.Context())
		// safety: a grant opens the team's whole cache tree, and this
		// credential is confined to one repository's runs.
		if _, isGitHub, _ := githubRunnerScope(p); isGitHub {
			writeGitHubFenceRefusal(w, p, "a GitHub Actions runner credential gets no cache grant; its job builds without the shared cache")
			return
		}
		key := os.Getenv(authwire.CacheGrantKeyEnv)
		if key == "" {
			writeError(w, http.StatusNotFound, errors.New("this controller holds no cache grant key, so it mints no cache grants"))
			return
		}
		cacheToken := s.cacheToken
		if cacheToken == "" {
			cacheToken = bincache.CacheToken()
		}
		if key == cacheToken {
			writeError(w, http.StatusServiceUnavailable, errors.New(
				"the cache grant key is the cache's operator token; give "+authwire.CacheGrantKeyEnv+" a secret of its own"))
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
		// safety: Round(0) drops the monotonic reading, so the ttl and the
		// expiry below are both wall-clock arithmetic and cannot drift apart.
		now := time.Now().Round(0)
		ttl := authwire.CacheGrantTTL
		if p != nil && !p.Expires.IsZero() {
			ttl = min(ttl, p.Expires.Sub(now))
		}
		if ttl < time.Second {
			writeAuthError(w, http.StatusUnauthorized, authErrorBody{
				Code: "unauthenticated", Principal: p.label(), Message: "the credential has expired",
			})
			return
		}
		var claim *authwire.CacheClaim
		node, trigger := claimIdentityShape(r)
		if node && trigger {
			writeError(w, http.StatusConflict, store.ErrLockHeld)
			return
		}
		if node {
			fence, fenceErr := nodeClaimFenceFromRequest(r)
			if fenceErr != nil {
				writeError(w, http.StatusConflict, store.ErrLockHeld)
				return
			}
			nodeID, liveErr := s.store.NodeClaimFenceNodeForRun(r.Context(), runID, fence, now)
			if liveErr != nil {
				s.writeInternalError(w, r, "check cache grant claim", liveErr)
				return
			}
			if nodeID == "" {
				writeError(w, http.StatusConflict, store.ErrLockHeld)
				return
			}
			claim = &authwire.CacheClaim{Kind: "node", NodeID: nodeID, HolderID: fence.HolderID, MembershipID: fence.MembershipID, ReservationID: fence.ReservationID, Generation: fence.ClaimGeneration, Principal: fence.Claimant.Principal, TokenPrefix: fence.Claimant.TokenPrefix}
		} else if trigger {
			generation, parseErr := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
			if parseErr != nil || generation < 1 {
				writeError(w, http.StatusConflict, store.ErrLockHeld)
				return
			}
			identity := claimIdentity(r)
			live, liveErr := s.store.TriggerClaimFenceIsLive(r.Context(), runID, identity, generation, now)
			if liveErr != nil {
				s.writeInternalError(w, r, "check cache grant claim", liveErr)
				return
			}
			if !live {
				writeError(w, http.StatusConflict, store.ErrLockHeld)
				return
			}
			claim = &authwire.CacheClaim{Kind: "trigger", Generation: generation, Principal: identity.Principal, TokenPrefix: identity.TokenPrefix}
		}
		grant, err := authwire.MintClaimCacheGrant(key, string(team), runID, now, ttl, claim)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		expiresAt := now.Add(ttl)
		// safety: a grant never outlives the credential that asked for it.
		if p != nil && !p.Expires.IsZero() && expiresAt.After(p.Expires) {
			expiresAt = p.Expires
		}
		writeJSON(w, http.StatusOK, CacheGrantResponse{
			Grant:     grant,
			Team:      string(team),
			ExpiresAt: expiresAt.UTC(),
		})
	})
}
