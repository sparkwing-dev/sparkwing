package backend

import (
	"context"
	"fmt"
	"io"

	swpaths "github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func FromSpecs(
	ctx context.Context,
	stateSpec, logsSpec, artifactsSpec *backends.Spec,
	paths swpaths.Paths,
	profileLookup storeurl.ProfileLookup,
) (Backend, io.Closer, error) {
	if stateSpec == nil {
		return nil, nopCloser{}, fmt.Errorf("state spec is required")
	}

	var logStore storage.LogStore
	var logCloser []io.Closer
	if logsSpec != nil {
		ls, err := storeurl.OpenLogStoreFromSpec(ctx, *logsSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("logs backend: %w", err)
		}
		logStore = ls
		// safety: a batching logs backend holds its last lines until Close,
		// so the caller's shutdown has to reach it.
		if c, ok := ls.(io.Closer); ok {
			logCloser = append(logCloser, c)
		}
	}

	switch stateSpec.Type {
	case backends.TypeSQLite:
		ss, err := storeurl.OpenStateStoreFromSpec(ctx, *stateSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("state backend: %w", err)
		}
		st, ok := ss.(*store.Store)
		if !ok {
			return nil, nopCloser{}, fmt.Errorf("state backend type=%s did not return *store.Store", stateSpec.Type)
		}
		b := NewStoreBackend(st, paths, logStore)
		b.SetCapabilities(capabilitiesFor(stateSpec, logsSpec, artifactsSpec))
		return b, &multiCloser{closers: append([]io.Closer{st}, logCloser...)}, nil

	case backends.TypeController:
		ss, err := storeurl.OpenStateStoreFromSpec(ctx, *stateSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("state backend: %w", err)
		}
		c, ok := ss.(*client.Client)
		if !ok {
			return nil, nopCloser{}, fmt.Errorf("state backend type=controller did not return *client.Client")
		}
		// safety: the controller serves every node's log from its segments, so a reader of a controller profile
		// asks it whatever logs surface the profile names for its own runs.
		if logsSpec == nil || logsSpec.Type == backends.TypeController {
			logStore = c.NodeLogs()
		}
		b := NewClientBackend(c, logStore)
		b.SetCapabilities(capabilitiesFor(stateSpec, logsSpec, artifactsSpec))
		return b, &multiCloser{closers: append([]io.Closer{c}, logCloser...)}, nil

	default:
		return nil, nopCloser{}, fmt.Errorf("state backend type %q not supported by the dashboard", stateSpec.Type)
	}
}

func capabilitiesFor(state, logs, artifacts *backends.Spec) Capabilities {
	mode := "local"
	if state.Type == backends.TypeController {
		mode = "cluster"
	}
	tag := func(spec *backends.Spec, def string) string {
		if spec == nil {
			return def
		}
		return spec.Type
	}
	return Capabilities{
		Mode: mode,
		Storage: CapabilitiesStorage{
			Runs:      state.Type,
			Logs:      tag(logs, ""),
			Artifacts: tag(artifacts, ""),
		},
		Features: []string{"pipelines", "runs", "logs", "secrets", "approvals", "cross-pipeline-refs"},
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

type multiCloser struct {
	closers []io.Closer
}

func (m *multiCloser) Close() error {
	var firstErr error
	for _, c := range m.closers {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
