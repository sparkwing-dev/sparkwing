package sdkcompat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

const sdkModule = "github.com/sparkwing-dev/sparkwing"

// Manifest names immutable released artifacts independently for each language.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Fixtures      []Fixture `json:"fixtures"`
}

// Fixture describes one source directory and the released SDK it consumes.
type Fixture struct {
	ID              string    `json:"id"`
	Language        string    `json:"language"`
	Version         string    `json:"version"`
	Directory       string    `json:"directory"`
	Entrypoint      string    `json:"entrypoint"`
	Artifact        Artifact  `json:"artifact"`
	Toolchain       Toolchain `json:"toolchain"`
	WireMajor       int       `json:"wire_major"`
	Capabilities    []string  `json:"capabilities"`
	FixtureRevision int       `json:"fixture_revision"`
	ExpectedOutcome string    `json:"expected_outcome"`
}

// Artifact identifies the published module or npm tarball and its checksum.
type Artifact struct {
	Source   string `json:"source"`
	Checksum string `json:"checksum"`
}

// Toolchain records the compiler or runtime contract of a frozen fixture.
type Toolchain struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Options selects shared immutable preparation storage and cold rebuilding.
type Options struct {
	CacheDir string
	Cold     bool
}

// Prepared contains the real process entrypoint and raw preparation evidence.
type Prepared struct {
	Command    []string          `json:"command"`
	Dir        string            `json:"directory"`
	CacheHit   bool              `json:"cache_hit"`
	DurationMS int64             `json:"duration_ms"`
	Key        string            `json:"cache_key"`
	Toolchain  map[string]string `json:"toolchain"`
	Downloads  string            `json:"download_cache"`
}

// LoadManifest reads the released fixture inventory under root.
func LoadManifest(root string) (Manifest, error) {
	var result Manifest
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode SDK compatibility manifest: %w", err)
	}
	if result.SchemaVersion != 1 || len(result.Fixtures) == 0 {
		return result, fmt.Errorf("SDK compatibility manifest needs schema 1 and released fixtures")
	}
	ids := make(map[string]bool)
	for _, fixture := range result.Fixtures {
		if ids[fixture.ID] || fixture.ID == "" || fixture.FixtureRevision < 1 || fixture.WireMajor < 1 {
			return result, fmt.Errorf("invalid SDK compatibility fixture %q", fixture.ID)
		}
		ids[fixture.ID] = true
	}
	return result, nil
}

// Prepare compiles or installs a released fixture without changing its frozen files.
// Cold bypasses prepared artifacts while retaining ordinary compiler/download caches.
func Prepare(ctx context.Context, root string, fixture Fixture, options Options) (result Prepared, prepareErr error) {
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	if !filepath.IsAbs(options.CacheDir) {
		return result, fmt.Errorf("SDK compatibility cache must be an absolute path")
	}
	if !filepath.IsLocal(fixture.Directory) {
		return result, fmt.Errorf("fixture directory %q must stay under the fixture root", fixture.Directory)
	}
	source := filepath.Join(root, fixture.Directory)
	files, err := readSources(source)
	if err != nil {
		return result, err
	}
	if err := validateArtifact(fixture, files); err != nil {
		return result, err
	}
	tools, err := toolVersions(ctx, fixture)
	if err != nil {
		return result, err
	}
	keyData, err := json.Marshal(struct {
		Format   int
		Fixture  Fixture
		Files    map[string][]byte
		Tools    map[string]string
		Platform string
	}{1, fixture, files, tools, runtime.GOOS + "/" + runtime.GOARCH})
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(keyData)
	result.Key = hex.EncodeToString(digest[:])
	result.Toolchain = tools
	result.Downloads = "shared-go-module-cache"
	if fixture.Language == "typescript" {
		result.Downloads = "shared-npm-cache"
		if options.Cold {
			result.Downloads = "private-npm-cache"
		}
	}
	destination := filepath.Join(options.CacheDir, result.Key)
	if !options.Cold {
		if preparedExists(destination, fixture) {
			result = preparedResult(result, destination, fixture)
			result.CacheHit = true
			return result, nil
		}
	}
	if err := os.MkdirAll(options.CacheDir, 0o700); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(options.CacheDir, ".prepare-")
	if err != nil {
		return result, err
	}
	defer func() { prepareErr = errors.Join(prepareErr, os.RemoveAll(stage)) }()
	for name, data := range files {
		path := filepath.Join(stage, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return result, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return result, err
		}
	}
	if fixture.Language == "go" {
		err = run(ctx, stage, "go", "build", "-mod=readonly", "-o", executableName(), fixture.Entrypoint)
	} else {
		args := []string{"ci", "--ignore-scripts", "--no-audit", "--no-fund"}
		if options.Cold {
			args = append(args, "--cache", filepath.Join(stage, ".npm-cache"), "--prefer-online")
		}
		err = run(ctx, stage, "npm", args...)
	}
	if err != nil {
		return result, fmt.Errorf("acquire/prepare released SDK %s: %w", fixture.ID, err)
	}
	for name, expected := range files {
		actual, err := os.ReadFile(filepath.Join(stage, name))
		if err != nil || !slices.Equal(actual, expected) {
			return result, fmt.Errorf("preparation changed frozen fixture %s/%s", fixture.ID, name)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, ".prepared"), []byte(result.Key), 0o600); err != nil {
		return result, err
	}
	if options.Cold {
		// perf: retain independently rebuilt evidence without evicting a warm reader's files.
		destination = stage + "-cold"
	}
	if err := os.Rename(stage, destination); err != nil && !preparedExists(destination, fixture) {
		return result, err
	}
	result = preparedResult(result, destination, fixture)
	return result, nil
}

func readSources(root string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("frozen fixture contains symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[name], err = os.ReadFile(path)
		return err
	})
	return files, err
}

func validateArtifact(fixture Fixture, files map[string][]byte) error {
	version := "v" + strings.TrimPrefix(fixture.Version, "v")
	if !semver.IsValid(version) || semver.Prerelease(version) != "" || fixture.Artifact.Checksum == "" {
		return fmt.Errorf("fixture %s must pin a stable released artifact and checksum", fixture.ID)
	}
	switch fixture.Language {
	case "go":
		if fixture.Artifact.Source != sdkModule || !strings.HasPrefix(fixture.Artifact.Checksum, "h1:") {
			return fmt.Errorf("fixture %s must identify the released Go module", fixture.ID)
		}
		module, err := modfile.Parse("go.mod", files["go.mod"], nil)
		if err != nil {
			return err
		}
		if len(module.Replace) != 0 {
			return fmt.Errorf("released fixture %s forbids local module replacements", fixture.ID)
		}
		for _, requirement := range module.Require {
			if requirement.Mod.Path == sdkModule && requirement.Mod.Version == fixture.Version {
				line := sdkModule + " " + fixture.Version + " " + fixture.Artifact.Checksum
				if slices.Contains(strings.Split(string(files["go.sum"]), "\n"), line) {
					return nil
				}
			}
		}
		return fmt.Errorf("fixture %s module/sum does not pin its released SDK", fixture.ID)
	case "typescript":
		var lock struct {
			Packages map[string]struct {
				Version   string `json:"version"`
				Resolved  string `json:"resolved"`
				Integrity string `json:"integrity"`
			} `json:"packages"`
		}
		if err := json.Unmarshal(files["package-lock.json"], &lock); err != nil {
			return err
		}
		artifact := lock.Packages["node_modules/@sparkwing/sdk"]
		if artifact.Version != fixture.Version || artifact.Integrity != fixture.Artifact.Checksum || artifact.Resolved != fixture.Artifact.Source || !strings.HasPrefix(artifact.Integrity, "sha512-") {
			return fmt.Errorf("fixture %s lock does not pin its published npm integrity", fixture.ID)
		}
		return nil
	default:
		return fmt.Errorf("unknown fixture language %q", fixture.Language)
	}
}

func toolVersions(ctx context.Context, fixture Fixture) (map[string]string, error) {
	commands := map[string][]string{"go": {"version"}}
	if fixture.Language == "typescript" {
		commands = map[string][]string{"node": {"--version"}, "npm": {"--version"}}
	}
	versions := make(map[string]string)
	for name, args := range commands {
		cmd := procenv.ControlCommandContext(ctx, preparationEnvironment(), name, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("resolve %s toolchain: %w: %s", name, err, output)
		}
		versions[name] = strings.TrimSpace(string(output))
	}
	if fixture.Language == "typescript" {
		minimum := "v" + strings.TrimPrefix(strings.TrimPrefix(fixture.Toolchain.Version, ">="), "v")
		if strings.Count(minimum, ".") == 1 {
			minimum += ".0"
		}
		if !semver.IsValid(minimum) || semver.Compare(versions["node"], minimum) < 0 {
			return nil, fmt.Errorf("fixture %s requires Node %s, found %s", fixture.ID, fixture.Toolchain.Version, versions["node"])
		}
	}
	return versions, nil
}

func run(ctx context.Context, directory, command string, args ...string) error {
	cmd := procenv.ControlCommandContext(ctx, preparationEnvironment(), command, args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", command, strings.Join(args, " "), err, output)
	}
	return nil
}

func preparationEnvironment() []string {
	result := slices.DeleteFunc(os.Environ(), func(value string) bool {
		key, _, _ := strings.Cut(value, "=")
		return key == "GOWORK" || key == "GOFLAGS"
	})
	return append(result, "GOWORK=off", "GOFLAGS=-buildvcs=false")
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "pipeline.exe"
	}
	return "pipeline"
}

func preparedExists(directory string, fixture Fixture) bool {
	if _, err := os.Stat(filepath.Join(directory, ".prepared")); err != nil {
		return false
	}
	path := filepath.Join(directory, executableName())
	if fixture.Language == "typescript" {
		path = filepath.Join(directory, "node_modules", "@sparkwing", "sdk", "package.json")
	}
	_, err := os.Stat(path)
	return err == nil
}

func preparedResult(result Prepared, directory string, fixture Fixture) Prepared {
	result.Dir = directory
	result.Command = []string{filepath.Join(directory, executableName())}
	if fixture.Language == "typescript" {
		result.Command = []string{"node", filepath.Join(directory, fixture.Entrypoint)}
	}
	return result
}
