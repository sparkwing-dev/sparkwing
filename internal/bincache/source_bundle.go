package bincache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/directdata"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var sourceBundleKeyRE = regexp.MustCompile(`^sources/[0-9a-f]{64}/[0-9a-f]{32}$`)

// FetchSourceBundleDirect imports a claim-authorized snapshot without a Git
// remote. Its bundle is verified before git sees it, and git uses no ambient
// global config or credentials while opening the local file.
func FetchSourceBundleDirect(ctx context.Context, controllerURL, grant, runID, key, sha, repoURL, workDir string) (path string, err error) {
	if !sourceBundleKeyRE.MatchString(key) || !directCommitRE.MatchString(sha) || grant == "" {
		return "", errors.New("invalid source bundle claim")
	}
	transport := &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	d := directdata.New(controllerURL, grant, runID, transport)
	rc, meta, err := d.Download(ctx, "source", key)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if meta.Size < 0 || meta.Size > 500<<20 || meta.SHA256 != strings.Split(key, "/")[1] {
		return "", errors.New("source bundle metadata does not match the requested object")
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(workDir, "source-*.bundle")
	if err != nil {
		return "", err
	}
	defer func() {
		if removeErr := os.Remove(f.Name()); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
	}()
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(rc, meta.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != meta.Size || hex.EncodeToString(h.Sum(nil)) != meta.SHA256 {
		return "", errors.New("source bundle size or SHA256 mismatch")
	}
	checkout := filepath.Join(workDir, "checkout")
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git source import: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := git("init", "--quiet", checkout); err != nil {
		return "", err
	}
	if err := git("-C", checkout, "fetch", "--quiet", "--no-tags", "--", f.Name(), SeedRef(sha)); err != nil {
		return "", err
	}
	if err := git("-C", checkout, "-c", "core.autocrlf=false", "-c", "core.hooksPath=/dev/null", "checkout", "--detach", "--quiet", sha); err != nil {
		return "", err
	}
	valid, err := isWorkspaceSnapshotCommit(ctx, checkout, sha)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", errors.New("source bundle does not contain the declared working-tree snapshot")
	}
	if repoURL != "" {
		remote, err := sourceurl.ValidateCloneURL(repoURL)
		if err != nil {
			return "", err
		}
		if err := git("-C", checkout, "remote", "add", "origin", remote); err != nil {
			return "", err
		}
	}
	candidate := filepath.Join(checkout, ".sparkwing")
	if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
		return "", errors.New("source bundle has no .sparkwing directory")
	}
	return candidate, nil
}
