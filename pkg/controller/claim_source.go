package controller

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	modzip "golang.org/x/mod/zip"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// hack: tests replace the trusted fetches, since they have no GitHub to reach.
var (
	checkoutSource    = bincache.CheckoutSource
	fetchModuleCommit = bincache.FetchModuleCommit
	moduleTags        = bincache.ModuleTags
)

// safety: a plan claim builds the pipeline and a work claim runs it, so both fetch source and modules.
var claimSourceKinds = []store.ClaimTokenKind{store.ClaimTokenPlan, store.ClaimTokenWork}

// safety: each trusted fetch holds a git process and scratch disk on the
// controller, so a pod looping on these routes waits for a slot instead of
// taking more of them.
var trustedFetchSlots = make(chan struct{}, 8)

func acquireTrustedFetch(ctx context.Context) (func(), error) {
	select {
	case trustedFetchSlots <- struct{}{}:
		return func() { <-trustedFetchSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// safety: the claim token names its team and run, so the repository and
// commit come from the run's own trigger and never from the request.
func (s *Server) claimRunSource(w http.ResponseWriter, r *http.Request) (claimedRunSource, store.ClaimToken, bool) {
	tok, _ := claimTokenFromContext(r.Context())
	tenant, err := s.tenantForTeam(r.Context(), tok.Team)
	if err != nil {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return claimedRunSource{}, tok, false
	}
	trigger, err := s.store.GetTrigger(r.Context(), tok.RunID)
	if err != nil || store.NormalizeTeam(trigger.Team) != tok.Team {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return claimedRunSource{}, tok, false
	}
	return claimedRunSource{claimed: store.ClaimedRun{Team: tok.Team}, tenant: tenant, trigger: trigger}, tok, true
}

func directCredentialOf(c GitCredentialResponse) bincache.DirectCredential {
	d := bincache.DirectCredential{
		Kind: c.Kind, Host: strings.ToLower(c.Host), Username: c.Username,
		Secret: c.Secret, KnownHosts: c.KnownHosts, ExtraRepositories: c.ExtraRepositories,
	}
	if c.Kind == gitCredentialGitHubApp {
		d.Username, d.Secret = "x-access-token", c.Token
	}
	return d
}

func sourceOptionsFrom(q url.Values) (bincache.SourceOptions, error) {
	o := bincache.SourceOptions{Depth: 1}
	if v := q.Get("depth"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 0 {
			return o, errors.New("depth must be 0 (all history) or a positive count")
		}
		o.Depth = d
	}
	o.Tags, o.Submodules, o.LFS = q.Get("tags") == "1", q.Get("submodules") == "1", q.Get("lfs") == "1"
	return o, nil
}

func (s *Server) handleRunSource(w http.ResponseWriter, r *http.Request) {
	o, err := sourceOptionsFrom(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	src, tok, ok := s.claimRunSource(w, r)
	if !ok {
		return
	}
	t := src.trigger
	if t.GitSHA == "" {
		writeError(w, http.StatusConflict, errors.New("run "+t.ID+" records no commit to check out"))
		return
	}
	repoURL, err := bincache.TriggerRepoURL(t.RepoURL, t.TriggerEnv["GITHUB_REPOSITORY"], t.GithubOwner, t.GithubRepo, true)
	if err != nil || repoURL == "" {
		writeNoSourceCredential(w, "run "+t.ID+" names no repository to fetch")
		return
	}
	cred, ok := s.resolveGitCredential(w, r, src)
	if !ok {
		return
	}
	release, err := acquireTrustedFetch(r.Context())
	if err != nil {
		return
	}
	defer release()
	scratch, err := os.MkdirTemp("", "sparkwing-source-")
	if err != nil {
		s.writeInternalError(w, r, "source scratch", err)
		return
	}
	defer s.removeScratch(scratch)
	extendStreamDeadline(w, r, 30*time.Minute)
	dest := filepath.Join(scratch, "src")
	if err := checkoutSource(r.Context(), repoURL, t.GitSHA, t.GitBranch, dest, directCredentialOf(cred), o); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	// safety: the fetch can outlast the claim, so the tree leaves only for a
	// claim that is still live and uncancelled.
	if _, err := s.store.CheckClaimSensitive(r.Context(), tok, time.Now()); err != nil {
		writeClaimRefusal(w, r, s, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err = errors.Join(tw.AddFS(os.DirFS(dest)), tw.Close(), gz.Close())
	if err != nil {
		s.logger.Warn("source stream", "run_id", t.ID, "err", err)
	}
}

// safety: every module outside the owner's list is 404, so the go command moves on to the next proxy
// and the pod reads no repository the team did not list; @latest is not served, since a build names its versions.
func (s *Server) handleRunGoProxy(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("path")
	i := strings.Index(rest, "/@v/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	modPath, err := module.UnescapePath(rest[:i])
	file := rest[i+len("/@v/"):]
	repo, subdir, pathMajor, ok := githubModule(modPath)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	src, _, ok := s.claimRunSource(w, r)
	if !ok {
		return
	}
	runRepo, onGitHub := runGitHubRepo(src.trigger)
	if !onGitHub || s.githubApp == nil {
		http.NotFound(w, r)
		return
	}
	extra, err := ownerExtraRepos(r.Context(), src.tenant, runRepo)
	if err != nil {
		s.writeInternalError(w, r, "read extra repositories", err)
		return
	}
	listed := false
	for _, x := range extra {
		listed = listed || strings.EqualFold(x.Slug(), repo.Slug())
	}
	if !listed {
		http.NotFound(w, r)
		return
	}
	tok, failure := s.runAppToken(r, src, runRepo, extra)
	if failure != nil {
		failure.write(w)
		return
	}
	cred := bincache.DirectCredential{
		Kind: bincache.CredentialGitHubApp, Host: "github.com",
		Username: "x-access-token", Secret: tok.Token,
	}
	release, err := acquireTrustedFetch(r.Context())
	if err != nil {
		return
	}
	defer release()
	scratch, err := os.MkdirTemp("", "sparkwing-goproxy-")
	if err != nil {
		s.writeInternalError(w, r, "goproxy scratch", err)
		return
	}
	defer s.removeScratch(scratch)
	extendStreamDeadline(w, r, 30*time.Minute)
	repoURL := "https://github.com/" + repo.Slug() + ".git"
	tagPrefix := ""
	if subdir != "" {
		tagPrefix = subdir + "/"
	}
	if file == "list" {
		tags, err := moduleTags(r.Context(), repoURL, tagPrefix+"v", scratch, cred)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		var out strings.Builder
		for _, tag := range tags {
			v := strings.TrimPrefix(tag, tagPrefix)
			if semver.IsValid(v) && v == semver.Canonical(v) && module.CheckPathMajor(v, pathMajor) == nil {
				out.WriteString(v + "\n")
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(out.String()))
		return
	}
	ext := path.Ext(file)
	version, err := module.UnescapeVersion(strings.TrimSuffix(file, ext))
	if err != nil || module.Check(modPath, version) != nil || (ext != ".info" && ext != ".mod" && ext != ".zip") {
		http.NotFound(w, r)
		return
	}
	s.serveModuleFile(w, r, repoURL, cred, module.Version{Path: modPath, Version: version}, subdir, pathMajor, tagPrefix, ext, scratch)
}

func (s *Server) serveModuleFile(w http.ResponseWriter, r *http.Request, repoURL string, cred bincache.DirectCredential,
	m module.Version, subdir, pathMajor, tagPrefix, ext, scratch string,
) {
	ctx := r.Context()
	rev := "refs/tags/" + tagPrefix + strings.TrimSuffix(m.Version, "+incompatible")
	if module.IsPseudoVersion(m.Version) {
		var err error
		if rev, err = module.PseudoVersionRev(m.Version); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}
	mc, err := fetchModuleCommit(ctx, repoURL, rev, scratch, cred)
	if err != nil {
		http.Error(w, fmt.Sprintf("%s: %v", m, err), http.StatusNotFound)
		return
	}
	// safety: a /vN module lives in its vN directory when one holds a go.mod,
	// else at the root, the two layouts the go command accepts.
	dir := subdir
	if major := strings.TrimPrefix(pathMajor, "/"); major != "" {
		_, ok, err := mc.File(ctx, path.Join(subdir, major, "go.mod"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if ok {
			dir = path.Join(subdir, major)
		}
	}
	switch ext {
	case ".info":
		at := mc.Time
		if t, err := module.PseudoVersionTime(m.Version); err == nil {
			at = t
		}
		writeJSON(w, http.StatusOK, map[string]string{"Version": m.Version, "Time": at.UTC().Format(time.RFC3339)})
	case ".mod":
		mod, ok, err := mc.File(ctx, path.Join(dir, "go.mod"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if !ok {
			mod = []byte("module " + m.Path + "\n")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(mod)
	case ".zip":
		zf, err := os.Create(filepath.Join(scratch, "module.zip"))
		if err == nil {
			err = errors.Join(modzip.CreateFromVCS(zf, m, mc.Dir, mc.Commit, dir), zf.Close())
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("%s: %v", m, err), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		http.ServeFile(w, r, zf.Name())
	}
}

func (s *Server) removeScratch(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		s.logger.Warn("remove fetch scratch", "dir", dir, "err", err)
	}
}

// safety: only a github.com module maps to a repository the team's App token can read.
func githubModule(modPath string) (store.GitHubRepo, string, string, bool) {
	prefix, pathMajor, ok := module.SplitPathVersion(modPath)
	parts := strings.SplitN(prefix, "/", 4)
	if !ok || len(parts) < 3 || parts[0] != "github.com" {
		return store.GitHubRepo{}, "", "", false
	}
	repo, ok := store.ParseGitHubRepo(parts[1] + "/" + parts[2])
	subdir := ""
	if len(parts) == 4 {
		subdir = parts[3]
	}
	return repo, subdir, pathMajor, ok
}
