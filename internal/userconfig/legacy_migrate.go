package userconfig

// hack: this file moves the per-file settings config.yaml replaced into their
// sections. A later release deletes it with its callers in userconfig.go and
// each owner's legacy_migrate.go.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// MigratedSuffix names the copy a migrated legacy file is kept as.
const MigratedSuffix = ".migrated"

type legacyFile struct {
	name    string
	section string
	key     string
}

var legacyFiles = []legacyFile{
	{name: "admission.yaml", section: Admission},
	{name: "budget", section: Admission, key: "budget"},
	{name: "agent.yaml", section: Agent},
	{name: "fleet.yaml", section: Fleet},
	{name: "profiles.yaml", section: Profiles},
	{name: "repos.yaml", section: Repos},
}

var legacyEnv = []string{"SPARKWING_FLEET_CONFIG", "SPARKWING_PROFILES", "SPARKWING_REPOS"}

var legacyValidators = map[string]func(*yaml.Node) error{}

// RegisterLegacyValidator gives section the check a legacy file's content must
// pass before it moves into config.yaml. A binary that links no validator for
// a section leaves that section's legacy file where it is, because it cannot
// tell valid content from invalid and does not read the section either.
func RegisterLegacyValidator(section string, validate func(section *yaml.Node) error) {
	legacyValidators[section] = validate
}

// DecodeStrict decodes node into out, rejecting a key out does not declare.
func DecodeStrict(node *yaml.Node, out any) error {
	body, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Leftover is a legacy settings file still in the config directory, or a
// replaced path variable still set, and where its setting belongs.
type Leftover struct {
	Name    string `json:"name"`
	MovesTo string `json:"moves_to"`
}

// LegacyDir is where the legacy settings files are looked for: the config
// directory, whatever SPARKWING_CONFIG names.
func LegacyDir() (string, error) {
	// safety: the same test-binary redirect as [Path], so a suite never moves
	// the developer's own files.
	if os.Getenv("XDG_CONFIG_HOME") == "" && paths.UnderTest() {
		return filepath.Join(paths.TestSandbox(), "config", "sparkwing"), nil
	}
	return fssecure.ConfigDir()
}

// Leftovers lists the legacy settings files still in the config directory and
// the replaced path variables still set.
func Leftovers() []Leftover {
	var out []Leftover
	if dir, err := LegacyDir(); err == nil {
		for _, f := range legacyFiles {
			p := filepath.Join(dir, f.name)
			if _, err := os.Lstat(p); err == nil {
				out = append(out, Leftover{Name: p, MovesTo: "the " + f.target() + " section of config.yaml"})
			}
		}
	}
	for _, name := range legacyEnv {
		if os.Getenv(name) != "" {
			out = append(out, Leftover{Name: name, MovesTo: PathEnv + ", which names config.yaml itself"})
		}
	}
	return out
}

func (f legacyFile) target() string {
	if f.key == "" {
		return f.section
	}
	return f.section + "." + f.key
}

// MigrateLegacy moves every legacy settings file into the config.yaml [Path]
// names. Reads and writes of config.yaml do this on their own; `sparkwing
// doctor` calls it to report the outcome.
func MigrateLegacy() error {
	path, err := Path()
	if err != nil {
		return err
	}
	return migrateLegacy(path)
}

// safety: a runner service installed before config.yaml passes its agent.yaml
// to --config, and would otherwise read a file the migration set aside.
func legacyRedirect(path string) string {
	dir, err := LegacyDir()
	if err != nil {
		return path
	}
	for _, f := range legacyFiles {
		if filepath.Clean(path) != filepath.Join(dir, f.name) {
			continue
		}
		target, err := Path()
		if err != nil || filepath.Clean(target) == filepath.Clean(path) {
			return path
		}
		fmt.Fprintf(os.Stderr, "sparkwing: %s is now the %s section of %s; point --config at %s\n", path, f.target(), target, target)
		return target
	}
	return path
}

func checkLegacyEnv() error {
	var set []string
	for _, name := range legacyEnv {
		if os.Getenv(name) != "" {
			set = append(set, name)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("%s no longer moves any setting; sparkwing reads one config.yaml, which %s names. "+
		"Move the file's contents into config.yaml (sparkwing docs search --query config.yaml) and unset %s",
		strings.Join(set, " and "), PathEnv, strings.Join(set, " and "))
}

func presentLegacyFiles() (string, []legacyFile, error) {
	dir, err := LegacyDir()
	if err != nil {
		return "", nil, err
	}
	var present []legacyFile
	for _, f := range legacyFiles {
		if legacyValidators[f.section] == nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, f.name)); err == nil {
			present = append(present, f)
		}
	}
	return dir, present, nil
}

func migrateLegacy(path string) error {
	if err := checkLegacyEnv(); err != nil {
		return err
	}
	if _, present, err := presentLegacyFiles(); err != nil || len(present) == 0 {
		return err
	}
	if err := configguard.GuardWrite("config.yaml", PathEnv, path); err != nil {
		return err
	}
	if err := fssecure.EnsureConfigDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare %s: %w", filepath.Dir(path), err)
	}
	unlock, err := lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	return migrateLegacyLocked(path)
}

// safety: config.yaml is written before any legacy file is renamed, so a crash
// between the two leaves a file whose section already holds the same content,
// which the next run renames without merging it again.
func migrateLegacyLocked(path string) error {
	if err := checkLegacyEnv(); err != nil {
		return err
	}
	dir, present, err := presentLegacyFiles()
	if err != nil || len(present) == 0 {
		return err
	}
	for _, f := range present {
		if err := configguard.GuardWrite("the legacy settings file "+f.name, "", filepath.Join(dir, f.name)); err != nil {
			return err
		}
	}
	data, err := readFile(path)
	if err != nil {
		return err
	}
	doc, root, err := parseDoc(path, data)
	if err != nil {
		return err
	}
	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = &yaml.Node{Kind: yaml.DocumentNode, HeadComment: doc.HeadComment, Content: []*yaml.Node{root}}
	}

	var conflicts []string
	changed := map[string]bool{}
	for _, f := range present {
		legacyPath := filepath.Join(dir, f.name)
		incoming, err := readLegacy(legacyPath, f)
		if err != nil {
			return err
		}
		merged, conflict, err := mergeLegacy(root, f, incoming)
		if err != nil {
			return fmt.Errorf("move %s into %s: %w", legacyPath, path, err)
		}
		if conflict {
			conflicts = append(conflicts, fmt.Sprintf("  %s and the %s section of %s disagree", legacyPath, f.target(), path))
		}
		if merged {
			changed[f.section] = true
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("sparkwing settings moved into config.yaml, but some legacy files disagree with it:\n%s\n"+
			"keep the setting you want in config.yaml, delete the legacy file, and run again", strings.Join(conflicts, "\n"))
	}
	for _, f := range present {
		if !changed[f.section] {
			continue
		}
		_, section := lookup(root, f.section)
		if err := legacyValidators[f.section](section); err != nil {
			return fmt.Errorf("%s cannot move into the %s section of %s: %w", filepath.Join(dir, f.name), f.section, path, err)
		}
	}
	if len(changed) > 0 {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return fmt.Errorf("encode %s: %w", path, err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("encode %s: %w", path, err)
		}
		if err := replace(path, buf.Bytes()); err != nil {
			return err
		}
	}
	for _, f := range present {
		legacyPath := filepath.Join(dir, f.name)
		if err := os.Rename(legacyPath, legacyPath+MigratedSuffix); err != nil {
			return fmt.Errorf("the settings in %s are in %s, but setting the file aside failed: %w", legacyPath, path, err)
		}
		fmt.Fprintf(os.Stderr, "sparkwing: moved %s into the %s section of %s; the original is kept as %s%s\n",
			legacyPath, f.target(), path, f.name, MigratedSuffix)
	}
	return nil
}

func readLegacy(path string, f legacyFile) (*yaml.Node, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file; move its settings into config.yaml by hand", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if f.key == "budget" {
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: line}, nil
			}
		}
		return nil, sc.Err()
	}
	_, root, err := parseLegacyDoc(path, data)
	if err != nil || root == nil {
		return nil, err
	}
	if f.section != Profiles {
		return root, nil
	}
	// safety: the profiles loader ignored every other top-level key, such as
	// the default: older releases wrote, so dropping one changes nothing that
	// took effect; the .migrated copy still holds it.
	var profiles *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if key := root.Content[i].Value; key != Profiles {
			fmt.Fprintf(os.Stderr, "sparkwing: %s sets %s, which sparkwing does not read; config.yaml leaves it out\n", path, key)
			continue
		}
		profiles = root.Content[i+1]
	}
	return profiles, nil
}

func parseLegacyDoc(path string, data []byte) (*yaml.Node, *yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing yaml.Node
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("parse %s: multiple YAML documents are not allowed", path)
	}
	if len(doc.Content) == 0 || isEmpty(doc.Content[0]) {
		return &doc, nil, nil
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("parse %s: line %d: the file must be a mapping", path, doc.Content[0].Line)
	}
	return &doc, doc.Content[0], nil
}

func mergeLegacy(root *yaml.Node, f legacyFile, incoming *yaml.Node) (merged, conflict bool, err error) {
	if incoming == nil || isEmpty(incoming) {
		return false, false, nil
	}
	_, section := lookup(root, f.section)
	if f.section != Admission {
		if section == nil || isEmpty(section) {
			setSection(root, f.section, incoming)
			return true, false, nil
		}
		equal, err := sameValue(section, incoming)
		return false, !equal, err
	}
	if section != nil && !isEmpty(section) && section.Kind != yaml.MappingNode {
		return false, false, fmt.Errorf("line %d: the admission section must be a mapping", section.Line)
	}
	if section == nil || isEmpty(section) {
		section = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	budgetKey, budget := lookup(section, "budget")
	if f.key == "budget" {
		if budget != nil && !isEmpty(budget) {
			equal, err := sameValue(budget, incoming)
			return false, !equal, err
		}
		setSection(section, "budget", incoming)
		setSection(root, Admission, section)
		return true, false, nil
	}
	rest := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(section.Content); i += 2 {
		if section.Content[i].Value != "budget" {
			rest.Content = append(rest.Content, section.Content[i], section.Content[i+1])
		}
	}
	if len(rest.Content) > 0 {
		equal, err := sameValue(rest, incoming)
		return false, !equal, err
	}
	next := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: append([]*yaml.Node{}, incoming.Content...)}
	if budget != nil {
		next.Content = append(next.Content, budgetKey, budget)
	}
	setSection(root, Admission, next)
	return true, false, nil
}

func isEmpty(n *yaml.Node) bool {
	switch n.Kind {
	case 0:
		return true
	case yaml.ScalarNode:
		return n.Tag == "!!null" || (n.Tag == "!!str" && strings.TrimSpace(n.Value) == "")
	case yaml.MappingNode, yaml.SequenceNode:
		return len(n.Content) == 0
	}
	return false
}

// safety: scalars compare by text, since a budget written by hand as 6 and one
// read from the budget file as "6" are the same setting.
func sameValue(a, b *yaml.Node) (bool, error) {
	if a.Kind == yaml.ScalarNode && b.Kind == yaml.ScalarNode {
		return strings.TrimSpace(a.Value) == strings.TrimSpace(b.Value), nil
	}
	var av, bv any
	if err := a.Decode(&av); err != nil {
		return false, err
	}
	if err := b.Decode(&bv); err != nil {
		return false, err
	}
	return reflect.DeepEqual(av, bv), nil
}
