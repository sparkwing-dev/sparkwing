package backend

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// FromSpecs picks the right dashboard Backend implementation for the
// resolved state, logs, and (optionally) artifact specs, populates the
// Capabilities tags from the spec types, and returns a Closer that
// owns the underlying state-store / log-store connections. Callers
// MUST defer the returned Closer; long-running web servers should
// open backends once at startup and reuse them across requests.
//
// Dispatch rule:
//
//	state.type ∈ {sqlite, postgres, mysql} -> StoreBackend over *store.Store
//	state.type ∈ {s3, gcs, azure-blob}      -> S3Backend over the ArtifactStore
//	state.type == controller                -> ClientBackend over *client.Client
//
// Each branch resolves its own log store on the side via OpenLogStoreFromSpec
// when logsSpec is non-nil; otherwise log reads fall back to the impl's
// default (disk reads under paths.RunDir for StoreBackend, empty for the
// HTTP impls).
//
// artifactsSpec is only consulted on the object-store state branch; it
// is the artifact store the S3Backend reads NDJSON dumps from. For
// most deployments artifactsSpec mirrors stateSpec — the same bucket
// hosts state, logs, and cache — but they can diverge.
func FromSpecs(
	ctx context.Context,
	stateSpec, logsSpec, artifactsSpec *backends.Spec,
	paths orchestrator.Paths,
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
		spec := stateSpec
		if artifactsSpec != nil {
			spec = artifactsSpec
		}
		art, err := storeurl.OpenArtifactStoreFromSpec(ctx, *spec, profileLookup)
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

// capabilitiesFor builds the Capabilities body the dashboard advertises
// at /api/v1/capabilities. The Storage tags are drawn from the resolved
// spec types so the SPA can adapt UI hints; the feature set is held
// constant per backend family because the dashboard's read API is
// uniform.
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

// ParseInlineSpec turns a compact URL-style backend reference into a
// backends.Spec. Accepts:
//
//	sqlite:///abs/path/state.db
//	postgres://user:pass@host:5432/db?sslmode=disable
//	postgresql://...                              (alias for postgres)
//	s3://bucket-name/optional/prefix
//	gcs://bucket-name/optional/prefix
//	azure-blob://bucket-name/optional/prefix
//	controller://profile-name
//	fs:///path/to/dir
//	stdout:                                       (logs only)
//
// Empty input returns (nil, nil) so callers can distinguish "no
// override" from "parse error". The returned Spec is fed straight into
// the storeurl factories; validation rules from pkg/backends apply at
// open time.
func ParseInlineSpec(s string) (*backends.Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	// stdout: has no path component; handle before url.Parse to avoid
	// fighting the bare-scheme edge case.
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
		// Hand the original URL through; the pgx driver parses it.
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
