package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var commitID = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// PrepareCheckout acquires source without invoking an SDK or an engine. Its Entry
// has no executable command; callers must resolve the matching engine before installation.
// Close removes remote workspaces while preserving authorized local working trees.
func PrepareCheckout(ctx context.Context, spec Spec, options Options) (entry *Entry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	environment, err := sourceEnvironment(options, procenv.Git)
	if err != nil {
		return nil, err
	}
	entry = &Entry{RepoKey: spec.RepoKey, DefaultBranch: spec.DefaultBranch}
	defer func() {
		if err != nil {
			err = errors.Join(err, entry.Close())
			entry = nil
		}
	}()
	checkout := spec.LocalCheckout
	client := gitClient{env: gitEnvironment(environment), bundleTransport: options.BundleTransport}
	if options.PreparedCheckout != "" {
		if spec.LocalCheckout != "" || !filepath.IsAbs(options.PreparedCheckout) || spec.SHA == "" {
			return entry, errors.New("prepared source requires an absolute checkout and sealed remote commit")
		}
		checkout = options.PreparedCheckout
		remote, err := client.run(ctx, checkout, "", "remote", "get-url", "origin")
		if err != nil {
			return entry, err
		}
		actual, err := sourceurl.CaseIdentity(remote)
		expected, expectedErr := sourceurl.CaseIdentity(spec.RemoteURL)
		if err != nil || expectedErr != nil || actual != expected {
			return entry, errors.New("prepared source origin differs from the sealed remote")
		}
		changes, err := client.run(ctx, checkout, "", "status", "--porcelain", "--untracked-files=all", "--ignored=matching")
		if err != nil {
			return entry, err
		}
		if changes != "" {
			return entry, errors.New("prepared source differs from its sealed checkout")
		}
	}
	if checkout == "" {
		if !filepath.IsAbs(options.Workspace) {
			return entry, errors.New("remote source requires an absolute host workspace")
		}
		if options.Access == nil {
			return entry, errors.New("remote source requires live source access")
		}
		credential, err := options.Access.GitCredential(ctx, spec)
		if err != nil {
			return entry, fmt.Errorf("authorize source: %w", err)
		}
		remote, err := credentialRemote(spec.RemoteURL, credential)
		if err != nil {
			return entry, err
		}
		root, err := fssecure.MkdirPrivateTemp(options.Workspace, "source-")
		if err != nil {
			return entry, fmt.Errorf("create source workspace: %w", err)
		}
		entry.ownedDirectory = root
		checkout = filepath.Join(root, "checkout")
		client.credential, client.workspace = credential, root
		if err := cloneCommit(ctx, &client, spec, remote, checkout, options.Access); err != nil {
			return entry, err
		}
		if entry.DefaultBranch == "" && spec.BundleDigest == "" {
			entry.DefaultBranch, err = remoteDefaultBranch(ctx, &client, checkout, remote)
			if err != nil {
				return entry, err
			}
		}
		if entry.DefaultBranch != "" {
			if _, err := client.run(ctx, checkout, "", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+entry.DefaultBranch); err != nil {
				return entry, err
			}
		}
	}
	actual, err := client.run(ctx, checkout, "", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return entry, err
	}
	if spec.SHA != "" && !strings.EqualFold(actual, spec.SHA) {
		return entry, errors.New("source checkout HEAD differs from the sealed commit")
	}
	entry.SHA = actual
	if entry.DefaultBranch == "" && (spec.LocalCheckout != "" || options.PreparedCheckout != "") {
		branch, err := client.run(ctx, checkout, "", "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
		if err == nil {
			entry.DefaultBranch = strings.TrimPrefix(branch, "refs/remotes/origin/")
		} else if !gitExit(err, 1) {
			return entry, err
		}
	}
	entry.checkoutDirectory = checkout
	entry.Directory, err = pipelineDirectory(checkout, spec.PipelineDir)
	return entry, err
}

func validateSpec(spec Spec) error {
	if spec.RepoKey == "" {
		return errors.New("source repository identity is required")
	}
	if spec.LocalCheckout != "" {
		if !filepath.IsAbs(spec.LocalCheckout) {
			return errors.New("local source checkout must be absolute")
		}
	} else {
		if _, err := sourceurl.CaseIdentity(spec.RemoteURL); err != nil {
			return fmt.Errorf("source remote: %w", err)
		}
		if spec.SHA == "" {
			return errors.New("remote source requires the sealed commit, not a branch head")
		}
	}
	if spec.SHA != "" && !commitID.MatchString(spec.SHA) {
		return errors.New("source commit must be a full 40 or 64 digit hexadecimal ID")
	}
	policy := spec.Checkout.Policy
	if policy.Depth != nil && (*policy.Depth < 0 || *policy.Depth > 0 && policy.FullHistory != nil && *policy.FullHistory) {
		return errors.New("source depth must be nonnegative; positive depth conflicts with full history")
	}
	return nil
}

func cloneCommit(ctx context.Context, client *gitClient, spec Spec, remote, checkout string, access Access) error {
	if _, err := client.run(ctx, "", "", "init", "--quiet", checkout); err != nil {
		return err
	}
	if _, err := client.run(ctx, checkout, "", "remote", "add", "origin", remote); err != nil {
		return err
	}
	if spec.BundleDigest != "" {
		if err := seedBundle(ctx, client, checkout, spec.BundleDigest, access); err != nil {
			return err
		}
	}
	policy := spec.Checkout.Policy
	args := []string{"fetch", "--quiet", "--no-recurse-submodules"}
	if policy.Tags != nil && *policy.Tags {
		args = append(args, "--tags")
	} else {
		args = append(args, "--no-tags")
	}
	if policy.Depth != nil {
		if *policy.Depth > 0 {
			args = append(args, "--depth="+strconv.Itoa(*policy.Depth))
		}
	} else if policy.FullHistory == nil || !*policy.FullHistory {
		args = append(args, "--depth=1")
	}
	args = append(args, "--", remote, spec.SHA)
	_, presentErr := client.run(ctx, checkout, "", "cat-file", "-e", spec.SHA+"^{commit}")
	needsFetch := spec.BundleDigest == "" || presentErr != nil
	needsTags := policy.Tags != nil && *policy.Tags
	if needsFetch {
		if _, err := client.run(ctx, checkout, remote, args...); err != nil {
			return err
		}
	} else if needsTags {
		if _, err := client.run(ctx, checkout, remote, "fetch", "--quiet", "--tags", "--no-recurse-submodules", "--", remote); err != nil {
			return err
		}
	}
	if _, err := client.run(ctx, checkout, "", "checkout", "--quiet", "--detach", strings.ToLower(spec.SHA), "--"); err != nil {
		return err
	}
	if policy.Submodules != nil && *policy.Submodules {
		if err := checkoutSubmodules(ctx, client, checkout, remote); err != nil {
			return err
		}
	}
	if policy.LFS != nil && *policy.LFS {
		return checkoutLFS(ctx, client, checkout, remote, spec.SHA)
	}
	return nil
}

func remoteDefaultBranch(ctx context.Context, client *gitClient, checkout, remote string) (string, error) {
	output, err := client.run(ctx, checkout, remote, "ls-remote", "--symref", "--", remote, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(output, "\n") {
		ref, name, ok := strings.Cut(line, "\t")
		if ok && name == "HEAD" && strings.HasPrefix(ref, "ref: refs/heads/") {
			return strings.TrimPrefix(ref, "ref: refs/heads/"), nil
		}
	}
	return "", nil
}

func pipelineDirectory(checkout, relative string) (string, error) {
	if relative == "" {
		relative = ".sparkwing"
	}
	if !filepath.IsLocal(relative) {
		return "", errors.New("pipeline directory must stay inside the checkout")
	}
	root, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", err
	}
	directory, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, directory)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("pipeline directory resolves outside the checkout")
	}
	info, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("pipeline directory is not a directory")
	}
	return directory, nil
}

// Close releases acquisition-owned resources and preserves user checkouts.
func (e *Entry) Close() error {
	if e == nil {
		return nil
	}
	var err error
	if e.releaseSDK != nil {
		err = e.releaseSDK()
		e.releaseSDK = nil
	}
	if e.ownedDirectory != "" {
		if removeErr := os.RemoveAll(e.ownedDirectory); removeErr != nil {
			return errors.Join(err, removeErr)
		}
		e.ownedDirectory = ""
	}
	return err
}
