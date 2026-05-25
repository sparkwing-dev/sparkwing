package profile

import (
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// SpecString stringifies a backend spec for display. It is the single
// source of truth shared by the `sparkwing profile` introspection
// command and the run_start envelope, so both agree byte-for-byte (e.g.
// `controller://prod`). It never emits a postgres/mysql DSN URL (those
// carry credentials); only the type and an optional url_source
// indirection.
func SpecString(s *backends.Spec) string {
	if s == nil {
		return "-"
	}
	switch s.Type {
	case backends.TypeSQLite:
		if s.Path != "" {
			return "sqlite:" + s.Path
		}
		return "sqlite"
	case backends.TypeS3, backends.TypeGCS, backends.TypeAzureBlob:
		out := s.Type + "://" + s.Bucket
		if s.Prefix != "" {
			out += "/" + s.Prefix
		}
		return out
	case backends.TypeFilesystem:
		return "filesystem:" + s.Path
	case backends.TypeController:
		return "controller://" + s.Controller
	case backends.TypePostgres, backends.TypeMySQL:
		if s.URLSource != "" {
			return s.Type + ":" + s.URLSource
		}
		return s.Type
	case backends.TypeStdout:
		return "stdout"
	default:
		return s.Type
	}
}

// SurfaceStrings renders the profile's effective state/logs/cache as the
// strings shown by `sparkwing profile` and the run_start envelope. It
// mirrors the orchestrator's profileSurfaceSpecs precedence (explicit
// surfaces > controller implication > local sqlite fallback) without
// filling concrete paths, so the output reflects what the profile
// declares. Nil-safe.
func (p *Profile) SurfaceStrings() (state, logs, cache string) {
	if p == nil {
		return "-", "-", "-"
	}
	surf := p.Surfaces()
	if surf.State == nil && surf.Cache == nil && surf.Logs == nil && p.Controller != "" {
		c := "controller://" + p.Name
		return c, c, c
	}
	state = SpecString(surf.State)
	if surf.State == nil && p.Controller == "" {
		// Bare profile: the run path falls back to local SQLite state
		// with no shared logs/cache.
		state = "sqlite"
	}
	return state, SpecString(surf.Logs), SpecString(surf.Cache)
}

// DisplayDefaultPath returns the profiles.yaml path resolved by
// DefaultPath with a leading $HOME collapsed to ~ for display. Best
// effort: returns "profiles.yaml" when the path can't be resolved.
func DisplayDefaultPath() string {
	path, err := DefaultPath()
	if err != nil || path == "" {
		return "profiles.yaml"
	}
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
