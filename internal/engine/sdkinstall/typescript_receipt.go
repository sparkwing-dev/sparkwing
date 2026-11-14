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

// safety: the receipt lives inside the tree it describes, so removing node_modules removes it too.
const typeScriptReceipt = ".sparkwing-sdk-receipt"

const typeScriptReceiptFormat = "sparkwing typescript sdk receipt 2"

// safety: npm runs these root scripts on every install, so a tree that declares one is never reused.
var typeScriptLifecycleScripts = []string{"preinstall", "install", "postinstall", "prepublish", "preprepare", "prepare", "postprepare", "dependencies"}

var errTypeScriptReuseUnavailable = errors.New("typescript install cannot be reused")

// safety: these are the inputs npm reads but does not write: manifest, lockfile, package and npm configuration
// in every ancestor, user, global and builtin npmrc, npm-relevant environment, node and npm, and local tarballs.
// Install compares them before and after npm, so an edit during npm is never recorded.
func typeScriptInstallInputs(dir, lockfile string, environment procenv.Env) (string, error) {
	manifest, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", err
	}
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
	for _, name := range []string{"package.json", lockfile} {
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

// perf: path, size and modification time identify an upgraded node or npm without hashing the executable.
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

// safety: npm copies local tarballs into the tree, so their bytes can change under an unchanged lockfile path.
// A copied local directory cannot be digested cheaply and disables reuse; a linked one shows changes live.
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

// safety: npm trusts its hidden lockfile only while every package it lists exists and no package folder is newer,
// and otherwise rereads and repairs the tree; a receipt must not skip that repair.
func typeScriptInstalledTree(dir string) (string, error) {
	record := filepath.Join(dir, "node_modules", ".package-lock.json")
	info, err := os.Stat(record)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(record)
	if err != nil {
		return "", err
	}
	var hidden struct {
		Packages map[string]struct {
			Link bool `json:"link"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &hidden); err != nil {
		return "", err
	}
	for location, entry := range hidden.Packages {
		if !strings.HasPrefix(location, "node_modules/") {
			continue
		}
		folder, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(location)))
		if err != nil {
			return "", errTypeScriptReuseUnavailable
		}
		if !entry.Link && folder.ModTime().After(info.ModTime()) {
			return "", errTypeScriptReuseUnavailable
		}
	}
	sdk, err := os.ReadFile(filepath.Join(dir, "node_modules", "@sparkwing", "sdk", "package.json"))
	if err != nil {
		return "", err
	}
	treeSum, sdkSum := sha256.Sum256(body), sha256.Sum256(sdk)
	return fmt.Sprintf("%x:%x", treeSum, sdkSum), nil
}

func typeScriptReceiptBody(inputs, tree string) string {
	return typeScriptReceiptFormat + "\n" + inputs + "\n" + tree
}

// safety: callers hold the lockfile lock, so no preparation rewrites the receipt or the tree while this reads it.
func reuseTypeScriptInstall(dir, inputs string) bool {
	if inputs == "" {
		return false
	}
	tree, err := typeScriptInstalledTree(dir)
	if err != nil {
		return false
	}
	recorded, err := os.ReadFile(filepath.Join(dir, "node_modules", typeScriptReceipt))
	return err == nil && string(recorded) == typeScriptReceiptBody(inputs, tree)
}

// safety: this runs before any npm write, so a failed or interrupted install leaves no receipt to match a
// partially written tree.
func discardTypeScriptReceipt(dir string) error {
	err := os.Remove(filepath.Join(dir, "node_modules", typeScriptReceipt))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// safety: callers verify the installed SDK first. inputs is the digest taken before npm ran; any input that
// changed since, or a tree npm itself would not trust, keeps no receipt.
func recordTypeScriptInstall(dir, lockfile, inputs string, environment procenv.Env) error {
	if inputs == "" {
		return discardTypeScriptReceipt(dir)
	}
	after, inputsErr := typeScriptInstallInputs(dir, lockfile, environment)
	if inputsErr != nil || after != inputs {
		return discardTypeScriptReceipt(dir)
	}
	tree, treeErr := typeScriptInstalledTree(dir)
	if treeErr != nil {
		return discardTypeScriptReceipt(dir)
	}
	path := filepath.Join(dir, "node_modules", typeScriptReceipt)
	staged, err := os.CreateTemp(filepath.Dir(path), typeScriptReceipt+".*")
	if err != nil {
		return err
	}
	_, err = staged.WriteString(typeScriptReceiptBody(inputs, tree))
	err = errors.Join(err, staged.Close())
	if err == nil {
		err = os.Rename(staged.Name(), path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(staged.Name()))
	}
	return nil
}
