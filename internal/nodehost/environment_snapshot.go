package nodehost

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
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
type FileEnvironmentSnapshots struct {
	directory string
	saltKey   []byte
}

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
	key, err := loadSnapshotSaltKey(directory)
	if err != nil {
		return nil, err
	}
	return &FileEnvironmentSnapshots{directory: directory, saltKey: key}, nil
}

const snapshotSaltKeyName = "salt.key"

// safety: the salt derives from a host-private key, so equal local inputs keep one identity
// while readers of the run cannot test guesses against its digest.
func loadSnapshotSaltKey(directory string) (_ []byte, err error) {
	path := filepath.Join(directory, snapshotSaltKeyName)
	if key, err := readSnapshotSaltKey(path); !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("cannot create captured environment salt key")
	}
	temporary, err := fssecure.MkdirPrivateTemp(directory, ".salt-")
	if err != nil {
		return nil, snapshotStorageError("create salt key", err)
	}
	defer func() {
		err = errors.Join(err, snapshotStorageError("remove incomplete salt key", os.RemoveAll(temporary)))
	}()
	file := filepath.Join(temporary, "key")
	if err := fssecure.WriteFile(file, key); err != nil {
		return nil, snapshotStorageError("write salt key", err)
	}
	if err := os.Link(file, path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, snapshotStorageError("publish salt key", err)
	}
	return readSnapshotSaltKey(path)
}

func readSnapshotSaltKey(path string) ([]byte, error) {
	file, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, snapshotStorageError("open salt key", err)
	}
	key, readErr := io.ReadAll(io.LimitReader(file, sha256.Size+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, snapshotStorageError("read salt key", err)
	}
	if len(key) != sha256.Size {
		return nil, errors.New("captured environment salt key is malformed")
	}
	return key, nil
}

func (s *FileEnvironmentSnapshots) salt(values map[string]string) string {
	names := slices.Sorted(maps.Keys(values))
	mac := hmac.New(sha256.New, s.saltKey)
	for _, name := range names {
		mac.Write([]byte(name + "=" + values[name] + "\x00"))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// Capture saves host-selected inputs before intake seals their identity.
// procenv remains responsible for filtering values at every process boundary.
// Equal inputs share an identity, so inheriting a snapshot needs no mutable run-ID mapping.
func (s *FileEnvironmentSnapshots) Capture(ctx context.Context, environment []string) (seal runspec.EnvSnapshot, err error) {
	if err := context.Cause(ctx); err != nil {
		return seal, err
	}
	values, err := capturedValues(environment)
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	seal, environment, err = capturedEnvironment(environment, s.salt(values))
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

// Identify names environment as Capture would without storing its values.
func (s *FileEnvironmentSnapshots) Identify(environment []string) (runspec.EnvSnapshot, error) {
	values, err := capturedValues(environment)
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	seal, _, err := capturedEnvironment(environment, s.salt(values))
	return seal, err
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
	if err == nil {
		err = s.verifyGateIndex(environment)
	}
	return environment, err
}

func (s *FileEnvironmentSnapshots) path(seal runspec.EnvSnapshot) string {
	return filepath.Join(s.directory, string(seal.Digest)+".json")
}

func verifyCapturedEnvironment(ctx context.Context, expected runspec.EnvSnapshot, environment []string) ([]string, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	actual, normalized, err := capturedEnvironment(environment, expected.Salt)
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
	base.Captured, base.Ambient, base.SubmitEnvAllow, base.Submitted = slices.Clone(base.Captured), slices.Clone(base.Ambient), slices.Clone(base.SubmitEnvAllow), slices.Clone(base.Submitted)
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

func capturedValues(environment []string) (map[string]string, error) {
	values := make(map[string]string, len(environment))
	seen := make(map[string]bool, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if !ok || name == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(value, 0) || seen[key] {
			return nil, errors.New("captured environment contains an invalid or duplicate assignment")
		}
		seen[key], values[name] = true, value
	}
	return values, nil
}

func capturedEnvironment(environment []string, salt string) (runspec.EnvSnapshot, []string, error) {
	values, err := capturedValues(environment)
	if err != nil {
		return runspec.EnvSnapshot{}, nil, err
	}
	seal, err := runspec.EnvironmentDigest(values, salt)
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

func (s *FileEnvironmentSnapshots) verifyGateIndex(environment []string) error {
	for _, item := range environment {
		path, ok := strings.CutPrefix(item, "SPARKWING_GATE_INDEX=")
		if !ok || path == "" {
			continue
		}
		if filepath.Dir(path) != s.directory {
			return errors.New("gate index is outside private snapshot storage")
		}
		file, err := fssecure.OpenPrivateConfig(path)
		if err != nil {
			return snapshotStorageError("open gate index", err)
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return snapshotStorageError("read gate index", err)
		}
		if filepath.Base(path) != fmt.Sprintf("index-%x", sha256.Sum256(data)) {
			return errors.New("gate index differs from its captured contents")
		}
	}
	return nil
}
