package controller

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CacheGrantResponse is the body of POST /api/v1/runs/{id}/cache-grant.
type CacheGrantResponse struct {
	// Grant is the bearer the runner sends to the cache in place of the
	// cache's operator token. It opens only Team's blob stores, within the
	// run's repository and ref, until ExpiresAt: five minutes for a claim
	// token, six hours for a runner's live node or trigger claim, or the
	// requesting credential's own expiry if that comes first.
	Grant     string    `json:"grant"`
	Team      string    `json:"team"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ClaimCacheGrantTTL bounds a claim token's cache grant, and so how long the
// cache serves a pod whose claim was lost or cancelled.
const ClaimCacheGrantTTL = 5 * time.Minute

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
		if tok, ok := claimTokenFromContext(r.Context()); ok {
			// safety: the cache checks a grant's signature alone, so a claim's grant lives
			// minutes, not hours, and its pod refreshes it through this claim-checked route.
			ttl = min(ttl, ClaimCacheGrantTTL)
			// safety: the grant carries the claim, which every controller use
			// of it re-checks, and expires with the claim token.
			claim = &authwire.CacheClaim{
				Kind: authwire.CacheClaimToken, NodeID: tok.NodeID, Generation: tok.Generation,
				Principal: p.Name, TokenPrefix: tok.Prefix,
			}
		} else if node && trigger {
			writeError(w, http.StatusConflict, store.ErrLockHeld)
			return
		} else if !node && !trigger {
			// safety: the cache honors a grant on its signature alone, so one minted with no
			// live claim would outlive a revoked token, a removed member and a finished run.
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code: "claim_required", Principal: p.label(),
				Message: "a cache grant needs the run's live node or trigger claim fence",
			})
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
		scope, err := s.cacheGrantScope(r.Context(), team, runID)
		if err != nil {
			s.writeInternalError(w, r, "read cache grant scope", err)
			return
		}
		grant, err := authwire.MintClaimCacheGrant(key, string(team), runID, now, ttl, claim, scope)
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

// safety: the cache scopes every key by the repository and refs a grant
// carries, so they come from the run's trigger and never from the caller. A
// run with no trigger writes under a scope no branch's run reads.
func (s *Server) cacheGrantScope(ctx context.Context, team store.Team, runID string) (*authwire.CacheScope, error) {
	trigger, err := s.store.GetTrigger(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return &authwire.CacheScope{Refs: []string{"manual:"}}, nil
	}
	if err != nil {
		return nil, err
	}
	if trigger.Team != team {
		return nil, errors.New("the run's trigger belongs to another team")
	}
	root, vouched, err := s.vouchedRoot(ctx, trigger)
	if err != nil {
		return nil, err
	}
	// safety: a vouched retry or child runs its root's ref and commit, so it takes the
	// root's scope whole, the pull request's ref and base included, which a retry row lacks.
	source := trigger
	if vouched {
		source = root
	}
	_, own, refs, err := s.triggerCacheScope(ctx, source)
	if err != nil {
		return nil, err
	}
	repo := cacheRepository(source)
	// safety: a run whose ref and commit only its submitter vouches for writes beside
	// that ref's entries and never over them.
	write := own
	if !vouched {
		write = "manual:" + own
	}
	if len(refs) == 0 || refs[0] != write {
		refs = append([]string{write}, refs...)
	}
	return &authwire.CacheScope{Repo: repo, Refs: refs}, nil
}

const maxCacheLineage = 64

// safety: classified as the OIDC subject's trigger is. A retry or child holds its root's
// ref only when the root's event or follow-the-tip schedule vouches for it and the run
// names that ref and commit with no uploaded source, so the code it runs is the root's.
func (s *Server) vouchedRoot(ctx context.Context, trigger *store.Trigger) (*store.Trigger, bool, error) {
	root := trigger
	for range maxCacheLineage {
		parent := root.RetryOf
		if parent == "" {
			parent = root.ParentRunID
		}
		if parent == "" {
			break
		}
		next, err := s.store.GetTrigger(ctx, parent)
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if next.Team != trigger.Team {
			return nil, false, nil
		}
		root = next
	}
	switch kind := oidcTriggerKind(root); {
	case root.RetryOf != "" || root.ParentRunID != "", kind == oidcTriggerManual:
		return nil, false, nil
	case kind == oidcTriggerCron && root.GitSHA != "":
		return nil, false, nil
	}
	if root == trigger {
		return root, true, nil
	}
	// safety: a submitter names the retry_of or parent it likes, so the lineage
	// vouches only for the repository the root ran.
	if repo := cacheRepository(trigger); repo == "" || repo != cacheRepository(root) {
		return nil, false, nil
	}
	rootID, err := s.store.TriggerRepoID(ctx, root.Team, root.ID)
	if err != nil {
		return nil, false, err
	}
	ownID, err := s.store.TriggerRepoID(ctx, trigger.Team, trigger.ID)
	if err != nil {
		return nil, false, err
	}
	if rootID > 0 && ownID > 0 && rootID != ownID {
		return nil, false, nil
	}
	ref := trigger.TriggerEnv["GITHUB_REF"]
	held := trigger.GitBranch == root.GitBranch && trigger.GitSHA == root.GitSHA &&
		(ref == "" || ref == root.TriggerEnv["GITHUB_REF"]) &&
		!strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@") &&
		trigger.TriggerEnv[bincache.SourceBundleObjectEnvKey] == ""
	return root, held, nil
}

// safety: the one repository the trigger names, with the port and the path's case kept,
// because either can name another repository; the OIDC subject drops the port, so this
// cannot reuse that. Names other than the clone URL are GitHub's, which folds case.
func cacheRepository(trigger *store.Trigger) string {
	repo, err := sourceurl.TriggerRepository(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo)
	if err != nil || trigger.RepoURL == "" {
		return repo
	}
	repo, err = sourceurl.CaseIdentity(trigger.RepoURL)
	if err != nil {
		return ""
	}
	return repo
}
