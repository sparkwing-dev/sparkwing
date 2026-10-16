package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type triggerInspection struct {
	options     source.Options
	acquire     func(context.Context, source.Spec, source.Options) (*source.Entry, error)
	close       func(*source.Entry) error
	buildEnv    func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
	exec        func(context.Context, string, []string, string, ...string) ([]byte, error)
	diagnostics io.Writer
}

func inspectTriggerSource(ctx context.Context, selection preparedTriggerSource, captured []string, deps triggerInspection) (described []nodeproto.PipelineDescription, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	masker := secrets.NewMasker()
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	options := deps.options
	options.Base = procenv.Base{Local: true, Captured: captured, Masker: masker}
	spec := source.Spec{
		RepoKey: string(selection.git.Repository.Key), RemoteURL: selection.source.Location.RemoteURL,
		SHA: selection.source.Ref, PipelineDir: selection.source.Location.PipelineDir, LocalCheckout: selection.source.Location.LocalCheckout,
	}
	if selection.source.BundleDigest.Set {
		spec.BundleDigest = selection.source.BundleDigest.Value
	}
	if selection.git.DefaultBranch.Set {
		spec.DefaultBranch = selection.git.DefaultBranch.Value
	}
	acquire := deps.acquire
	if acquire == nil {
		acquire = source.Acquire
	}
	entry, err := acquire(ctx, spec, options)
	if entry != nil {
		defer func() {
			closeEntry := deps.close
			if closeEntry == nil {
				closeEntry = (*source.Entry).Close
			}
			resultErr = errors.Join(resultErr, closeEntry(entry), ctx.Err())
		}()
	}
	if err := errors.Join(err, ctx.Err()); err != nil {
		return nil, err
	}
	if entry == nil || entry.SHA != spec.SHA || entry.RepoKey != spec.RepoKey || !filepath.IsAbs(entry.Directory) ||
		!filepath.IsAbs(entry.Engine.Executable) || entry.Engine.Identity.Version == "" || entry.Engine.Identity.ManifestDigest == "" ||
		entry.Engine.AssetDigest == "" || entry.Engine.Identity.Pin.SDK != options.SDK || entry.Engine.Target != options.Target {
		return nil, &apierr.Error{Code: apierr.ProtocolMismatch, Message: "trigger acquisition returned a different source or incomplete verified engine"}
	}
	buildEnv := deps.buildEnv
	if buildEnv == nil {
		buildEnv = procenv.Build
	}
	env, err := buildEnv(procenv.HostChild, options.Base, procenv.Runtime{})
	if err := errors.Join(err, ctx.Err()); err != nil {
		return nil, err
	}
	diagnostics := deps.diagnostics
	if diagnostics == nil {
		diagnostics = os.Stderr
	}
	masked := &installDiagnostics{out: diagnostics, stream: masker.Stream()}
	if err := errors.Join(writeEngineNotices(masked, entry.Engine.Notices), masked.close(), ctx.Err()); err != nil {
		return nil, err
	}
	execute := deps.exec
	if execute == nil {
		execute = executeUpgradeCheck
	}
	raw, err := execute(ctx, entry.Directory, env, entry.Engine.Executable, "pipeline", "describe", "--pipeline="+selection.pipeline, "--output=json")
	if err := errors.Join(err, ctx.Err()); err != nil {
		return nil, err
	}
	description, err := triggerDescription(raw, selection)
	if err != nil {
		return nil, err
	}
	return []nodeproto.PipelineDescription{description}, nil
}

func triggerDescription(raw []byte, selection preparedTriggerSource) (nodeproto.PipelineDescription, error) {
	var result struct {
		nodeproto.PipelineDescription
		Entrypoint string `json:"entrypoint"`
	}
	invalid := &apierr.Error{Code: apierr.ProtocolMismatch, Message: "selected engine returned a different pipeline binding or malformed description"}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return nodeproto.PipelineDescription{}, invalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nodeproto.PipelineDescription{}, invalid
	}
	if result.Entrypoint == "" {
		result.Entrypoint = result.Name
	}
	if result.Name != selection.pipeline || result.Entrypoint != selection.source.Entrypoint {
		return nodeproto.PipelineDescription{}, invalid
	}
	return result.PipelineDescription, nil
}
