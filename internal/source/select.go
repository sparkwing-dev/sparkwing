package source

import (
	"context"
	"errors"
	"os"
	"path"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	workspace, err := os.MkdirTemp(options.Workspace, "selection-")
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(workspace)) }()
	spec.SHA, spec.PipelineSHA, spec.PipelineDir, spec.CommittedCheckout = sha, "", ".", true
	options.Workspace, options.PreparedCheckout, options.LocalReplica = workspace, "", false
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
	content, err := root.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return content, err == nil, err
}
