package source

import (
	"context"
	"errors"
	"os"
	"path"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// Select resolves the execution commit, reads its configuration for the pipeline's code pin and resolves the code commit.
func Select(ctx context.Context, spec Spec, pipeline string, git runspec.Git, code runspec.Source, override string, options Options) (runspec.Git, runspec.Source, *projectconfig.Config, error) {
	lookup := func(ref string) (string, error) { return ResolveRef(ctx, spec, ref, options) }
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
	env, err := sourceEnvironment(options, procenv.Git)
	if err != nil {
		return nil, false, err
	}
	client := &gitClient{env: gitEnvironment(env)}
	repository := spec.LocalCheckout
	if repository == "" {
		remote := spec.RemoteURL
		if options.Access != nil {
			if remote, err = client.authorizeRemote(ctx, spec, remote, options.Access); err != nil {
				return nil, false, err
			}
		}
		if repository, err = os.MkdirTemp(options.Workspace, "selection-"); err != nil {
			return nil, false, err
		}
		owned := repository
		defer func() { err = errors.Join(err, os.RemoveAll(owned)) }()
		client.workspace = repository
		format := "--object-format=sha1"
		if len(sha) == 64 {
			format = "--object-format=sha256"
		}
		if _, err := client.run(ctx, "", "", "init", "--quiet", "--bare", format, repository); err != nil {
			return nil, false, err
		}
		fetch := []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--filter=blob:none"}
		if _, err := client.run(ctx, repository, remote, append(fetch, "--depth=1", "--", remote, sha)...); err != nil {
			return nil, false, err
		}
		object, err := client.run(ctx, repository, "", "ls-tree", "--object-only", sha, "--", name)
		if err != nil || object == "" {
			return nil, false, err
		}
		if _, err := client.run(ctx, repository, remote, append(fetch, "--", remote, object)...); err != nil {
			return nil, false, err
		}
	}
	listed, err := client.run(ctx, repository, "", "ls-tree", "--name-only", sha, "--", name)
	if err != nil || listed == "" {
		return nil, false, err
	}
	content, err := client.run(ctx, repository, "", "cat-file", "blob", sha+":"+name)
	return []byte(content), err == nil, err
}
