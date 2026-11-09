package docs

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// CollectChangelog inserts independently authored docs/changes/*.md fragments
// into the empty [Unreleased] section. sources is the docs/ filesystem.
func CollectChangelog(body string, sources fs.FS) (string, error) {
	fragments, err := fragmentBodies(sources, "changes")
	if err != nil || len(fragments) == 0 {
		return body, err
	}
	start, end := -1, len(body)
	for _, section := range ParseChangelog(body) {
		if section.Version == "Unreleased" {
			start, end = section.HeaderEnd, section.End
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("CHANGELOG.md has no [Unreleased] section")
	}
	if strings.TrimSpace(body[start:end]) != "" {
		return "", fmt.Errorf("move CHANGELOG.md [Unreleased] entries to docs/changes/*.md")
	}
	categories := []string{"Breaking", "Added", "Changed", "Fixed", "Removed", "Security", "Docs"}
	entries := make(map[string][]string)
	for _, fragment := range fragments {
		category := ""
		bullets := 0
		for line := range strings.Lines(fragment) {
			if strings.HasPrefix(line, "### ") {
				category = strings.TrimSpace(strings.TrimPrefix(line, "### "))
				if !containsCategory(categories, category) {
					return "", fmt.Errorf("unknown changelog category %q", category)
				}
			} else if strings.HasPrefix(line, "#") || (category == "" && strings.TrimSpace(line) != "") {
				return "", fmt.Errorf("changelog fragment must start with a ### category")
			} else if category != "" {
				if strings.HasPrefix(line, "- ") {
					bullets++
				}
				entries[category] = append(entries[category], line)
			}
		}
		if bullets == 0 {
			return "", fmt.Errorf("changelog fragment has no bullet entries")
		}
	}
	var collected strings.Builder
	for _, category := range categories {
		text := strings.TrimSpace(strings.Join(entries[category], ""))
		if text != "" {
			fmt.Fprintf(&collected, "\n\n### %s\n\n%s", category, text)
		}
	}
	return body[:start] + collected.String() + "\n\n" + strings.TrimLeft(body[end:], "\n"), nil
}

func containsCategory(categories []string, category string) bool {
	for _, name := range categories {
		if name == category {
			return true
		}
	}
	return false
}

// CollectMigration reads _unreleased.md and appends unreleased/*.md fragments.
// sources is the migrations/ filesystem; each fragment starts with an H2.
func CollectMigration(sources fs.FS) ([]byte, error) {
	body, err := fs.ReadFile(sources, "_unreleased.md")
	if err != nil {
		return nil, err
	}
	fragments, err := fragmentBodies(sources, "unreleased")
	if err != nil || len(fragments) == 0 {
		return body, err
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(body), "No breaking changes so far.", ""))
	for _, fragment := range fragments {
		if !strings.HasPrefix(strings.TrimSpace(fragment), "## ") {
			return nil, fmt.Errorf("migration fragment must start with an H2")
		}
		if strings.TrimSpace(strings.SplitN(strings.TrimSpace(fragment), "\n", 2)[0]) == strings.TrimSpace(fragment) {
			return nil, fmt.Errorf("migration fragment has no prose")
		}
		text += "\n\n" + strings.TrimSpace(fragment)
	}
	seen := make(map[string]bool)
	for line := range strings.Lines(text) {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		heading := MigrationAnchor(strings.TrimSpace(line[3:]))
		if seen[heading] {
			return nil, fmt.Errorf("duplicate migration heading %q", heading)
		}
		seen[heading] = true
	}
	return []byte(text + "\n"), nil
}

func fragmentBodies(sources fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(sources, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var bodies []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := dir + "/" + entry.Name()
		body, err := fs.ReadFile(sources, name)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("empty release fragment: %s", name)
		}
		bodies = append(bodies, string(body)+"\n")
	}
	return bodies, nil
}

// MigrationAnchor returns the fragment identifier used by migration links.
func MigrationAnchor(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '\t':
			b.WriteRune('-')
		default:
		}
	}
	return b.String()
}
