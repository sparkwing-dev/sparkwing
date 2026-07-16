package controller

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var gitcacheSeedSHA = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func (s *Server) handleGitcacheRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	repoURL, ok := validateGitcacheRepoURL(w, r)
	if !ok {
		return
	}
	s.proxyGitcache(w, r, "/git/refresh", repoURL, "", nil)
}

func (s *Server) handleGitcacheSeed(w http.ResponseWriter, r *http.Request) {
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
	body := http.MaxBytesReader(w, r.Body, 500<<20)
	defer func() { _ = body.Close() }()
	s.proxyGitcache(w, r, "/sync/seed", repoURL, sha, body)
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

func (s *Server) proxyGitcache(w http.ResponseWriter, r *http.Request, path, repoURL, sha string, body io.Reader) {
	if s.cacheURL == "" {
		http.Error(w, "gitcache proxy is not configured", http.StatusNotFound)
		return
	}
	q := neturl.Values{}
	q.Set("repo", repoURL)
	if sha != "" {
		q.Set("sha", sha)
	}
	target := strings.TrimRight(s.cacheURL, "/") + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, body)
	if err != nil {
		http.Error(w, "build cache request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "cache request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

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
