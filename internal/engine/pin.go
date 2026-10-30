package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

// ReadPin resolves the repository SDK requirement without executing repository code.
func ReadPin(checkout string, sdk engineid.SDKKind) (engineid.Pin, error) {
	var pin engineid.Pin
	var err error
	switch sdk {
	case engineid.GoSDK:
		pin, err = readGoPin(checkout)
	case engineid.TypeScriptSDK:
		pin, err = readNpmPin(checkout)
	default:
		return engineid.Pin{}, invalid("unknown SDK kind", "Select go or typescript.")
	}
	if err != nil {
		return engineid.Pin{}, invalid(err.Error(), "Pin an exact SDK release or a version-stamped local TypeScript package and update its native lockfile.")
	}
	if _, err = VersionForPin(pin); err != nil {
		return engineid.Pin{}, err
	}
	pin.InputsDigest, err = pinInputsDigest(checkout, pin)
	return pin, err
}

// VersionForPin selects the exact release or installed engine for an SDK replacement.
func VersionForPin(pin engineid.Pin) (string, error) {
	if pin.SDK == engineid.GoSDK && pin.Replacement != "" && pin.Version != "" {
		return installedEngineVersion()
	}
	version := pin.Version
	switch pin.SDK {
	case engineid.GoSDK:
	case engineid.TypeScriptSDK:
		version = "v" + version
	default:
		return "", invalid("unknown SDK kind", "Select go or typescript.")
	}
	if !releaseVersion(version) {
		return "", invalid("SDK pin must identify an exact published release", "Use vMAJOR.MINOR.PATCH for Go or MAJOR.MINOR.PATCH for TypeScript; local and pseudo-version pins need a development workflow.")
	}
	return version, nil
}

func releaseVersion(version string) bool {
	return semver.IsValid(version) && semver.Canonical(version) == version && semver.Build(version) == "" && !module.IsPseudoVersion(version)
}

func readGoPin(dir string) (engineid.Pin, error) {
	body, err := readPinFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return engineid.Pin{}, err
	}
	file, err := modfile.Parse("go.mod", body, nil)
	if err != nil {
		return engineid.Pin{}, err
	}
	pin := engineid.Pin{SDK: engineid.GoSDK, Manifest: "go.mod"}
	for _, require := range file.Require {
		if require.Mod.Path == GoModule {
			if pin.Version != "" {
				return engineid.Pin{}, fmt.Errorf("duplicate SDK requirement")
			}
			pin.Version = require.Mod.Version
		}
	}
	for _, replace := range file.Replace {
		if replace.Old.Path == GoModule && (replace.Old.Version == "" || replace.Old.Version == pin.Version) {
			pin.Replacement = replace.New.String()
			if replace.Old.Version != "" {
				break
			}
		}
	}
	for _, exclude := range file.Exclude {
		if exclude.Mod.Path == GoModule && exclude.Mod.Version == pin.Version {
			return engineid.Pin{}, fmt.Errorf("SDK pin is excluded")
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "go.sum")); err == nil {
		pin.Lockfile = "go.sum"
	} else if !os.IsNotExist(err) {
		return engineid.Pin{}, err
	}
	return pin, nil
}

func sdkRequirement(body []byte) (string, error) {
	var manifest struct{ Dependencies, DevDependencies, PeerDependencies map[string]string }
	if err := decodeUniqueJSON(body, &manifest); err != nil {
		return "", err
	}
	var spec string
	for _, deps := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.PeerDependencies} {
		if value := deps[typeScriptPackage]; value != "" {
			if spec != "" && spec != value {
				return "", fmt.Errorf("conflicting SDK requirements")
			}
			spec = value
		}
	}
	if spec == "" {
		return "", fmt.Errorf("missing %s dependency", typeScriptPackage)
	}
	return spec, nil
}

func readNpmPin(dir string) (engineid.Pin, error) {
	body, err := readPinFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return engineid.Pin{}, err
	}
	spec, err := sdkRequirement(body)
	if err != nil {
		return engineid.Pin{}, err
	}
	pin := engineid.Pin{SDK: engineid.TypeScriptSDK, Manifest: "package.json"}
	var lockBody []byte
	for _, name := range []string{"npm-shrinkwrap.json", "package-lock.json"} {
		body, err := readPinFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return engineid.Pin{}, err
		}
		if pin.Lockfile != "" {
			return engineid.Pin{}, fmt.Errorf("conflicting npm lockfiles")
		}
		pin.Lockfile = name
		lockBody = body
	}
	if pin.Lockfile == "" {
		if releaseVersion("v" + spec) {
			pin.Version = spec
			return pin, nil
		}
		return engineid.Pin{}, fmt.Errorf("SDK range requires an exact npm lockfile resolution")
	}
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version                                         string `json:"version"`
			Resolved                                        string `json:"resolved"`
			Link                                            bool   `json:"link"`
			Dependencies, DevDependencies, PeerDependencies map[string]string
		} `json:"packages"`
		Dependencies map[string]struct {
			Version  string `json:"version"`
			Resolved string `json:"resolved"`
		} `json:"dependencies"`
	}
	if err := decodeUniqueJSON(lockBody, &lock); err != nil {
		return engineid.Pin{}, err
	}
	if lock.LockfileVersion < 1 || lock.LockfileVersion > 3 {
		return engineid.Pin{}, fmt.Errorf("unsupported npm lockfile version")
	}
	if lock.LockfileVersion == 1 {
		pin.Version = lock.Dependencies[typeScriptPackage].Version
	} else {
		pin.Version = lock.Packages["node_modules/"+typeScriptPackage].Version
		root := lock.Packages[""]
		rootBody, err := json.Marshal(root)
		if err != nil {
			return engineid.Pin{}, err
		}
		rootSpec, err := sdkRequirement(rootBody)
		if err != nil || rootSpec != spec {
			return engineid.Pin{}, fmt.Errorf("package manifest and npm lockfile disagree")
		}
		if legacy := lock.Dependencies[typeScriptPackage].Version; localNpmPath(spec) == "" && legacy != "" && legacy != pin.Version {
			return engineid.Pin{}, fmt.Errorf("conflicting SDK lockfile resolutions")
		}
	}
	if source := localNpmPath(spec); source != "" {
		entry := lock.Packages["node_modules/"+typeScriptPackage]
		resolved := entry.Resolved
		if lock.LockfileVersion == 1 {
			resolved = lock.Dependencies[typeScriptPackage].Resolved
			if resolved == "" {
				resolved = pin.Version
			}
		} else if entry.Link {
			pin.Version = lock.Packages[entry.Resolved].Version
		}
		if localNpmPath(resolved) == "" && !entry.Link {
			return engineid.Pin{}, fmt.Errorf("local SDK lockfile must resolve the manifest source")
		}
		resolved = strings.TrimPrefix(resolved, "file:")
		if localNpmFile(dir, resolved) != localNpmFile(dir, source) {
			return engineid.Pin{}, fmt.Errorf("local SDK lockfile resolves a different source")
		}
		if legacy := lock.Dependencies[typeScriptPackage].Resolved; legacy != "" && localNpmFile(dir, strings.TrimPrefix(legacy, "file:")) != localNpmFile(dir, source) {
			return engineid.Pin{}, fmt.Errorf("conflicting local SDK lockfile sources")
		}
		version, _, err := localNpmPackage(dir, source)
		if err != nil {
			return engineid.Pin{}, err
		}
		if lock.LockfileVersion != 1 && pin.Version != version {
			return engineid.Pin{}, fmt.Errorf("local SDK package version differs from its lockfile")
		}
		if legacy := lock.Dependencies[typeScriptPackage].Version; legacy != "" && legacy != version && localNpmFile(dir, localNpmPath(legacy)) != localNpmFile(dir, source) {
			return engineid.Pin{}, fmt.Errorf("conflicting local SDK lockfile resolutions")
		}
		pin.Version = version
	} else if !npmSatisfies(spec, pin.Version) {
		return engineid.Pin{}, fmt.Errorf("SDK lockfile resolution does not satisfy its manifest requirement")
	}
	return pin, nil
}

func pinInputsDigest(dir string, pin engineid.Pin) (string, error) {
	var buf bytes.Buffer
	paths := []string{pin.Manifest, pin.Lockfile}
	if pin.SDK == engineid.TypeScriptSDK {
		body, err := readPinFile(filepath.Join(dir, pin.Manifest))
		if err != nil {
			return "", err
		}
		spec, err := sdkRequirement(body)
		if err != nil {
			return "", err
		}
		if source := localNpmPath(spec); source != "" {
			_, path, err := localNpmPackage(dir, source)
			if err != nil {
				return "", err
			}
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		input := path
		if !filepath.IsAbs(input) {
			input = filepath.Join(dir, input)
		}
		body, err := readPinFile(input)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%d:%s%d:", len(path), path, len(body))
		buf.Write(body)
	}
	return digest(buf.Bytes()), nil
}
func digest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func invalid(message, hint string) error {
	return &apierr.Error{Code: apierr.InvalidArgs, Message: message, Hint: hint}
}
