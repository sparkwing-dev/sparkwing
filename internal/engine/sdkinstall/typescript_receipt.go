package sdkinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// typeScriptReceipt names the record of the last verified install inside the tree it describes,
// so removing node_modules removes it too.
const typeScriptReceipt = ".sparkwing-sdk-receipt"

const typeScriptReceiptFormat = "sparkwing typescript sdk receipt 1"

// typeScriptLifecycleScripts run on every npm install of the root package, so a tree that declares
// one is never reused.
var typeScriptLifecycleScripts = []string{"preinstall", "install", "postinstall", "prepublish", "preprepare", "prepare", "postprepare", "dependencies"}

var errTypeScriptReuseUnavailable = errors.New("typescript install cannot be reused")

// typeScriptInstallFingerprint digests every input npm reads to decide the installed tree: the
// manifest, lockfile and npm's own tree record, the installed SDK manifest, package and npm
// configuration files in every ancestor directory, user, global and builtin npm configuration,
// npm-relevant environment, the node and npm executables, and local tarball dependencies.
func typeScriptInstallFingerprint(dir, lockfile string, manifest []byte, environment procenv.Env) (string, error) {
	var scripts struct{ Scripts map[string]json.RawMessage }
	if err := json.Unmarshal(manifest, &scripts); err != nil {
		return "", err
	}
	for _, name := range typeScriptLifecycleScripts {
		if _, ok := scripts.Scripts[name]; ok {
			return "", errTypeScriptReuseUnavailable
		}
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\x00%s\x00%s\x00", typeScriptReceiptFormat, dir, lockfile)
	for _, name := range []string{"package.json", lockfile, filepath.Join("node_modules", ".package-lock.json"), filepath.Join("node_modules", "@sparkwing", "sdk", "package.json")} {
		if err := digestFile(digest, filepath.Join(dir, name), true); err != nil {
			return "", err
		}
	}
	for ancestor := dir; ; {
		for _, name := range []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", ".npmrc"} {
			if err := digestFile(digest, filepath.Join(ancestor, name), false); err != nil {
				return "", err
			}
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	npmEnvironment := map[string]string{}
	for setting := range environment.Values() {
		key, value, _ := strings.Cut(setting, "=")
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "npm_config_") || slices.Contains([]string{"home", "node_env", "node_options", "node_path", "prefix"}, lower) {
			npmEnvironment[lower] = value
		}
	}
	for _, key := range slicesSortedKeys(npmEnvironment) {
		fmt.Fprintf(digest, "env\x00%s\x00%s\x00", key, npmEnvironment[key])
	}
	node, err := executableIdentity(digest, "node")
	if err != nil {
		return "", err
	}
	npm, err := executableIdentity(digest, "npm")
	if err != nil {
		return "", err
	}
	user := npmEnvironment["npm_config_userconfig"]
	if user == "" {
		user = filepath.Join(npmEnvironment["home"], ".npmrc")
	}
	prefix := npmEnvironment["npm_config_prefix"]
	if prefix == "" {
		prefix = npmEnvironment["prefix"]
	}
	if prefix == "" {
		prefix = filepath.Dir(filepath.Dir(node))
	}
	global := npmEnvironment["npm_config_globalconfig"]
	if global == "" {
		global = filepath.Join(prefix, "etc", "npmrc")
	}
	for _, path := range []string{user, global} {
		if err := digestFile(digest, path, false); err != nil {
			return "", err
		}
	}
	for root := filepath.Dir(npm); ; {
		var identity struct{ Name string }
		if body, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil && json.Unmarshal(body, &identity) == nil && identity.Name == "npm" {
			sum := sha256.Sum256(body)
			fmt.Fprintf(digest, "npm\x00%s\x00%x\x00", root, sum)
			if err := digestFile(digest, filepath.Join(root, "npmrc"), false); err != nil {
				return "", err
			}
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	if err := digestLocalTarballs(digest, dir, lockfile); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func slicesSortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func digestFile(digest hash.Hash, path string, required bool) error {
	body, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		fmt.Fprintf(digest, "absent\x00%s\x00", path)
		return nil
	}
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	fmt.Fprintf(digest, "file\x00%s\x00%x\x00", path, sum)
	return nil
}

// executableIdentity records the resolved executable's path, size and modification time; npm
// and node upgrades replace the file.
func executableIdentity(digest hash.Hash, name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(digest, "exe\x00%s\x00%s\x00%d\x00%d\x00", name, path, info.Size(), info.ModTime().UnixNano())
	return path, nil
}

// digestLocalTarballs covers local file dependencies npm copies into the tree: their bytes can
// change without any change to the lockfile path. A copied local directory cannot be digested
// cheaply, so it disables reuse; linked directories show their changes through the link.
func digestLocalTarballs(digest hash.Hash, dir, lockfile string) error {
	body, err := os.ReadFile(filepath.Join(dir, lockfile))
	if err != nil {
		return err
	}
	type entry struct {
		Resolved string `json:"resolved"`
		Version  string `json:"version"`
		Link     bool   `json:"link"`
	}
	var lock struct {
		Packages     map[string]entry `json:"packages"`
		Dependencies map[string]entry `json:"dependencies"`
	}
	if err := json.Unmarshal(body, &lock); err != nil {
		return err
	}
	var sources []string
	for _, entries := range []map[string]entry{lock.Packages, lock.Dependencies} {
		for _, dependency := range entries {
			source := dependency.Resolved
			if source == "" && strings.HasPrefix(dependency.Version, "file:") {
				source = dependency.Version
			}
			if path, ok := strings.CutPrefix(source, "file:"); ok && !dependency.Link {
				sources = append(sources, filepath.Join(dir, filepath.FromSlash(path)))
			}
		}
	}
	slices.Sort(sources)
	for _, source := range slices.Compact(sources) {
		info, err := os.Stat(source)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errTypeScriptReuseUnavailable
		}
		if err := digestFile(digest, source, true); err != nil {
			return err
		}
	}
	return nil
}

// reuseTypeScriptInstall reports whether the receipt from the last verified install matches the
// current inputs. Callers hold the lockfile lock.
func reuseTypeScriptInstall(dir, fingerprint string) bool {
	if fingerprint == "" {
		return false
	}
	recorded, err := os.ReadFile(filepath.Join(dir, "node_modules", typeScriptReceipt))
	return err == nil && string(recorded) == fingerprint
}

// discardTypeScriptReceipt runs before any npm write, so an install that fails or is
// interrupted leaves no receipt to match a partially written tree.
func discardTypeScriptReceipt(dir string) error {
	err := os.Remove(filepath.Join(dir, "node_modules", typeScriptReceipt))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// recordTypeScriptInstall writes the receipt after the installed SDK is verified. A tree whose
// inputs cannot be fingerprinted keeps no receipt.
func recordTypeScriptInstall(dir, lockfile string, manifest []byte, environment procenv.Env) error {
	fingerprint, err := typeScriptInstallFingerprint(dir, lockfile, manifest, environment)
	if err != nil {
		return nil
	}
	path := filepath.Join(dir, "node_modules", typeScriptReceipt)
	staged, err := os.CreateTemp(filepath.Dir(path), typeScriptReceipt+".*")
	if err != nil {
		return err
	}
	_, err = staged.WriteString(fingerprint)
	err = errors.Join(err, staged.Close())
	if err == nil {
		err = os.Rename(staged.Name(), path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(staged.Name()))
	}
	return nil
}
