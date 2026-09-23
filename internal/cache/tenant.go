package cache

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// teamsDir holds one blob-store tree per team that reached the cache with a
// controller-signed grant.
var teamsDir = "/data/teams"

// cacheCaller is who a request authenticated as. An empty team is the operator
// token (or an open cache), which keeps the unscoped trees; a grant's team
// confines the request to that team's trees.
type cacheCaller struct {
	team string
	run  string
}

type cacheCallerKey struct{}

func callerFrom(r *http.Request) cacheCaller {
	c, _ := r.Context().Value(cacheCallerKey{}).(cacheCaller)
	return c
}

// grantKey verifies cache grants; empty accepts none.
var grantKey string

// requireCaller admits the operator token or a grant the controller signed with
// the grant key. It fronts the blob stores and the clone routes a runner needs; seeding,
// refresh, archives, uploads and the admin routes stay behind requireToken,
// because the mirrors are shared and a seed lands one team's source in them.
// Registration answers another team's grant with 403 itself.
func requireCaller(next http.HandlerFunc) http.HandlerFunc {
	token, key := apiToken, grantKey
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next(w, r)
			return
		}
		got := bearerToken(r)
		if got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			next(w, r)
			return
		}
		g, err := authwire.VerifyCacheGrant(key, got, time.Now())
		if err != nil {
			http.Error(w, "unauthorized -- set Authorization: Bearer <token or cache grant> header", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), cacheCallerKey{}, cacheCaller{team: g.Team, run: g.Run})
		next(w, r.WithContext(ctx))
	}
}

// blobDirs are the trees one request may read and write.
type blobDirs struct {
	artifacts string
	bins      string
	cache     string
	// tenant marks a team's trees, whose bin writes count toward the store
	// ceiling; the operator's bins predate the ceiling and stay outside it.
	tenant bool
}

func dirsFor(r *http.Request) (blobDirs, error) {
	c := callerFrom(r)
	if c.team == "" {
		return blobDirs{artifacts: artifactsDir, bins: binsDir, cache: cacheDir}, nil
	}
	root := filepath.Join(teamsDir, c.team)
	d := blobDirs{
		artifacts: filepath.Join(root, "artifacts"),
		bins:      filepath.Join(root, "bins"),
		cache:     filepath.Join(root, "cache"),
		tenant:    true,
	}
	for _, dir := range []string{d.artifacts, d.bins, d.cache} {
		// #nosec G703 -- the team is a DNS-safe slug a verified grant carried
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return blobDirs{}, err
		}
	}
	return d, nil
}

func withBlobDirs(next func(http.ResponseWriter, *http.Request, blobDirs)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := dirsFor(r)
		if err != nil {
			http.Error(w, "prepare team store", http.StatusInternalServerError)
			return
		}
		next(w, r, d)
	}
}

// safety: every mirror is the operator's, since only its token or its team's grant registers one, so a mirror
// registered before grants existed counts too; the store reserves the operator's slug for it alone.
func (c cacheCaller) operatorGrant() bool { return c.team == authwire.OperatorTeam }

// safety: the operator's own runs register on first fetch as its token did; any other team would clone with the
// cache's credentials.
func (c cacheCaller) mayRegisterMirror() bool { return c.team == "" || c.operatorGrant() }

// safety: the mirrors are shared by every team, so another team's grant reads
// only a mirror that holds nothing private: an https origin the cache clones
// with no credential, registered under the one name derived from that URL.
func grantMayUseMirror(name, repoURL string) bool {
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	return name == sourceurl.ClaimedRepoNameFromURL(repoURL)
}

// safety: the operator reads its own private mirrors; another team's grant reads only a public one.
func (c cacheCaller) mayReadMirror(name string) bool {
	if c.team == "" || c.operatorGrant() {
		return true
	}
	repoNamesMu.RLock()
	repoURL, ok := repoNames[name]
	repoNamesMu.RUnlock()
	return ok && grantMayUseMirror(name, repoURL)
}
