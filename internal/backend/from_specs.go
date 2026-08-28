package backend

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

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
	if logsSpec != nil {
		ls, err := storeurl.OpenLogStoreFromSpec(ctx, *logsSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("logs backend: %w", err)
		}
		logStore = ls
	}

	switch stateSpec.Type {
	case backends.TypeSQLite, backends.TypePostgres, backends.TypeMySQL:
		ss, err := storeurl.OpenStateStoreFromSpec(ctx, *stateSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("state backend: %w", err)
		}
		st, ok := ss.(*store.Store)
		if !ok {
			return nil, nopCloser{}, fmt.Errorf("state backend type=%s did not return *store.Store", stateSpec.Type)
		}
		b := NewStoreBackend(st, paths, logStore)
		b.SetCapabilities(capabilitiesFor(stateSpec, logsSpec, artifactsSpec, false))
		return b, &multiCloser{closers: []io.Closer{st}}, nil

	case backends.TypeS3, backends.TypeGCS, backends.TypeAzureBlob:
		art, err := storeurl.OpenArtifactStoreFromSpec(ctx, *stateSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("artifact backend: %w", err)
		}
		b := NewS3Backend(art, logStore)
		b.SetCapabilities(capabilitiesFor(stateSpec, logsSpec, artifactsSpec, true))
		return b, nopCloser{}, nil

	case backends.TypeController:
		ss, err := storeurl.OpenStateStoreFromSpec(ctx, *stateSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("state backend: %w", err)
		}
		c, ok := ss.(*client.Client)
		if !ok {
			return nil, nopCloser{}, fmt.Errorf("state backend type=controller did not return *client.Client")
		}
		b := NewClientBackend(c, logStore)
		b.SetCapabilities(capabilitiesFor(stateSpec, logsSpec, artifactsSpec, false))
		return b, &multiCloser{closers: []io.Closer{c}}, nil

	default:
		return nil, nopCloser{}, fmt.Errorf("state backend type %q not supported by the dashboard", stateSpec.Type)
	}
}

func capabilitiesFor(state, logs, artifacts *backends.Spec, readOnly bool) Capabilities {
	mode := "local"
	switch state.Type {
	case backends.TypePostgres, backends.TypeMySQL:
		mode = "shared-db"
	case backends.TypeS3, backends.TypeGCS, backends.TypeAzureBlob:
		mode = state.Type + "-only"
	case backends.TypeController:
		mode = "cluster"
	}
	tag := func(spec *backends.Spec, def string) string {
		if spec == nil {
			return def
		}
		return spec.Type
	}
	c := Capabilities{
		Mode: mode,
		Storage: CapabilitiesStorage{
			Runs:      state.Type,
			Logs:      tag(logs, ""),
			Artifacts: tag(artifacts, ""),
		},
		Features: []string{"pipelines", "runs", "logs"},
		ReadOnly: readOnly,
	}
	switch state.Type {
	case backends.TypeSQLite, backends.TypePostgres, backends.TypeMySQL, backends.TypeController:
		c.Features = append(c.Features, "secrets", "approvals", "cross-pipeline-refs")
	}
	return c
}

func ParseInlineSpec(s string) (*backends.Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if s == "stdout:" || s == "stdout://" {
		return &backends.Spec{Type: backends.TypeStdout}, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("parse spec %q: %w", s, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "sqlite":
		path := u.Path
		if u.Host != "" {
			path = "/" + u.Host + u.Path
		}
		return &backends.Spec{Type: backends.TypeSQLite, Path: path}, nil
	case "postgres", "postgresql":
		return &backends.Spec{Type: backends.TypePostgres, URL: s}, nil
	case "mysql":
		return &backends.Spec{Type: backends.TypeMySQL, URL: s}, nil
	case "s3", "gcs", "azure-blob":
		ty := strings.ToLower(u.Scheme)
		bucket := u.Host
		prefix := strings.TrimPrefix(u.Path, "/")
		return &backends.Spec{Type: ty, Bucket: bucket, Prefix: prefix}, nil
	case "controller":
		profile := u.Host
		if profile == "" {
			profile = strings.TrimPrefix(u.Path, "/")
		}
		if profile == "" {
			return nil, fmt.Errorf("controller spec %q is missing a profile name", s)
		}
		return &backends.Spec{Type: backends.TypeController, Controller: profile}, nil
	case "fs", "file", "filesystem":
		path := u.Path
		if u.Host != "" {
			path = "/" + u.Host + u.Path
		}
		return &backends.Spec{Type: backends.TypeFilesystem, Path: path}, nil
	case "stdout":
		return &backends.Spec{Type: backends.TypeStdout}, nil
	default:
		return nil, fmt.Errorf("unknown spec scheme %q (expected sqlite, postgres, s3, gcs, azure-blob, controller, fs, stdout)", u.Scheme)
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
