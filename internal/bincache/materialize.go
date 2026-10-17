package bincache

import (
	"context"
	"fmt"
)

// MaterializePipeline prepares effective module checksums and returns a leased executable.
func MaterializePipeline(ctx context.Context, dir string, options CompileOptions) (*Lease, bool, error) {
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
	if err := RunGo(ctx, dir, args, preparation); err != nil {
		return nil, false, fmt.Errorf("prepare pipeline dependencies: %w", err)
	}
	key, parts, err := ExplainCacheKey(dir, options.Environment)
	if err != nil {
		return nil, false, err
	}
	entry, err := PipelineEntry(key)
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

// Key returns the effective build-input key held by the lease.
func (l *Lease) Key() string { return l.entry.key }
