package bincache

import (
	"bytes"
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

// SeedRef names the one ref a working-tree snapshot bundle carries.
func SeedRef(sha string) string { return "refs/sparkwing-seed/" + sha }

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
		if out, err := gitCommandCombinedOutput(cmd); err != nil {
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
	if err := restoreRawCheckout(ctx, checkout, sha); err != nil {
		return "", err
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
	return directSparkwingDir(checkout)
}

func isWorkspaceSnapshotCommit(ctx context.Context, repoDir, sha string) (bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "cat-file", "commit", sha).Output()
	if err != nil {
		return false, fmt.Errorf("inspect workspace commit: %w", err)
	}
	header, message, ok := bytes.Cut(out, []byte("\n\n"))
	if !ok {
		return false, fmt.Errorf("inspect workspace commit: malformed identity")
	}
	parents := 0
	author := false
	for _, line := range bytes.Split(header, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("parent ")) {
			parents++
		}
		if bytes.HasPrefix(line, []byte("author Sparkwing <workspace@sparkwing.dev> ")) {
			author = true
		}
	}
	return author && parents == 0 && string(message) == "sparkwing working-tree snapshot\n", nil
}

func restoreRawCheckout(ctx context.Context, repoDir, sha string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "ls-tree", "-rz", "--full-tree", sha).Output()
	if err != nil {
		return fmt.Errorf("inspect workspace tree: %w", err)
	}
	for _, raw := range bytes.Split(out, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		metadata, name, ok := bytes.Cut(raw, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !ok || len(fields) != 3 || string(fields[1]) != "blob" {
			return fmt.Errorf("inspect workspace tree: malformed blob entry")
		}
		rel := filepath.Clean(filepath.FromSlash(string(name)))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("inspect workspace tree: unsafe path %q", name)
		}
		blob, blobErr := exec.CommandContext(ctx, "git", "-C", repoDir, "cat-file", "blob", string(fields[2])).Output()
		if blobErr != nil {
			return fmt.Errorf("read workspace blob %q: %w", name, blobErr)
		}
		path := filepath.Join(repoDir, rel)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replace workspace path %q: %w", name, err)
		}
		switch string(fields[0]) {
		case "100644":
			err = os.WriteFile(path, blob, 0o644)
			if err == nil {
				err = os.Chmod(path, 0o644)
			}
		case "100755":
			err = os.WriteFile(path, blob, 0o755)
			if err == nil {
				err = os.Chmod(path, 0o755)
			}
		case "120000":
			err = os.Symlink(string(blob), path)
		default:
			return fmt.Errorf("workspace tree contains unsupported mode %s for %q", fields[0], name)
		}
		if err != nil {
			return fmt.Errorf("restore workspace path %q: %w", name, err)
		}
	}
	return nil
}
