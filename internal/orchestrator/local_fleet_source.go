package orchestrator

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

type localFleetSource struct {
	url, token, name, repoURL string
	bareRepo                  string
	srv                       *http.Server
}

func startLocalFleetSource(root, bundle, sha, repoURL, token string) (*localFleetSource, error) {
	if root == "" || bundle == "" || sha == "" || repoURL == "" || token == "" {
		return nil, fmt.Errorf("fleet source requires root, bundle, commit, repository identity, and credential")
	}
	data := filepath.Join(root, "served-source")
	if err := fssecure.EnsureDir(data); err != nil {
		return nil, fmt.Errorf("prepare fleet source: %w", err)
	}
	bare := filepath.Join(data, "repo.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", bare).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("initialize fleet source: %w: %s", err, strings.TrimSpace(string(out)))
	}
	ref := bincache.SeedRef(sha)
	if out, err := exec.Command("git", "--git-dir", bare, "fetch", "--quiet", bundle, ref+":"+ref).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("import fleet source: %w: %s", err, strings.TrimSpace(string(out)))
	}
	history, err := exec.Command("git", "--git-dir", bare, "rev-list", "--parents", "--all").CombinedOutput()
	historyFields := strings.Fields(string(history))
	if err != nil || len(historyFields) != 1 || historyFields[0] != sha {
		return nil, fmt.Errorf("import fleet source: snapshot must contain one parentless commit")
	}
	refs, err := exec.Command("git", "--git-dir", bare, "for-each-ref", "--format=%(refname)").CombinedOutput()
	if err != nil || strings.TrimSpace(string(refs)) != ref {
		return nil, fmt.Errorf("import fleet source: snapshot must expose only its fixed ref")
	}
	for _, key := range []string{
		"uploadpack.allowAnySHA1InWant",
		"uploadpack.allowReachableSHA1InWant",
		"uploadpack.allowTipSHA1InWant",
	} {
		if out, err := exec.Command("git", "--git-dir", bare, "config", key, "false").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("configure fleet source: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fleet source listen: %w", err)
	}
	s := &localFleetSource{
		url: "http://" + lis.Addr().String(), token: token,
		name: bincache.ClaimedRepoNameFromURL(repoURL), repoURL: repoURL, bareRepo: bare,
	}
	s.srv = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(lis) }()
	return s, nil
}

func (s *localFleetSource) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const bearer = "Bearer "
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), bearer))
		if !strings.HasPrefix(r.Header.Get("Authorization"), bearer) || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/git/register" {
			if r.URL.Query().Get("name") != s.name || r.URL.Query().Get("repo") != s.repoURL {
				http.Error(w, "source identity mismatch", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		prefix := "/git/" + url.PathEscape(s.name) + "/"
		if !strings.HasPrefix(r.URL.EscapedPath(), prefix) {
			http.NotFound(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		switch {
		case r.Method == http.MethodGet && rest == "info/refs" && r.URL.Query().Get("service") == "git-upload-pack":
			s.serveInfoRefs(w)
		case r.Method == http.MethodPost && rest == "git-upload-pack":
			s.serveUploadPack(w, r.Body)
		default:
			http.Error(w, "read-only Git upload-pack requests only", http.StatusMethodNotAllowed)
		}
	})
}

func (s *localFleetSource) serveInfoRefs(w http.ResponseWriter) {
	cmd := exec.Command("git", "upload-pack", "--stateless-rpc", "--advertise-refs", s.bareRepo)
	out, err := cmd.Output()
	if err != nil {
		http.Error(w, "source advertisement failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	header := "# service=git-upload-pack\n"
	fmt.Fprintf(w, "%04x%s0000", len(header)+4, header)
	_, _ = w.Write(out)
}

func (s *localFleetSource) serveUploadPack(w http.ResponseWriter, body io.Reader) {
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	cmd := exec.Command("git", "upload-pack", "--stateless-rpc", s.bareRepo)
	cmd.Stdin = body
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		return
	}
}

func (s *localFleetSource) Close() {
	if s == nil || s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}
