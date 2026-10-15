package bincache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// SeedRef names the one ref a working-tree snapshot bundle carries.
func SeedRef(sha string) string { return "refs/sparkwing-seed/" + sha }

// SourceData binds a source bundle to the host's live data authority. Reads use
// the digest sealed in the run spec; writes retain the exact uploader receipt.
type SourceData struct {
	Bytes     blob.Store
	Records   blob.RecordStore
	Authority blob.Authority
	Identity  blob.UploadIdentity
	Budget    blob.Budget
}

// UploadSourceBundle stores a snapshot as a source_bundle record after verified
// upload. The caller seals the returned digest, rather than an object-store key.
func UploadSourceBundle(ctx context.Context, data *SourceData, sha, src string) (blob.Record, error) {
	if data == nil || data.Bytes == nil || data.Records == nil {
		return blob.Record{}, errors.New("source bundle storage binding is required")
	}
	if !bundleCommitID.MatchString(sha) {
		return blob.Record{}, errors.New("source bundle requires a full snapshot commit")
	}
	return uploadFileRecord(ctx, data.Bytes, data.Records, data.Authority, data.Identity, blob.KindSourceBundle, sha, src)
}

// FetchSourceBundle imports an authorized working-tree snapshot without a Git
// remote. It consumes the sealed digest to verified EOF before git sees bytes,
// then publishes the exact snapshot's checkout. Failure removes staging.
func FetchSourceBundle(ctx context.Context, data *SourceData, digest blob.Digest, sha, repoURL, workDir string) (path string, err error) {
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.Join(blob.ErrBudget, err)
		}
	}()
	if data == nil || data.Bytes == nil {
		return "", errors.New("source bundle storage binding is required")
	}
	if !bundleCommitID.MatchString(sha) {
		return "", errors.New("source bundle requires a full snapshot commit")
	}
	if data.Budget.Deadline.IsZero() {
		return "", blob.ErrBudget
	}
	ctx, cancel := context.WithDeadline(ctx, data.Budget.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(workDir, ".source-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if removeErr := os.RemoveAll(staging); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
	}()
	reader, err := data.Bytes.Get(ctx, blob.GetRequest{Authority: data.Authority, Digest: digest})
	if err != nil {
		return "", err
	}
	bundle, err := receiveFile(ctx, staging, reader)
	if err != nil {
		return "", err
	}
	checkout := filepath.Join(staging, "checkout")
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = bundleGitEnv(os.Environ())
		killGroupOnCancel(cmd)
		if out, err := gitCommandCombinedOutput(cmd); err != nil {
			return fmt.Errorf("git source import: %w: %s", errors.Join(err, ctx.Err()), strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := git("-c", "init.templateDir=", "init", "--quiet", checkout); err != nil {
		return "", err
	}
	if err := git("-C", checkout, "fetch", "--quiet", "--no-tags", "--", bundle, SeedRef(sha)); err != nil {
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
	if _, err := bundlePipelineDir(checkout); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	destination := filepath.Join(workDir, "checkout")
	if err := os.Rename(checkout, destination); err != nil {
		return "", err
	}
	return bundlePipelineDir(destination)
}

func isWorkspaceSnapshotCommit(ctx context.Context, repoDir, sha string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "cat-file", "commit", sha)
	cmd.Env = bundleGitEnv(os.Environ())
	killGroupOnCancel(cmd)
	out, err := gitCommandOutput(cmd)
	if err != nil {
		return false, fmt.Errorf("inspect workspace commit: %w", errors.Join(err, ctx.Err()))
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
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "ls-tree", "-rz", "--full-tree", sha)
	cmd.Env = bundleGitEnv(os.Environ())
	killGroupOnCancel(cmd)
	out, err := gitCommandOutput(cmd)
	if err != nil {
		return fmt.Errorf("inspect workspace tree: %w", errors.Join(err, ctx.Err()))
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
		cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "cat-file", "blob", string(fields[2]))
		cmd.Env = bundleGitEnv(os.Environ())
		killGroupOnCancel(cmd)
		blob, blobErr := gitCommandOutput(cmd)
		if blobErr != nil {
			return fmt.Errorf("read workspace blob %q: %w", name, errors.Join(blobErr, ctx.Err()))
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
