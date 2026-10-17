package main

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type localSourceSelection struct {
	identity  clisurface.PipelineIdentity
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
	reader := triggerCheckout{directory: root, environment: environment}
	sha := ""
	if flags.ref != "" {
		sha, err = reader.read(ctx, "rev-parse", "--verify", "--end-of-options", flags.ref+"^{commit}")
		if err != nil {
			return localSourceSelection{}, err
		}
	}
	execution := &source.Entry{Directory: directory}
	acquireExecution := func() error {
		selected, err := source.PrepareCheckout(ctx, source.Spec{RepoKey: string(repository.Key), LocalCheckout: root, PipelineDir: filepath.Base(directory), SHA: sha, CommittedCheckout: flags.ref != ""}, source.Options{Workspace: filepath.Join(home, "workspaces"), Base: procenv.Base{Local: true, Captured: captured, Masker: masker}})
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
	revision, err := triggerPipelineRevision(ctx, reader, cfg, pipeline, flags.pipelineRef, "")
	if err != nil {
		return localSourceSelection{}, err
	}
	if flags.ref == "" && revision != "" {
		if err := acquireExecution(); err != nil {
			return localSourceSelection{}, err
		}
	}
	working := flags.ref == "" && revision == ""
	if revision == "" {
		revision = execution.SHA
	}
	return localSourceSelection{identity: identity, execution: execution, git: runspec.Git{Repository: repository, SHA: execution.SHA, Ref: flags.ref}, source: runspec.Source{Ref: revision, Entrypoint: identity.Entrypoint, WorkingTree: working, CommittedCheckout: flags.ref != "", Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: filepath.Base(directory)}, NoBincache: flags.noBincache, HashAllFiles: flags.hashAllFiles}}, nil
}

func (s localSourceSelection) checkout(ctx context.Context, home string, captured []string) (*source.Entry, error) {
	if s.source.WorkingTree && s.git.SHA == "" {
		return &source.Entry{Directory: s.execution.Directory}, nil
	}
	spec, err := source.ForRequest(runspec.SealedRequest{Request: runspec.Request{Git: s.git, Source: s.source}})
	if err != nil {
		return nil, err
	}
	return source.PrepareCheckout(ctx, spec, source.Options{Workspace: filepath.Join(home, "workspaces"), Base: procenv.Base{Local: true, Captured: captured, Masker: secrets.NewMasker()}})
}
