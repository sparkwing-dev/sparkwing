package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type localSourceSelection struct {
	identity  clisurface.PipelineIdentity
	project   *projectconfig.Config
	git       runspec.Git
	source    runspec.Source
	execution *source.Entry
}

func selectLocalRunSource(ctx context.Context, home, directory, pipeline string, flags runFlags, captured []string, masker *secrets.Masker) (_ localSourceSelection, resultErr error) {
	root := filepath.Dir(directory)
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: root})
	if err != nil {
		return localSourceSelection{}, err
	}
	environment, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: captured, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return localSourceSelection{}, err
	}
	reader := triggerCheckout{directory: root, environment: slices.Collect(environment.Values())}
	sha := ""
	if flags.ref != "" {
		sha, err = reader.read(ctx, "rev-parse", "--verify", "--end-of-options", flags.ref+"^{commit}")
		if err != nil {
			return localSourceSelection{}, err
		}
	}
	execution := &source.Entry{Directory: directory}
	acquireExecution := func() error {
		selected, err := source.PrepareCheckout(ctx, source.Spec{RepoKey: string(repository.Key), LocalCheckout: root, PipelineDir: filepath.Base(directory), SHA: sha, CommittedCheckout: flags.ref != ""}, source.Options{Workspace: filepath.Join(home, "workspaces"), GitAccess: source.GitAccessOwner, Base: procenv.Base{Local: true, Captured: captured, Masker: masker}})
		if err == nil {
			execution = selected
		}
		return err
	}
	if flags.ref != "" {
		if err := acquireExecution(); err != nil {
			return localSourceSelection{}, err
		}
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, execution.Close())
		}
	}()
	cfg, err := projectconfig.Load(filepath.Join(execution.Directory, projectconfig.Filename))
	if err != nil {
		return localSourceSelection{}, err
	}
	identity, err := clisurface.ResolvePipeline(cfg, pipeline)
	if err != nil {
		return localSourceSelection{}, err
	}
	selected := runspec.Source{Entrypoint: identity.Entrypoint, Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: filepath.Base(directory)}, NoBincache: flags.noBincache, HashAllFiles: flags.hashAllFiles}
	if entry := cfg.Registry().Find(pipeline); entry != nil {
		selected.RequestedRef = entry.Source
	}
	git := runspec.Git{Repository: repository, SHA: sha, Ref: flags.ref}
	if flags.ref != "" || selected.RequestedRef != "" || flags.pipelineRef != "" {
		git, selected, err = source.ResolveRevisions(ctx, git, selected, flags.pipelineRef, func(ref string) (string, error) {
			return reader.read(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		})
		if err != nil {
			return localSourceSelection{}, err
		}
	}
	if flags.ref == "" {
		// safety: a detached HEAD (exit 1) or a directory outside any repository
		// (exit 128) records no branch; source acquisition reports a missing repository.
		git.Ref, err = reader.read(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil && !gitExitCode(err, 1) && !gitExitCode(err, 128) {
			return localSourceSelection{}, err
		}
	}
	revision := selected.Ref
	if flags.ref == "" && revision != "" {
		if err := acquireExecution(); err != nil {
			return localSourceSelection{}, err
		}
	}
	working := flags.ref == "" && revision == ""
	if revision == "" {
		revision = execution.SHA
	}
	git.SHA = execution.SHA
	selected.Ref, selected.WorkingTree, selected.CommittedCheckout = revision, working, flags.ref != ""
	return localSourceSelection{identity: identity, project: cfg, execution: execution, git: git, source: selected}, nil
}

func (s localSourceSelection) checkout(ctx context.Context, home string, captured []string) (*source.Entry, error) {
	if s.source.WorkingTree && s.git.SHA == "" {
		return &source.Entry{Directory: s.execution.Directory}, nil
	}
	spec, err := source.ForRequest(runspec.SealedRequest{Request: runspec.Request{Git: s.git, Source: s.source}})
	if err != nil {
		return nil, err
	}
	return source.PrepareCheckout(ctx, spec, source.Options{Workspace: filepath.Join(home, "workspaces"), GitAccess: source.GitAccessOwner, Base: procenv.Base{Local: true, Captured: captured, Masker: secrets.NewMasker()}})
}

// safety: nil without sparkwing.yaml, so planning never calls a name unlisted.
func listedPipelines(project *projectconfig.Config) []string {
	if project == nil {
		return nil
	}
	names := make([]string, 0, len(project.Pipelines))
	for _, pipeline := range project.Pipelines {
		names = append(names, pipeline.Name)
	}
	return names
}

// safety: source git output is withheld because it can echo credentials, so
// whether the project sits outside any git repository comes from a separate
// local read of its directory.
func explainOutsideGit(ctx context.Context, root string, err error) error {
	if err == nil || ctx.Err() != nil || !strings.Contains(err.Error(), "source git ") {
		return err
	}
	environment, envErr := procenv.Build(procenv.Git, procenv.Base{Local: true}, procenv.Runtime{})
	if envErr != nil {
		return err
	}
	reader := triggerCheckout{directory: root, environment: slices.Collect(environment.Values())}
	if _, readErr := reader.read(ctx, "rev-parse", "--git-dir"); readErr != nil && strings.Contains(readErr.Error(), "not a git repository") {
		return &apierr.Error{Code: apierr.InvalidArgs, Message: fmt.Sprintf("%s is not a git repository; run `git init`, commit the project, then retry this run", root)}
	}
	return err
}
