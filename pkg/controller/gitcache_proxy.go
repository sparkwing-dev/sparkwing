package controller

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var (
	gitcacheSeedSHA  = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)
	gitcacheRepoName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

func (s *Server) handleGitcacheRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	repoURL, ok := validateGitcacheRepoURL(w, r)
	if !ok {
		return
	}
	s.proxyGitcache(w, r, "/git/refresh", repoURL, "", false, nil)
}

func (s *Server) handleGitcacheSeed(w http.ResponseWriter, r *http.Request) {
	extendGitcacheStreamDeadline(w)
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	repoURL, ok := validateGitcacheRepoURL(w, r)
	if !ok {
		return
	}
	sha := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sha")))
	if !gitcacheSeedSHA.MatchString(sha) {
		http.Error(w, "sha query param must be a 40-64 character hex object id", http.StatusBadRequest)
		return
	}
	workspace := r.URL.Query().Get("workspace")
	if workspace != "" && workspace != "1" {
		http.Error(w, "workspace query param must be 1 when set", http.StatusBadRequest)
		return
	}
	body := http.MaxBytesReader(w, r.Body, 500<<20)
	defer func() { _ = body.Close() }()
	s.proxyGitcache(w, r, "/sync/seed", repoURL, sha, workspace == "1", body)
}

func (s *Server) handleGitcacheRegister(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !gitcacheRepoName.MatchString(name) {
		http.Error(w, "name query param must contain only letters, digits, dots, underscores, or hyphens", http.StatusBadRequest)
		return
	}
	repoURL, ok := validateGitcacheRepoURL(w, r)
	if !ok {
		return
	}
	q := neturl.Values{}
	q.Set("name", name)
	q.Set("repo", repoURL)
	s.proxyGitcacheRequest(w, r, http.MethodPost, "/git/register", q.Encode(), nil)
}

func (s *Server) handleGitcacheGit(w http.ResponseWriter, r *http.Request) {
	extendGitcacheStreamDeadline(w)
	path := strings.TrimPrefix(r.PathValue("path"), "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || !gitcacheRepoName.MatchString(parts[0]) {
		http.Error(w, "invalid Git cache path", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodGet && parts[1] == "info/refs" && r.URL.Query().Get("service") == "git-upload-pack":
		q := neturl.Values{}
		q.Set("service", "git-upload-pack")
		s.proxyGitcacheRequest(w, r, http.MethodGet, "/git/"+parts[0]+"/info/refs", q.Encode(), nil)
	case r.Method == http.MethodPost && parts[1] == "git-upload-pack":
		s.proxyGitcacheRequest(w, r, http.MethodPost, "/git/"+parts[0]+"/git-upload-pack", "", r.Body)
	default:
		http.Error(w, "read-only Git upload-pack requests only", http.StatusMethodNotAllowed)
	}
}

func validateGitcacheRepoURL(w http.ResponseWriter, r *http.Request) (string, bool) {
	repoURL := r.URL.Query().Get("repo")
	if repoURL == "" {
		http.Error(w, "repo query param required", http.StatusBadRequest)
		return "", false
	}
	validated, err := sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		http.Error(w, "invalid repo URL: "+err.Error(), http.StatusBadRequest)
		return "", false
	}
	return validated, true
}

func (s *Server) proxyGitcache(w http.ResponseWriter, r *http.Request, path, repoURL, sha string, workspace bool, body io.Reader) {
	q := neturl.Values{}
	q.Set("repo", repoURL)
	if sha != "" {
		q.Set("sha", sha)
	}
	if workspace {
		q.Set("workspace", "1")
	}
	s.proxyGitcacheRequest(w, r, http.MethodPost, path, q.Encode(), body)
}

func (s *Server) proxyGitcacheRequest(w http.ResponseWriter, r *http.Request, method, path, rawQuery string, body io.Reader) {
	if s.cacheURL == "" {
		http.Error(w, "gitcache proxy is not configured", http.StatusNotFound)
		return
	}
	target := strings.TrimRight(s.cacheURL, "/") + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), method, target, body)
	if err != nil {
		http.Error(w, "build cache request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, key := range []string{"Accept", "Content-Type", "Git-Protocol"} {
		if value := r.Header.Get(key); value != "" {
			req.Header.Set(key, value)
		}
	}
	// safety: the cache authenticates every route, and the caller's own bearer is never forwarded.
	if token := bincache.CacheToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{
		Timeout: 30 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "cache request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		http.Error(w, "cache redirect rejected", http.StatusBadGateway)
		return
	}

	for k, vals := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		s.logger.Warn("gitcache proxy response write failed", "err", fmt.Sprint(err))
	}
}

func extendGitcacheStreamDeadline(w http.ResponseWriter) {
	deadline := time.Now().Add(30 * time.Minute)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
}
