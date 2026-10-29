package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// Select resolves the execution commit, reads its configuration for the pipeline's code pin and resolves the code commit.
func Select(ctx context.Context, spec Spec, pipeline string, git runspec.Git, code runspec.Source, override string, options Options) (runspec.Git, runspec.Source, *projectconfig.Config, error) {
	lookup := func(ref string) (string, error) {
		if options.ResolveRef != nil {
			return resolveReference(ref, false, func(refs []string) (map[string]string, error) {
				commits := make(map[string]string)
				for _, candidate := range refs {
					sha, err := options.ResolveRef(ctx, candidate)
					if err != nil {
						return nil, err
					}
					commits[candidate] = sha
				}
				return commits, nil
			})
		}
		return ResolveRef(ctx, spec, ref, options)
	}
	git, _, err := ResolveRevisions(ctx, git, runspec.Source{}, "", lookup)
	if err != nil {
		return git, code, nil, err
	}
	directory := code.Location.PipelineDir
	if directory == "" {
		directory = ".sparkwing"
	}
	read := options.ReadFile
	if read == nil {
		read = func(ctx context.Context, sha, name string) ([]byte, bool, error) {
			return readCommitFile(ctx, spec, sha, name, options)
		}
	}
	raw, found, err := read(ctx, git.SHA, path.Join(directory, projectconfig.Filename))
	if err != nil {
		return git, code, nil, err
	}
	var cfg *projectconfig.Config
	code.RequestedRef = ""
	if found {
		if cfg, err = projectconfig.Parse(raw); err != nil {
			return git, code, nil, &apierr.Error{Code: apierr.InvalidArgs, Message: "execution commit configuration is invalid: " + err.Error()}
		}
		if entry := cfg.Registry().Find(pipeline); entry != nil {
			code.RequestedRef = entry.Source
		}
	}
	git, code, err = ResolveRevisions(ctx, git, code, override, lookup)
	return git, code, cfg, err
}

func readCommitFile(ctx context.Context, spec Spec, sha, name string, options Options) (_ []byte, _ bool, err error) {
	if (spec.LocalCheckout == "") == (spec.RemoteURL == "") {
		return nil, false, errors.New("source selection requires exactly one acquisition location")
	}
	if spec.LocalCheckout != "" {
		return readLocalCommitFile(ctx, spec, sha, name, options)
	}
	if options.Access == nil {
		return nil, false, errors.New("remote source selection requires live source access")
	}
	environment, err := sourceEnvironment(options, procenv.Git)
	if err != nil {
		return nil, false, err
	}
	if spec.RemoteURL, err = tenantRemote(spec.RemoteURL, options); err != nil {
		return nil, false, err
	}
	parent := options.Workspace
	if parent == "" {
		parent = os.TempDir()
	}
	if err := fssecure.EnsureDir(parent); err != nil {
		return nil, false, err
	}
	workspace, err := fssecure.MkdirPrivateTemp(parent, "selection-")
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(workspace)) }()
	client := &gitClient{owner: options.ownerGit(), env: gitEnvironment(environment), workspace: workspace, masker: options.Base.Masker}
	repository := filepath.Join(workspace, "objects.git")
	remote, err := client.authorizeRemote(ctx, spec, spec.RemoteURL, options.Access)
	if err != nil {
		return nil, false, err
	}
	format := "sha1"
	if len(sha) == 64 {
		format = "sha256"
	}
	if _, err := client.run(ctx, "", "", "init", "--bare", "--quiet", "--object-format="+format, repository); err != nil {
		return nil, false, err
	}
	if _, err := client.run(ctx, repository, remote, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--depth=1", "--filter=blob:limit="+strconv.FormatInt(limits.ProjectConfigBytes+1, 10), "--", remote, sha); err != nil {
		return nil, false, err
	}
	// safety: an oversized blob the remote omitted must stay absent rather than
	// be fetched lazily; readCommittedBlob still refuses one the remote sent.
	local := *client
	local.env = append(append([]string{}, client.env...), "GIT_NO_LAZY_FETCH=1")
	return readCommittedBlob(ctx, &local, repository, sha+":"+name)
}

func readLocalCommitFile(ctx context.Context, spec Spec, sha, name string, options Options) (_ []byte, _ bool, err error) {
	workspace, err := os.MkdirTemp(options.Workspace, "selection-")
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(workspace)) }()
	spec.SHA, spec.PipelineSHA, spec.PipelineDir, spec.CommittedCheckout = sha, "", ".", true
	options.Workspace, options.PreparedCheckout = workspace, ""
	entry, err := PrepareCheckout(ctx, spec, options)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, entry.Close()) }()
	root, err := os.OpenRoot(entry.CheckoutDirectory())
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	content, err := io.ReadAll(io.LimitReader(file, limits.ProjectConfigBytes+1))
	if err = errors.Join(err, file.Close()); err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func readCommittedBlob(ctx context.Context, client *gitClient, repository, object string) ([]byte, bool, error) {
	tooLarge := &apierr.Error{Code: apierr.InvalidArgs, Message: "execution commit configuration is invalid: " + projectconfig.ErrTooLarge.Error()}
	out, err := client.output(ctx, repository, "", object+"\n", "cat-file", "--batch-check", "--follow-symlinks")
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, err
		}
		diagnose := *client
		diagnose.combined = true
		detail, _ := diagnose.output(ctx, repository, "", object+"\n", "cat-file", "--batch-check", "--follow-symlinks")
		// safety: the size filter leaves an oversized blob absent, which git
		// reports only as an object it may not fetch.
		if strings.Contains(string(detail), "from promisor remote") {
			return nil, false, errors.Join(tooLarge, err)
		}
		return nil, false, fmt.Errorf("read the execution commit configuration: %w", err)
	}
	header, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(header)
	switch {
	case len(fields) == 3 && fields[1] == "blob":
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("source git reported an invalid object size")
		}
		if size > limits.ProjectConfigBytes {
			return nil, false, tooLarge
		}
		content, err := client.output(ctx, repository, "", "", "cat-file", "blob", fields[0])
		if err == nil && int64(len(content)) != size {
			err = fmt.Errorf("source git returned a configuration of the wrong size")
		}
		return content, err == nil, err
	case len(fields) == 2 && fields[1] == "missing", len(fields) == 2 && fields[0] == "dangling":
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("source configuration is not a file inside the commit")
	}
}
