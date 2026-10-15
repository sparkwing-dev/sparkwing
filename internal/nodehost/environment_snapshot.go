package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// CapturedEnvironmentLoader reads private values named by the immutable request.
// Hosts verify the returned names and digest before passing values to procenv.
type CapturedEnvironmentLoader interface {
	Load(context.Context, runspec.EnvSnapshot) ([]string, error)
}

// FileEnvironmentSnapshots retains local inputs across owner loss, retries and
// children. The directory belongs to an authorized local host; a digest grants
// no access. Values never enter the run spec or a shared planning-cache entry.
type FileEnvironmentSnapshots struct{ directory string }

// NewFileEnvironmentSnapshots secures the host-owned directory before storing values.
func NewFileEnvironmentSnapshots(directory string) (*FileEnvironmentSnapshots, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("captured environments require an absolute private directory")
	}
	if err := fssecure.EnsureDir(directory); err != nil {
		return nil, snapshotStorageError("prepare", err)
	}
	if err := fssecure.SecurePrivateDir(directory); err != nil {
		return nil, snapshotStorageError("protect", err)
	}
	return &FileEnvironmentSnapshots{directory: directory}, nil
}

// Capture saves host-selected inputs before intake seals their identity.
// procenv remains responsible for filtering values at every process boundary.
// Equal inputs share an identity, so inheriting a snapshot needs no mutable run-ID mapping.
func (s *FileEnvironmentSnapshots) Capture(ctx context.Context, environment []string) (seal runspec.EnvSnapshot, err error) {
	if err := context.Cause(ctx); err != nil {
		return seal, err
	}
	seal, environment, err = capturedEnvironment(environment)
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	// safety: native environment values can contain non-UTF-8 bytes; JSON strings would replace them.
	encoded := make([][]byte, 0, len(environment))
	for _, entry := range environment {
		encoded = append(encoded, []byte(entry))
	}
	data, err := json.Marshal(encoded)
	if err != nil {
		return runspec.EnvSnapshot{}, errors.New("cannot encode captured environment")
	}
	temporary, err := fssecure.MkdirPrivateTemp(s.directory, ".environment-")
	if err != nil {
		return runspec.EnvSnapshot{}, snapshotStorageError("create", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(temporary); removeErr != nil {
			err = errors.Join(err, snapshotStorageError("remove incomplete", removeErr))
		}
		if err != nil {
			seal = runspec.EnvSnapshot{}
		}
	}()
	file := filepath.Join(temporary, "snapshot")
	if err := fssecure.WriteFile(file, data); err != nil {
		return runspec.EnvSnapshot{}, snapshotStorageError("write", err)
	}
	if err := context.Cause(ctx); err != nil {
		return runspec.EnvSnapshot{}, err
	}
	if err := os.Rename(file, s.path(seal)); err != nil {
		return runspec.EnvSnapshot{}, snapshotStorageError("publish", err)
	}
	return seal, nil
}

// Load never consumes the snapshot: replacement hosts and child runs need the
// same values. Missing, corrupt or nonprivate files fail before process startup.
func (s *FileEnvironmentSnapshots) Load(ctx context.Context, seal runspec.EnvSnapshot) (environment []string, err error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if !planCacheDigestValid(seal.Digest) {
		return nil, errors.New("captured environment has no valid sealed identity")
	}
	file, err := fssecure.OpenPrivateConfig(s.path(seal))
	if err != nil {
		return nil, snapshotStorageError("open", err)
	}
	defer func() {
		err = errors.Join(err, snapshotStorageError("close", file.Close()))
		if err != nil {
			environment = nil
		}
	}()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, snapshotStorageError("read", err)
	}
	var encoded [][]byte
	if json.Unmarshal(data, &encoded) != nil || encoded == nil {
		return nil, errors.New("captured environment is malformed")
	}
	for _, entry := range encoded {
		environment = append(environment, string(entry))
	}
	environment, err = verifyCapturedEnvironment(ctx, seal, environment)
	return environment, err
}

func (s *FileEnvironmentSnapshots) path(seal runspec.EnvSnapshot) string {
	return filepath.Join(s.directory, string(seal.Digest)+".json")
}

func verifyCapturedEnvironment(ctx context.Context, expected runspec.EnvSnapshot, environment []string) ([]string, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	actual, normalized, err := capturedEnvironment(environment)
	if err != nil {
		return nil, err
	}
	if !planCacheDigestValid(expected.Digest) || actual.Digest != expected.Digest || !slices.Equal(actual.Names, expected.Names) {
		return nil, errors.New("captured environment differs from the sealed request")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return normalized, nil
}

func capturedBase(ctx context.Context, base procenv.Base, snapshots CapturedEnvironmentLoader, seal runspec.EnvSnapshot) (procenv.Base, error) {
	base.Captured, base.Ambient, base.SubmitEnvAllow = slices.Clone(base.Captured), slices.Clone(base.Ambient), slices.Clone(base.SubmitEnvAllow)
	if !base.Local {
		return base, context.Cause(ctx)
	}
	if !planCacheDigestValid(seal.Digest) {
		return procenv.Base{}, errors.New("local execution requires the sealed environment identity")
	}
	captured := base.Captured
	if snapshots != nil {
		expected := seal
		expected.Names = slices.Clone(seal.Names)
		var err error
		captured, err = snapshots.Load(ctx, expected)
		if err != nil {
			return procenv.Base{}, err
		}
	}
	var err error
	base.Captured, err = verifyCapturedEnvironment(ctx, seal, captured)
	if err != nil {
		return procenv.Base{}, err
	}
	return base, nil
}

func capturedEnvironment(environment []string) (runspec.EnvSnapshot, []string, error) {
	values := make(map[string]string, len(environment))
	seen := make(map[string]bool, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if !ok || name == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(value, 0) || seen[key] {
			return runspec.EnvSnapshot{}, nil, errors.New("captured environment contains an invalid or duplicate assignment")
		}
		seen[key], values[name] = true, value
	}
	seal, err := runspec.EnvironmentDigest(values)
	if err != nil {
		return runspec.EnvSnapshot{}, nil, errors.New("cannot identify captured environment")
	}
	normalized := make([]string, 0, len(values))
	for _, name := range seal.Names {
		normalized = append(normalized, name+"="+values[name])
	}
	return seal, normalized, nil
}

func snapshotStorageError(operation string, err error) error {
	if err == nil {
		return nil
	}
	// safety: paths contain hashes of secret-bearing values and must not enter diagnostics.
	var pathErr *os.PathError
	var linkErr *os.LinkError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	} else if errors.As(err, &linkErr) {
		err = linkErr.Err
	} else {
		return fmt.Errorf("%s captured environment failed", operation)
	}
	return fmt.Errorf("%s captured environment: %w", operation, err)
}

var _ CapturedEnvironmentLoader = (*FileEnvironmentSnapshots)(nil)
