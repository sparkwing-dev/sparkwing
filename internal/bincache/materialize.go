package bincache

import (
	"context"
	"errors"
	"fmt"
)

// MaterializePipeline leases a cached executable or prepares its dependencies and compiles it.
func MaterializePipeline(ctx context.Context, dir string, options CompileOptions) (*Lease, bool, error) {
	if options.Environment == nil || options.Diagnostics == nil {
		return nil, false, errors.New("pipeline materialization requires a captured environment and diagnostic writer")
	}
	if _, err := ConfiguredLimits(); err != nil {
		return nil, false, fmt.Errorf("pipeline cache ceilings: %w", err)
	}
	key, parts, err := explainCacheKey(dir, options.Environment, options.HashAllFiles)
	if err != nil {
		return nil, false, err
	}
	entry, err := PipelineEntry(key)
	if err != nil {
		return nil, false, err
	}
	if lease, found, err := entry.Acquire(ctx); err != nil {
		return nil, false, err
	} else if found {
		lease.RecordUse(dir, parts)
		return lease, false, nil
	}
	if err := preparePipelineDependencies(ctx, dir, options); err != nil {
		return nil, false, fmt.Errorf("prepare pipeline dependencies: %w", err)
	}
	key, parts, err = explainCacheKey(dir, options.Environment, options.HashAllFiles)
	if err != nil {
		return nil, false, err
	}
	entry, err = PipelineEntry(key)
	if err != nil {
		return nil, false, err
	}
	lease, published, err := entry.AcquireOrMaterialize(ctx, func(path string) error { return CompilePipeline(ctx, dir, path, options) })
	if err != nil {
		return nil, false, err
	}
	lease.RecordUse(dir, parts)
	return lease, published, nil
}

func preparePipelineDependencies(ctx context.Context, dir string, options CompileOptions) error {
	args := []string{"list", "-deps", "-f", "{{if .Error}}{{.Error}}{{end}}"}
	preparation := options
	work, present := goWorkInScope(dir, options.Environment)
	if !present || !goWorkCovers(work, dir) {
		args = append(args, "-mod=mod")
		if present {
			preparation.Environment = withGoworkOff(options.Environment)
		}
		if overlay := EffectiveOverlay(dir, options.Environment); overlay != "" {
			args = append(args, "-modfile="+overlay)
		}
	}
	args = append(args, ".")
	return RunGo(ctx, dir, args, preparation)
}

// Key returns the effective build-input key held by the lease.
func (l *Lease) Key() string { return l.entry.key }
