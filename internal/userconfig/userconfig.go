// Package userconfig owns config.yaml, the one settings file in the sparkwing
// user config directory. Each top-level key is a section that belongs to one
// package, which keeps its own typed struct and validation: admission and its
// budget to internal/wingd, agent to internal/agentconfig, cache to
// internal/bincache, fleet to internal/fleet, profiles to internal/profile,
// repos to internal/repos, and debug, logs, machine and run to
// internal/orchestrator.
//
// [Read] decodes one section and rejects unknown keys. [Update] and [Write]
// rewrite one section under a lock, keeping every other section and the
// comments outside the rewritten section. The file can carry credentials, so
// it is read only when it is an owner-only regular file and written owner-only.
package userconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// Filename is the settings file inside [fssecure.ConfigDir].
const Filename = "config.yaml"

// PathEnv names the settings file, the way SPARKWING_HOME names the state root.
const PathEnv = "SPARKWING_CONFIG"

// The sections config.yaml accepts. Any other top-level key is an error.
const (
	Admission = "admission"
	Agent     = "agent"
	Cache     = "cache"
	Debug     = "debug"
	Fleet     = "fleet"
	Logs      = "logs"
	Machine   = "machine"
	Profiles  = "profiles"
	Repos     = "repos"
	Run       = "run"
)

var sections = []string{Admission, Agent, Cache, Debug, Fleet, Logs, Machine, Profiles, Repos, Run}

// Path reports the settings file: $SPARKWING_CONFIG when set, else config.yaml
// in [fssecure.ConfigDir]. SPARKWING_HOME does not move it, because a profile
// or a registered repo is a machine-wide fact that outlives any one home;
// [configguard.ErrOutsideSandboxHome] is how a write says so.
func Path() (string, error) {
	if v := os.Getenv(PathEnv); v != "" {
		return v, nil
	}
	// safety: a test binary that sets neither variable would otherwise read and
	// rewrite the developer's own file, which holds live credentials.
	if os.Getenv("XDG_CONFIG_HOME") == "" && paths.UnderTest() {
		return filepath.Join(paths.TestSandbox(), "config", "sparkwing", Filename), nil
	}
	return fssecure.ConfigFile(Filename)
}

// Read decodes section of the settings file at path into out, which must be a
// pointer. Fields out already holds survive when the section leaves them
// unset, so a caller seeds defaults before the call. Read reports false, and
// leaves out alone, when the file or the section is absent. A key out does not
// declare, an unknown section, or a second YAML document is an error.
func Read(path, section string, out any) (bool, error) {
	path = legacyRedirect(path)
	if err := migrateLegacy(path, section); err != nil {
		return false, err
	}
	data, err := readFile(path)
	if err != nil || data == nil {
		return false, err
	}
	return decodeSection(path, data, section, out)
}

// ReadDefault is [Read] on the file [Path] reports, which it returns so a
// caller can name the file in an error.
func ReadDefault(section string, out any) (string, bool, error) {
	path, err := Path()
	if err != nil {
		return "", false, err
	}
	found, err := Read(path, section, out)
	return path, found, err
}

// Node returns section of the settings file at path as parsed YAML, or nil
// when the file or the section is absent. It is for an owner that must inspect
// the section's shape before decoding it.
func Node(path, section string) (*yaml.Node, error) {
	path = legacyRedirect(path)
	if err := migrateLegacy(path, section); err != nil {
		return nil, err
	}
	data, err := readFile(path)
	if err != nil || data == nil {
		return nil, err
	}
	root, err := parseRoot(path, data)
	if err != nil {
		return nil, err
	}
	_, value := lookup(root, section)
	return value, nil
}

// Update rewrites section of the settings file at path under an exclusive
// lock. It decodes the current section into out (see [Read]), calls change
// with whether the section was present, and writes out back as the section.
// An error from change leaves the file untouched. A section that encodes to
// nothing is removed. what names the file's owner in a sandbox refusal; see
// [configguard.GuardWrite].
func Update(path, section, what string, out any, change func(found bool) error) error {
	return update(path, section, what, out, out, change)
}

// Write replaces section of the settings file at path with value, keeping
// every other section. See [Update].
func Write(path, section, what string, value any) error {
	var current yaml.Node
	return update(path, section, what, &current, value, func(bool) error { return nil })
}

func update(path, section, what string, out, value any, change func(found bool) error) error {
	if err := knownSection(section); err != nil {
		return err
	}
	path = legacyRedirect(path)
	if err := configguard.GuardWrite(what, PathEnv, path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := fssecure.EnsureConfigDir(dir); err != nil {
		return fmt.Errorf("prepare %s: %w", dir, err)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; sparkwing writes its settings only to a regular file, so move it aside and run this again", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	unlock, err := lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	if err := migrateLegacyLocked(path, section); err != nil {
		return err
	}

	data, err := readFile(path)
	if err != nil {
		return err
	}
	doc, root, err := parseDoc(path, data)
	if err != nil {
		return err
	}
	found := false
	if root != nil {
		if found, err = decodeSection(path, data, section, out); err != nil {
			return err
		}
	} else {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = &yaml.Node{Kind: yaml.DocumentNode, HeadComment: doc.HeadComment, Content: []*yaml.Node{root}}
	}
	if err := change(found); err != nil {
		return err
	}
	var encoded yaml.Node
	if err := encoded.Encode(value); err != nil {
		return fmt.Errorf("encode the %s section: %w", section, err)
	}
	setSection(root, section, &encoded)
	return writeDoc(path, section, doc)
}

func writeDoc(path, section string, doc *yaml.Node) error {
	if anchor := danglingAlias(doc); anchor != "" {
		return fmt.Errorf("%s: an alias elsewhere in the file refers to the anchor &%s, which the rewritten %s section no longer defines; "+
			"replace that alias with its value, then run this again", path, anchor, section)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return replace(path, buf.Bytes())
}

// safety: a node's identity, not its anchor name, decides reachability, so an
// alias whose anchor node was replaced counts as dangling even if the name recurs.
func danglingAlias(doc *yaml.Node) string {
	present := map[*yaml.Node]bool{}
	var aliases []*yaml.Node
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || present[n] {
			return
		}
		present[n] = true
		if n.Kind == yaml.AliasNode {
			aliases = append(aliases, n)
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(doc)
	for _, a := range aliases {
		if a.Alias != nil && !present[a.Alias] {
			return a.Alias.Anchor
		}
	}
	return ""
}

func knownSection(section string) error {
	for _, s := range sections {
		if s == section {
			return nil
		}
	}
	return fmt.Errorf("userconfig: unknown section %q", section)
}

func readFile(path string) ([]byte, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func parseRoot(path string, data []byte) (*yaml.Node, error) {
	_, root, err := parseDoc(path, data)
	return root, err
}

// safety: the document is never nil, so a comment-only file keeps its comment
// when a section is added to it.
func parseDoc(path string, data []byte) (*yaml.Node, *yaml.Node, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode}
	if len(data) == 0 {
		return doc, nil, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(doc); err != nil {
		if errors.Is(err, io.EOF) {
			// safety: yaml reports a comment-only file as empty, so its text is
			// carried as the document's comment rather than dropped on write.
			return &yaml.Node{Kind: yaml.DocumentNode, HeadComment: strings.TrimRight(string(data), "\n")}, nil, nil
		}
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing yaml.Node
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not allowed")
		}
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return doc, nil, nil
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return doc, nil, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("parse %s: line %d: the file must be a mapping of sections (%s)", path, root.Line, strings.Join(sections, ", "))
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if err := knownSection(key.Value); err != nil {
			return nil, nil, fmt.Errorf("parse %s: line %d: unknown section %q; the sections are %s", path, key.Line, key.Value, strings.Join(sections, ", "))
		}
		if seen[key.Value] {
			return nil, nil, fmt.Errorf("parse %s: line %d: section %q appears twice", path, key.Line, key.Value)
		}
		seen[key.Value] = true
	}
	return doc, root, nil
}

// hack: a struct built at run time holds the one typed section beside bare
// nodes for the others, so yaml's own unknown-field check runs and its errors
// carry the file's line numbers.
func decodeSection(path string, data []byte, section string, out any) (bool, error) {
	if err := knownSection(section); err != nil {
		return false, err
	}
	target := reflect.ValueOf(out)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return false, fmt.Errorf("userconfig: decode %s into a non-nil pointer, not %T", section, out)
	}
	root, err := parseRoot(path, data)
	if err != nil {
		return false, err
	}
	_, value := lookup(root, section)
	if value == nil || (value.Kind == yaml.ScalarNode && value.Tag == "!!null") {
		return false, nil
	}
	if node, ok := out.(*yaml.Node); ok {
		*node = *value
		return true, nil
	}
	nodeType := reflect.TypeOf(yaml.Node{})
	fields := make([]reflect.StructField, len(sections))
	own := 0
	for i, s := range sections {
		fields[i] = reflect.StructField{Name: fmt.Sprintf("S%d", i), Type: nodeType, Tag: reflect.StructTag(`yaml:"` + s + `"`)}
		if s == section {
			fields[i].Type = target.Type()
			own = i
		}
	}
	holder := reflect.New(reflect.StructOf(fields))
	holder.Elem().Field(own).Set(target)
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(holder.Interface()); err != nil {
		return false, fmt.Errorf("parse %s: %s: %w", path, section, err)
	}
	return true, nil
}

func lookup(root *yaml.Node, section string) (*yaml.Node, *yaml.Node) {
	if root == nil {
		return nil, nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == section {
			return root.Content[i], root.Content[i+1]
		}
	}
	return nil, nil
}

// safety: an existing section keeps its key node and is merged rather than
// replaced, so comments, styles and anchors on the keys that survive stay put.
func setSection(root *yaml.Node, section string, value *yaml.Node) {
	empty := value.Kind == 0 ||
		(value.Kind == yaml.ScalarNode && value.Tag == "!!null") ||
		((value.Kind == yaml.MappingNode || value.Kind == yaml.SequenceNode) && len(value.Content) == 0)
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != section {
			continue
		}
		if empty {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			return
		}
		root.Content[i+1] = mergeNode(root.Content[i+1], value)
		return
	}
	if empty {
		return
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: section}, value)
}

// hack: keys prev and next share keep prev's nodes, updated in place, which is
// what carries their comments, styles and anchors through a rewrite.
func mergeNode(prev, next *yaml.Node) *yaml.Node {
	if prev == nil || prev.Kind != next.Kind || prev.Kind == yaml.AliasNode {
		return next
	}
	switch next.Kind {
	case yaml.ScalarNode:
		if prev.Value == next.Value && prev.Tag == next.Tag {
			return prev
		}
		next.HeadComment, next.LineComment, next.FootComment = prev.HeadComment, prev.LineComment, prev.FootComment
		return next
	case yaml.MappingNode:
		for i := 0; i+1 < len(prev.Content); i += 2 {
			if prev.Content[i].Value == "<<" {
				return next
			}
		}
		merged := make([]*yaml.Node, 0, len(next.Content))
		for i := 0; i+1 < len(next.Content); i += 2 {
			key, val := next.Content[i], next.Content[i+1]
			if prevKey, prevVal := lookup(prev, key.Value); prevKey != nil {
				key, val = prevKey, mergeNode(prevVal, val)
			}
			merged = append(merged, key, val)
		}
		prev.Content = merged
		return prev
	case yaml.SequenceNode:
		// safety: items match by value, not position, so removing one entry
		// does not shift the comments of the rest onto their neighbors.
		used := make([]bool, len(prev.Content))
		merged := make([]*yaml.Node, len(next.Content))
		for i, item := range next.Content {
			merged[i] = item
			for j, old := range prev.Content {
				if !used[j] && equalNode(old, item) {
					used[j], merged[i] = true, old
					break
				}
			}
		}
		prev.Content = merged
		return prev
	}
	return next
}

func equalNode(a, b *yaml.Node) bool {
	var av, bv any
	if a.Decode(&av) != nil || b.Decode(&bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// safety: a random name plus O_EXCL keeps a pre-created path from receiving a
// credential, and the rename means an interrupted write leaves the previous
// file whole rather than truncated.
func replace(path string, body []byte) (retErr error) {
	dir := filepath.Dir(path)
	tempParent := dir
	if runtime.GOOS == "windows" {
		privateDir, err := fssecure.MkdirPrivateTemp(dir, ".config-")
		if err != nil {
			return fmt.Errorf("create private temp directory in %s: %w", dir, err)
		}
		tempParent = privateDir
		defer func() { retErr = errors.Join(retErr, os.Remove(privateDir)) }()
	}
	tmp, err := os.CreateTemp(tempParent, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if retErr == nil {
			return
		}
		_ = tmp.Close()
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "sparkwing: could not clear the temporary file %s: %v\n", tmpPath, err)
		}
	}()
	if err := fssecure.TightenOpen(tmp); err != nil {
		return fmt.Errorf("secure %s: %w", tmpPath, err)
	}
	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmpPath, err)
	}
	if runtime.GOOS != "windows" {
		if err := fssecure.SecurePrivateConfig(path); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// safety: the rename is durable only once the directory entry is, so a crash
// after a write reports success cannot bring the old file back.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return d.Close()
}

// safety: the lock sits beside the file rather than on it, because the write
// replaces the file by rename and a lock on the old inode would not exclude a
// writer that opened the new one.
func lock(path string) (func(), error) {
	lockPath := path + ".lock"
	for range 5 {
		info, err := os.Lstat(lockPath)
		if errors.Is(err, os.ErrNotExist) {
			var f *os.File
			var createErr error
			if runtime.GOOS == "windows" {
				f, createErr = fssecure.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR)
			} else {
				f, createErr = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			}
			if errors.Is(createErr, os.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, createErr
			}
			if runtime.GOOS != "windows" {
				if err := fssecure.SecurePrivateConfig(lockPath); err != nil {
					_ = f.Close()
					return nil, err
				}
			}
			if err := flockWait(f); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("lock %s: %w", lockPath, err)
			}
			return release(f), nil
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s must be a regular file", lockPath)
		}
		if err := fssecure.VerifyPrivateConfig(lockPath, info); err != nil {
			return nil, fmt.Errorf("%s is not owner-only: %w", lockPath, err)
		}
		f, err := os.OpenFile(lockPath, os.O_RDWR, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		opened, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = f.Close()
			if statErr != nil {
				return nil, statErr
			}
			continue
		}
		if err := flockWait(f); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", lockPath, err)
		}
		return release(f), nil
	}
	return nil, fmt.Errorf("%s kept changing", lockPath)
}

func release(f *os.File) func() {
	return func() {
		// safety: closing the descriptor drops the lock as well, so a failed
		// unlock needs no retry, only a report.
		if err := errors.Join(flockUnlock(f), f.Close()); err != nil {
			fmt.Fprintf(os.Stderr, "sparkwing: release %s: %v\n", f.Name(), err)
		}
	}
}
