package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

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
	if spec.RemoteURL, err = tenantRemote(spec.RemoteURL, options); err != nil {
		return nil, err
	}
	if options.LocalBundle != "" && (spec.LocalCheckout == "" || !filepath.IsAbs(options.LocalBundle) || options.PreparedCheckout != "") {
		return nil, errors.New("local source bundle requires an absolute file and original checkout")
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
	client := gitClient{owner: options.Base.Local, env: gitEnvironment(environment), bundleTransport: options.BundleTransport}
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
		remote, bundleURL := spec.RemoteURL, ""
		if spec.BundleDigest != "" {
			bundleURL, err = options.Access.BundleURL(ctx, spec.BundleDigest)
			if err != nil {
				return entry, fmt.Errorf("authorize source bundle: %w", err)
			}
		} else {
			remote, err = client.authorizeRemote(ctx, spec, spec.RemoteURL, options.Access)
			if err != nil {
				return entry, err
			}
		}
		if err := fssecure.EnsureDir(options.Workspace); err != nil {
			return entry, fmt.Errorf("prepare host workspace: %w", err)
		}
		root, err := fssecure.MkdirPrivateTemp(options.Workspace, "source-")
		if err != nil {
			return entry, fmt.Errorf("create source workspace: %w", err)
		}
		entry.ownedDirectory = root
		checkout = filepath.Join(root, "checkout")
		client.workspace = root
		if err := cloneCommit(ctx, &client, spec, remote, checkout, bundleURL, options.Access); err != nil {
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
	actual, err := client.run(ctx, checkout, "", "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		if spec.LocalCheckout == "" || spec.SHA != "" || !gitExit(err, 1) {
			return entry, err
		}
		branch, branchErr := client.run(ctx, checkout, "", "symbolic-ref", "--quiet", "HEAD")
		if branchErr != nil {
			return entry, errors.Join(err, branchErr)
		}
		if _, refErr := client.run(ctx, checkout, "", "show-ref", "--verify", "--quiet", branch); !gitExit(refErr, 1) {
			return entry, errors.Join(err, refErr)
		}
	}
	if entry.DefaultBranch == "" && (spec.LocalCheckout != "" || options.PreparedCheckout != "") {
		branch, err := client.run(ctx, checkout, "", "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
		if err == nil {
			entry.DefaultBranch = strings.TrimPrefix(branch, "refs/remotes/origin/")
		} else if !gitExit(err, 1) {
			return entry, err
		}
	}
	if spec.LocalCheckout != "" && (spec.CommittedCheckout || options.LocalBundle != "" || spec.SHA != "" && !strings.EqualFold(actual, spec.SHA)) {
		if !filepath.IsAbs(options.Workspace) {
			return entry, errors.New("selected local revision requires an absolute host workspace")
		}
		if err := fssecure.EnsureDir(options.Workspace); err != nil {
			return entry, err
		}
		root, err := fssecure.MkdirPrivateTemp(options.Workspace, "revision-")
		if err != nil {
			return entry, err
		}
		entry.ownedDirectory = root
		client.workspace = root
		selected := filepath.Join(root, "checkout")
		revision := spec.SHA
		if revision == "" {
			revision = actual
		}
		local := client
		local.env = append(append([]string{}, client.env...), "GIT_ALLOW_PROTOCOL=file")
		policy := spec.Checkout.Policy
		if err := initCheckout(ctx, &client, selected, checkout, revision); err != nil {
			return entry, err
		}
		if options.LocalBundle != "" {
			if _, err := client.run(ctx, selected, "", "bundle", "unbundle", options.LocalBundle); err != nil {
				return entry, err
			}
			if err := preserveSnapshotBytes(selected); err != nil {
				return entry, err
			}
		}

		_, presentErr := client.run(ctx, selected, "", "cat-file", "-e", revision+"^{commit}")
		if presentErr != nil || policy.Tags != nil && *policy.Tags {
			if _, err := local.run(ctx, selected, "", append(fetchArgs(policy), "--", checkout, revision)...); err != nil {
				return entry, err
			}
		}
		if entry.DefaultBranch != "" {
			if _, err := client.run(ctx, selected, "", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+entry.DefaultBranch); err != nil {
				return entry, err
			}
		}

		remote := checkout
		if policy.Submodules != nil && *policy.Submodules || policy.LFS != nil && *policy.LFS {
			remote, err = client.run(ctx, checkout, "", "remote", "get-url", "origin")
			if err != nil {
				return entry, err
			}
			if options.Access != nil {
				remote, err = client.authorizeRemote(ctx, spec, remote, options.Access)
				if err != nil {
					return entry, err
				}
			}
			if _, err := client.run(ctx, selected, "", "remote", "set-url", "origin", remote); err != nil {
				return entry, err
			}
		}
		if err := checkoutCommit(ctx, &client, spec, selected, remote, revision); err != nil {
			return entry, err
		}
		checkout = selected
		actual, err = client.run(ctx, checkout, "", "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return entry, err
		}
	}
	if spec.SHA != "" && !strings.EqualFold(actual, spec.SHA) {
		return entry, errors.New("source checkout HEAD differs from the sealed commit")
	}
	entry.SHA = actual
	entry.checkoutDirectory = checkout
	if spec.BundleDigest != "" {
		relative := spec.PipelineDir
		if relative == "" {
			relative = ".sparkwing"
		}
		info, err := os.Lstat(filepath.Join(checkout, relative))
		if err != nil {
			return entry, err
		}
		if !info.IsDir() {
			return entry, errors.New("source bundle pipeline entry must be a directory")
		}
	}
	entry.Directory, err = pipelineDirectory(checkout, spec.PipelineDir)
	if err != nil {
		return entry, err
	}
	entry.pipelineCheckout = checkout
	if spec.PipelineSHA != "" && (spec.PipelineSHA != actual || spec.LocalCheckout != "" && !spec.CommittedCheckout) {
		code := spec.PipelineSource()
		codeOptions := options
		if options.PreparedCheckout != "" {
			code.LocalCheckout, code.RemoteURL = options.PreparedCheckout, ""
		}
		codeOptions.PreparedCheckout = ""
		entry.pipelineEntry, err = PrepareCheckout(ctx, code, codeOptions)
		if err != nil {
			return entry, err
		}
		entry.Directory, entry.pipelineCheckout = entry.pipelineEntry.Directory, entry.pipelineEntry.CheckoutDirectory()
	}
	return entry, nil
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
	if spec.PipelineSHA != "" && !runspec.IsCommitID(spec.PipelineSHA) {
		return errors.New("pipeline commit must be a full hexadecimal ID")
	}
	if spec.SHA != "" && !runspec.IsCommitID(spec.SHA) {
		return errors.New("source commit must be a full 40 or 64 digit hexadecimal ID")
	}
	policy := spec.Checkout.Policy
	if policy.Depth != nil && (*policy.Depth < 0 || *policy.Depth > 0 && policy.FullHistory != nil && *policy.FullHistory) {
		return errors.New("source depth must be nonnegative; positive depth conflicts with full history")
	}
	return nil
}

func (g *gitClient) authorizeRemote(ctx context.Context, spec Spec, remote string, access Access) (string, error) {
	credential, err := access.GitCredential(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("authorize source: %w", err)
	}
	remote, err = credentialRemote(remote, credential)
	if err != nil {
		return "", err
	}
	g.credential = credential
	return remote, nil
}

func cloneCommit(ctx context.Context, client *gitClient, spec Spec, remote, checkout, bundleURL string, access Access) error {
	if err := initCheckout(ctx, client, checkout, remote, spec.SHA); err != nil {
		return err
	}
	if spec.BundleDigest != "" {
		if err := seedBundle(ctx, client, checkout, spec.BundleDigest, bundleURL); err != nil {
			return err
		}
		if err := preserveSnapshotBytes(checkout); err != nil {
			return err
		}
	}
	policy := spec.Checkout.Policy
	args := fetchArgs(policy)
	_, presentErr := client.run(ctx, checkout, "", "cat-file", "-e", spec.SHA+"^{commit}")
	needsFetch := spec.BundleDigest == "" || presentErr != nil
	needsTags := policy.Tags != nil && *policy.Tags
	if spec.BundleDigest != "" && (needsFetch || needsTags || policy.Submodules != nil && *policy.Submodules || policy.LFS != nil && *policy.LFS) {
		var err error
		remote, err = client.authorizeRemote(ctx, spec, spec.RemoteURL, access)
		if err != nil {
			return err
		}
	}
	args = append(args, "--", remote, spec.SHA)
	if needsFetch {
		if _, err := client.run(ctx, checkout, remote, args...); err != nil {
			return err
		}
	} else if needsTags {
		if _, err := client.run(ctx, checkout, remote, "fetch", "--quiet", "--tags", "--no-recurse-submodules", "--", remote); err != nil {
			return err
		}
	}
	return checkoutCommit(ctx, client, spec, checkout, remote, spec.SHA)
}

func checkoutCommit(ctx context.Context, client *gitClient, spec Spec, checkout, remote, sha string) error {
	depth := checkoutDepth(spec.Checkout.Policy)
	if depth > 0 {
		local := *client
		local.env = append(append([]string{}, client.env...), "GIT_ALLOW_PROTOCOL=file")
		path := filepath.ToSlash(checkout)
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		seed := (&url.URL{Scheme: "file", Path: path}).String()
		if _, err := local.run(ctx, checkout, "", "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--update-shallow", "--depth="+strconv.Itoa(depth), "--", seed, sha); err != nil {
			return err
		}
	}
	if _, err := client.run(ctx, checkout, "", "checkout", "--quiet", "--detach", strings.ToLower(sha), "--"); err != nil {
		return err
	}
	return materializePolicy(ctx, client, spec, checkout, remote, sha)
}

func initCheckout(ctx context.Context, client *gitClient, checkout, origin, sha string) error {
	format := "sha1"
	if len(sha) == 64 {
		format = "sha256"
	}
	if _, err := client.run(ctx, "", "", "init", "--object-format="+format, "--quiet", checkout); err != nil {
		return err
	}
	_, err := client.run(ctx, checkout, "", "remote", "add", "origin", origin)
	return err
}

func fetchArgs(policy nodeproto.SourceSpec) []string {
	args := []string{"fetch", "--quiet", "--no-recurse-submodules"}
	if policy.Tags != nil && *policy.Tags {
		args = append(args, "--tags")
	} else {
		args = append(args, "--no-tags")
	}
	if depth := checkoutDepth(policy); depth > 0 {
		args = append(args, "--depth="+strconv.Itoa(depth))
	}
	return args
}

func checkoutDepth(policy nodeproto.SourceSpec) int {
	if policy.Depth != nil {
		return *policy.Depth
	}
	if policy.FullHistory != nil && *policy.FullHistory {
		return 0
	}
	return 1
}

// safety: snapshot blobs already hold the captured working-tree bytes; info/attributes
// outranks the tree's .gitattributes, so checkout cannot convert them again.
func preserveSnapshotBytes(checkout string) error {
	info := filepath.Join(checkout, ".git", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(info, "attributes"), []byte("* -text -filter -ident -working-tree-encoding\n"), 0o600)
}

func materializePolicy(ctx context.Context, client *gitClient, spec Spec, checkout, remote, sha string) error {
	policy := spec.Checkout.Policy
	if policy.Submodules != nil && *policy.Submodules {
		if err := checkoutSubmodules(ctx, client, checkout, remote); err != nil {
			return err
		}
	}
	if policy.LFS != nil && *policy.LFS {
		return checkoutLFS(ctx, client, checkout, remote, sha)
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
	if e.pipelineEntry != nil {
		err = errors.Join(err, e.pipelineEntry.Close())
		e.pipelineEntry = nil
	}
	if e.ownedDirectory != "" {
		if removeErr := os.RemoveAll(e.ownedDirectory); removeErr != nil {
			return errors.Join(err, removeErr)
		}
		e.ownedDirectory = ""
	}
	return err
}
