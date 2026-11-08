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
	const heading = "## [Unreleased]"
	start := strings.Index(body, heading+"\n")
	if start < 0 {
		return "", fmt.Errorf("CHANGELOG.md has no %s section", heading)
	}
	start += len(heading)
	end := len(body)
	if next := strings.Index(body[start:], "\n## "); next >= 0 {
		end = start + next
	}
	if strings.TrimSpace(body[start:end]) != "" {
		return "", fmt.Errorf("move CHANGELOG.md [Unreleased] entries to docs/changes/*.md")
	}
	categories := []string{"Breaking", "Added", "Changed", "Fixed", "Removed", "Security", "Docs"}
	entries := make(map[string][]string)
	for _, fragment := range fragments {
		category := ""
		for line := range strings.Lines(fragment) {
			if strings.HasPrefix(line, "### ") {
				category = strings.TrimSpace(strings.TrimPrefix(line, "### "))
				if !containsCategory(categories, category) {
					return "", fmt.Errorf("unknown changelog category %q", category)
				}
			} else if strings.HasPrefix(line, "#") || (category == "" && strings.TrimSpace(line) != "") {
				return "", fmt.Errorf("changelog fragment must start with a ### category")
			} else if category != "" {
				entries[category] = append(entries[category], line)
			}
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
		text += "\n\n" + strings.TrimSpace(fragment)
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
