package backends

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
)

// DocsURL is the public docs entry referenced by the deprecation
// warning. Kept here so callers stay in sync.
const DocsURL = "https://sparkwing.dev/docs/backends"

// LegacyURLToSpec converts a legacy SPARKWING_LOG_STORE or
// SPARKWING_ARTIFACT_STORE URL ("fs:///abs/path", "s3://bucket/prefix")
// into a typed Spec. Returns (nil, false) for malformed values so
// callers can decide whether to warn or skip silently -- the legacy
// code path treated malformed values as no-op and we preserve that
// to avoid breaking older CI configs on the boundary.
func LegacyURLToSpec(raw string) (*Spec, bool) {
	scheme, rest, err := splitScheme(raw)
	if err != nil {
		return nil, false
	}
	switch scheme {
	case "fs":
		path, err := fsPath(rest)
		if err != nil {
			return nil, false
		}
		return &Spec{Type: TypeFilesystem, Path: path}, true
	case "s3":
		bucket, prefix, err := s3BucketPrefix(rest)
		if err != nil {
			return nil, false
		}
		return &Spec{Type: TypeS3, Bucket: bucket, Prefix: prefix}, true
	default:
		return nil, false
	}
}

// BuiltinEnvironments returns the auto-detect rules every install
// gets for free: gha and kubernetes. Users override per-surface by
// declaring the same environment name in backends.yaml.
func BuiltinEnvironments() File {
	return File{
		Environments: map[string]Environment{
			"gha": {
				Name: "gha",
				Detect: Detect{
					EnvVar: "GITHUB_ACTIONS",
					Equals: "true",
				},
			},
			"kubernetes": {
				Name: "kubernetes",
				Detect: Detect{
					EnvVar:  "KUBERNETES_SERVICE_HOST",
					Present: true,
				},
				Surfaces: Surfaces{
					Cache: &Spec{Type: TypeController},
					Logs:  &Spec{Type: TypeController},
				},
			},
		},
	}
}

// shimWarned ensures the deprecation warning fires exactly once
// per process.
var shimWarned sync.Once

// ApplyLegacyEnvShim translates SPARKWING_LOG_STORE and
// SPARKWING_ARTIFACT_STORE into typed entries on a synthetic
// backends.File that sits underneath the supplied file. The file's
// own defaults still win per-surface; the shim only fills blanks.
// A one-shot deprecation warning is printed on stderr the first
// time either variable is observed.
func ApplyLegacyEnvShim(file File) File {
	logStore := os.Getenv("SPARKWING_LOG_STORE")
	artStore := os.Getenv("SPARKWING_ARTIFACT_STORE")
	if logStore == "" && artStore == "" {
		return file
	}
	shimWarned.Do(func() {
		fmt.Fprintf(os.Stderr,
			"warn: SPARKWING_LOG_STORE / SPARKWING_ARTIFACT_STORE are deprecated; declare cache and logs in .sparkwing/backends.yaml. See %s\n",
			DocsURL)
	})
	shim := File{}
	if logStore != "" {
		if spec, ok := LegacyURLToSpec(logStore); ok {
			shim.Defaults.Logs = spec
		}
	}
	if artStore != "" {
		if spec, ok := LegacyURLToSpec(artStore); ok {
			shim.Defaults.Cache = spec
		}
	}
	return Merge(file, shim)
}

// ResolveWithEnv loads backends.yaml (repo + user merge), layers in
// BuiltinEnvironments, then applies ApplyLegacyEnvShim. Returns the
// fully-prepared File suitable for DetectEnvironment + Effective.
func ResolveWithEnv(sparkwingDir string) (File, error) {
	file, err := Resolve(sparkwingDir)
	if err != nil {
		return File{}, err
	}
	file = Merge(file, BuiltinEnvironments())
	file = ApplyLegacyEnvShim(file)
	return file, nil
}

// --- URL parsing helpers, kept here so pkg/backends has no
// dependency on pkg/storage. Mirrors pkg/storage/storeurl's
// splitting logic.

func splitScheme(raw string) (scheme, rest string, err error) {
	if raw == "" {
		return "", "", errors.New("empty URL")
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return "", "", fmt.Errorf("missing scheme:// in %q", raw)
	}
	return scheme, rest, nil
}

func fsPath(rest string) (string, error) {
	if rest == "" {
		return "", errors.New("fs:// requires a path")
	}
	if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "~") {
		return "", fmt.Errorf("fs path must be absolute, got %q", rest)
	}
	if strings.HasPrefix(rest, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		rest = home + rest[1:]
	}
	return rest, nil
}

func s3BucketPrefix(rest string) (bucket, prefix string, err error) {
	u, err := url.Parse("s3://" + rest)
	if err != nil {
		return "", "", fmt.Errorf("parse s3 url: %w", err)
	}
	if u.Host == "" {
		return "", "", errors.New("s3:// requires a bucket")
	}
	prefix = strings.TrimPrefix(u.Path, "/")
	prefix = strings.TrimSuffix(prefix, "/")
	return u.Host, prefix, nil
}
