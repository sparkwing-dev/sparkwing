package userconfig

// hack: this file copies the per-file settings config.yaml replaced into their
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
	"regexp"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

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
// pass before it is copied into config.yaml. A binary that links no validator
// for a section never copies that section, because it cannot tell valid
// content from invalid and does not read the section either.
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
	// Copied reports a legacy file whose section config.yaml already holds, so
	// sparkwing no longer reads the file and it can be deleted.
	Copied bool `json:"copied,omitempty"`
	// Variable reports a replaced path variable rather than a file.
	Variable bool `json:"variable,omitempty"`
}

// LegacyDir is the config directory the legacy settings files are read from.
func LegacyDir() (string, error) {
	// safety: the same test-binary redirect as [Path], so a suite never reads
	// the developer's own files.
	if os.Getenv("XDG_CONFIG_HOME") == "" && paths.UnderTest() {
		return filepath.Join(paths.TestSandbox(), "config", "sparkwing"), nil
	}
	return fssecure.ConfigDir()
}

// Leftovers lists the legacy settings files still in the config directory,
// each marked copied once config.yaml holds its section, and the replaced path
// variables still set.
func Leftovers() []Leftover {
	var out []Leftover
	if dir, err := LegacyDir(); err == nil {
		var root *yaml.Node
		// safety: an unreadable config.yaml marks every file not copied, which
		// is what a reader of that file would find.
		if data, err := readFile(filepath.Join(dir, Filename)); err == nil {
			if _, parsed, err := parseDoc(filepath.Join(dir, Filename), data); err == nil {
				root = parsed
			}
		}
		for _, f := range legacyFiles {
			p := filepath.Join(dir, f.name)
			if _, err := os.Lstat(p); err == nil {
				out = append(out, Leftover{Name: p, MovesTo: "the " + f.target() + " section of config.yaml", Copied: f.present(root)})
			}
		}
	}
	for _, name := range legacyEnv {
		if os.Getenv(name) != "" {
			out = append(out, Leftover{Name: name, MovesTo: PathEnv + ", which names config.yaml itself", Variable: true})
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

// safety: two files feed the admission section, so the budget file counts as
// present by its key and admission.yaml by any other admission key.
func (f legacyFile) present(root *yaml.Node) bool {
	_, section := lookup(root, f.section)
	if section == nil || isEmpty(section) {
		return false
	}
	if f.section != Admission || section.Kind != yaml.MappingNode {
		return true
	}
	for i := 0; i+1 < len(section.Content); i += 2 {
		isBudget := section.Content[i].Value == "budget"
		if isBudget == (f.key == "budget") && !isEmpty(section.Content[i+1]) {
			return true
		}
	}
	return false
}

// MigrateLegacy copies every legacy settings file whose section config.yaml
// lacks into it. Reads and writes of config.yaml do this on their own;
// `sparkwing doctor` calls it to report the outcome.
func MigrateLegacy() error {
	path, err := Path()
	if err != nil {
		return err
	}
	failures, err := migrate(path)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sections {
		if failures[s] != nil {
			errs = append(errs, failures[s])
		}
	}
	return errors.Join(errs...)
}

// safety: a runner service installed before config.yaml passes its agent.yaml
// to --config; reading config.yaml in its place picks up the copied section.
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

// safety: only the machine's own config.yaml takes the legacy files, so a
// one-off SPARKWING_CONFIG never copies the machine's credentials elsewhere.
func migratesInto(path string) (string, bool) {
	if os.Getenv(PathEnv) != "" {
		return "", false
	}
	dir, err := LegacyDir()
	if err != nil || filepath.Clean(path) != filepath.Join(dir, Filename) {
		return "", false
	}
	return dir, true
}

func pendingLegacy(dir string, root *yaml.Node) []legacyFile {
	var pending []legacyFile
	for _, f := range legacyFiles {
		if legacyValidators[f.section] == nil || f.present(root) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, f.name)); err == nil {
			pending = append(pending, f)
		}
	}
	return pending
}

var sandboxSkipWarned sync.Once

// safety: only section's own refusal is returned, so an invalid legacy file
// blocks the section it feeds and no other.
func migrateLegacy(path, section string) error {
	failures, err := migrate(path)
	if err != nil {
		return err
	}
	return failures[section]
}

func migrate(path string) (map[string]error, error) {
	if err := checkLegacyEnv(); err != nil {
		return nil, err
	}
	dir, ok := migratesInto(path)
	if !ok {
		return nil, nil
	}
	if data, err := readFile(path); err == nil {
		if _, root, err := parseDoc(path, data); err == nil && len(pendingLegacy(dir, root)) == 0 {
			return nil, nil
		}
	}
	if err := configguard.GuardWrite("config.yaml", PathEnv, path); err != nil {
		if !errors.Is(err, configguard.ErrOutsideSandboxHome) {
			return nil, err
		}
		sandboxSkipWarned.Do(func() {
			fmt.Fprintf(os.Stderr, "sparkwing: not copying the legacy settings files in %s into config.yaml from under a sandboxed SPARKWING_HOME; reading config.yaml as it is\n", dir)
		})
		return nil, nil
	}
	if err := fssecure.EnsureConfigDir(dir); err != nil {
		return nil, fmt.Errorf("prepare %s: %w", dir, err)
	}
	unlock, err := lock(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return migrateLocked(path)
}

func migrateLegacyLocked(path, section string) error {
	failures, err := migrateLocked(path)
	if err != nil {
		return err
	}
	return failures[section]
}

// safety: a legacy file is only ever read, never renamed or removed, so an
// older sparkwing on the same machine keeps reading it and a rollback loses
// nothing. Once config.yaml holds the section, the file is ignored.
func migrateLocked(path string) (map[string]error, error) {
	if err := checkLegacyEnv(); err != nil {
		return nil, err
	}
	dir, ok := migratesInto(path)
	if !ok {
		return nil, nil
	}
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	doc, root, err := parseDoc(path, data)
	if err != nil {
		return nil, err
	}
	pending := pendingLegacy(dir, root)
	if len(pending) == 0 {
		return nil, nil
	}
	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = &yaml.Node{Kind: yaml.DocumentNode, HeadComment: doc.HeadComment, Content: []*yaml.Node{root}}
	}

	failures := map[string]error{}
	var copied []legacyFile
	for _, section := range sections {
		var files []legacyFile
		for _, f := range pending {
			if f.section == section {
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			continue
		}
		_, current := lookup(root, section)
		merged, used, err := mergeLegacy(dir, current, files)
		if err == nil && merged != nil {
			err = legacyValidators[section](merged)
		}
		if err != nil {
			failures[section] = legacyError(dir, files, err)
			continue
		}
		if merged == nil {
			continue
		}
		setSection(root, section, merged)
		copied = append(copied, used...)
	}
	if len(copied) == 0 {
		return failures, nil
	}
	if err := writeDoc(path, copied[0].section, doc); err != nil {
		return nil, err
	}
	for _, f := range copied {
		legacyPath := filepath.Join(dir, f.name)
		fmt.Fprintf(os.Stderr, "sparkwing: copied %s into the %s section of %s; %s is no longer read and can be deleted once no older sparkwing on this machine needs it\n",
			legacyPath, f.target(), path, f.name)
	}
	return failures, nil
}

var unknownFieldRE = regexp.MustCompile(`field (\S+) not found in type`)

func legacyError(dir string, files []legacyFile, err error) error {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = filepath.Join(dir, f.name)
	}
	which := strings.Join(names, " or ")
	if m := unknownFieldRE.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s sets %s, which sparkwing does not accept; remove that key from %s and rerun", which, m[1], which)
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	return fmt.Errorf("%s cannot be copied into config.yaml: %s; fix it in %s and rerun", which, msg, which)
}

// safety: current is copied, not changed, so a section whose files fail
// validation leaves the document as it was.
func mergeLegacy(dir string, current *yaml.Node, files []legacyFile) (*yaml.Node, []legacyFile, error) {
	var used []legacyFile
	var merged *yaml.Node
	if current != nil && !isEmpty(current) {
		if current.Kind != yaml.MappingNode {
			return nil, nil, fmt.Errorf("line %d: the section must be a mapping", current.Line)
		}
		merged = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: append([]*yaml.Node{}, current.Content...)}
	}
	for _, f := range files {
		incoming, err := readLegacy(filepath.Join(dir, f.name), f)
		if err != nil {
			return nil, nil, err
		}
		if incoming == nil || isEmpty(incoming) {
			continue
		}
		if anchor := danglingAlias(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{incoming}}); anchor != "" {
			return nil, nil, fmt.Errorf("an alias refers to the anchor &%s outside the settings being copied", anchor)
		}
		used = append(used, f)
		switch {
		case f.key == "budget":
			if merged == nil {
				merged = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			merged.Content = append(merged.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "budget"}, incoming)
		case merged == nil:
			merged = incoming
		default:
			merged.Content = append(append([]*yaml.Node{}, incoming.Content...), merged.Content...)
		}
	}
	return merged, used, nil
}

func readLegacy(path string, f legacyFile) (*yaml.Node, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file; copy its settings into config.yaml by hand", path)
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
	root, err := parseLegacyDoc(path, data)
	if err != nil || root == nil {
		return nil, err
	}
	if f.section != Profiles {
		return root, nil
	}
	// safety: the profiles loader ignored every other top-level key, such as
	// the default: older releases wrote, so leaving one out changes nothing.
	var profiles *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == Profiles {
			profiles = root.Content[i+1]
		}
	}
	return profiles, nil
}

func parseLegacyDoc(path string, data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing yaml.Node
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: multiple YAML documents are not allowed", path)
	}
	if len(doc.Content) == 0 || isEmpty(doc.Content[0]) {
		return nil, nil
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse %s: line %d: the file must be a mapping", path, doc.Content[0].Line)
	}
	return doc.Content[0], nil
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
