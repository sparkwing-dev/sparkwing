package cache

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

var teamsDir = "/data/teams"

type cacheCaller struct {
	team   string
	run    string
	scopes []string
}

func (c cacheCaller) prefixes() []string {
	if len(c.scopes) == 0 {
		return []string{""}
	}
	return c.scopes
}

type cacheCallerKey struct{}

func callerFrom(r *http.Request) cacheCaller {
	c, _ := r.Context().Value(cacheCallerKey{}).(cacheCaller)
	return c
}

var grantKey string

// safety: seeding, refresh, archives, uploads and admin routes stay behind requireToken,
// because the mirrors are shared and a seed lands one team's source in them.
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
		ctx := context.WithValue(r.Context(), cacheCallerKey{}, cacheCaller{team: g.Team, run: g.Run, scopes: g.ScopePrefixes()})
		next(w, r.WithContext(ctx))
	}
}

type blobDirs struct {
	artifacts string
	bins      string
	cache     string
	tenant    bool
	reads     []blobDirs
}

func (d blobDirs) readOrder() []blobDirs {
	if len(d.reads) == 0 {
		return []blobDirs{d}
	}
	return d.reads
}

func (d blobDirs) find(kind func(blobDirs) string, name string) string {
	for _, tree := range d.readOrder() {
		candidate := filepath.Join(kind(tree), name)
		// #nosec G703 -- callers pass a pattern-validated key
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return filepath.Join(kind(d), name)
}

func binsOf(d blobDirs) string      { return d.bins }
func cachesOf(d blobDirs) string    { return d.cache }
func artifactsOf(d blobDirs) string { return d.artifacts }

func dirsFor(r *http.Request) (blobDirs, error) {
	c := callerFrom(r)
	if c.team == "" {
		return blobDirs{artifacts: artifactsDir, bins: binsDir, cache: cacheDir}, nil
	}
	var trees []blobDirs
	for _, prefix := range c.prefixes() {
		root := filepath.Join(teamsDir, c.team, filepath.FromSlash(prefix))
		trees = append(trees, blobDirs{
			artifacts: filepath.Join(root, "artifacts"),
			bins:      filepath.Join(root, "bins"),
			cache:     filepath.Join(root, "cache"),
			tenant:    true,
		})
	}
	d := trees[0]
	d.reads = trees
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
func (c cacheCaller) mayRegisterMirror() bool { return c.operator() }

// safety: anonymous git reaches only https, and the name a runner derives is the
// one an operator registration of that URL must use.
func grantMayUseMirror(name, repoURL string) bool {
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	return name == sourceurl.ClaimedRepoNameFromURL(repoURL)
}

func (c cacheCaller) operator() bool { return c.team == "" || c.operatorGrant() }

// safety: a seed or a credential the cache inherits puts private objects in the operator's mirror of
// even an https URL, so another team's grant reads only the separate public mirror of an https origin
// the operator registered, which the cache fills with no credential.
func (c cacheCaller) mayReadMirror(name string) bool {
	if c.operator() {
		return true
	}
	repoNamesMu.RLock()
	repoURL, ok := repoNames[name]
	repoNamesMu.RUnlock()
	return ok && grantMayUseMirror(name, repoURL)
}

// safety: deleting a tree that is already gone succeeds, so the controller can retry
// a deletion that stopped part-way.
func handleDeleteTeamTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}
	team := strings.TrimPrefix(r.URL.Path, "/admin/teams/")
	// safety: the name becomes a path under teamsDir and a bucket prefix, so
	// it is held to the DNS-label charset a team slug has and can never
	// climb out of either.
	if !isTeamSlug(team) {
		http.Error(w, "not a team slug", http.StatusBadRequest)
		return
	}
	// #nosec G703 -- the slug is checked above to hold no separator or dot
	if err := os.RemoveAll(filepath.Join(teamsDir, team)); err != nil {
		http.Error(w, "delete team tree", http.StatusInternalServerError)
		return
	}
	if err := deleteTeamBlobs(r.Context(), team); err != nil {
		log.Printf("warning: delete team %s from the blob store: %v", team, err)
		http.Error(w, "delete team blobs", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func isTeamSlug(s string) bool { return teamblob.ValidTeam(s) }
