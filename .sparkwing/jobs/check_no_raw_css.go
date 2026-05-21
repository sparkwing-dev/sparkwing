package jobs

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// CheckNoRawCSS scans every tracked TypeScript / JavaScript file
// (.ts, .tsx, .js, .jsx, .mjs, .cjs) for the three patterns that
// indicate raw CSS sneaking into a Tailwind-only codebase:
//
//  1. `style={...}` JSX prop (inline style override)
//  2. import of a `.css` / `.scss` / `.less` / `.module.css` file
//     (with one allow-listed exception: a single global Tailwind
//     entry like `globals.css` / `global.css`)
//  3. `<style>` JSX tag (styled-jsx)
//
// No findings -> nil. Any finding -> aggregated error with file:line
// references the author can audit.
//
// This is a Tailwind-codebase convention check, not a Go lint. Repos
// without any matching files (e.g. Go-only consumers under
// sparkwing-platform/) yield zero findings; the function is safe to
// wire into every consumer's pre-commit.
func CheckNoRawCSS(ctx context.Context, repoRoot string) error {
	var problems []string
	files, err := jsTrackedFiles(ctx, repoRoot)
	if err != nil {
		return err
	}
	for _, rel := range files {
		full := filepath.Join(repoRoot, rel)
		f, err := os.Open(full)
		if err != nil {
			continue
		}
		problems = append(problems, scanFileForRawCSS(rel, f)...)
		_ = f.Close()
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("raw CSS detected (this is a Tailwind-only codebase):\n  - %s",
		strings.Join(problems, "\n  - "))
}

// jsTrackedFiles returns the subset of git-tracked files whose
// extension implies TypeScript or JavaScript source. node_modules,
// .next build output, and Biome / TS source maps are skipped via
// path filtering after the git ls-files call.
func jsTrackedFiles(ctx context.Context, repoRoot string) ([]string, error) {
	all, err := captureGit(ctx, repoRoot, "ls-files")
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(all), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "node_modules/") || strings.Contains(line, "/node_modules/") {
			continue
		}
		if strings.HasPrefix(line, ".next/") || strings.Contains(line, "/.next/") {
			continue
		}
		switch filepath.Ext(line) {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
			out = append(out, line)
		}
	}
	return out, nil
}

var (
	// inlineStylePropPattern catches `style={...}` JSX attributes.
	// Anchored to a leading non-word boundary so `someStyle={...}`
	// doesn't false-match (the prop must be the literal `style`).
	inlineStylePropPattern = regexp.MustCompile(`(^|[\s,<({])style\s*=\s*\{`)

	// cssImportPattern catches ES-module / CommonJS imports of CSS
	// (any flavor). Captures the imported path so the allow-list
	// check has the filename to decide on.
	cssImportPattern = regexp.MustCompile(`(?:^|\n|;)\s*(?:import\s+(?:[^'"]+\s+from\s+)?|require\s*\()['"]([^'"]+\.(?:css|scss|sass|less|styl))['"]`)

	// styleTagPattern catches JSX `<style>` and `<style scoped>` etc.
	// Lowercase-only to avoid catching `<StyleProvider>` and friends.
	styleTagPattern = regexp.MustCompile(`<style[\s>/]`)
)

// allowedGlobalCSS is the list of filenames whose CSS import is
// legitimate (the one Tailwind entry point per Next.js convention).
// Compared by filepath.Base; full path doesn't matter so
// `app/globals.css` and `src/styles/globals.css` both pass.
var allowedGlobalCSS = map[string]bool{
	"globals.css": true,
	"global.css":  true,
	// Tailwind v4 sometimes uses these names instead.
	"app.css":  true,
	"main.css": true,
}

// scanFileForRawCSS walks the file line-by-line, returning a
// finding-string per violation. Callers pass the file's repo-
// relative path so the messages render in standard linter format.
func scanFileForRawCSS(relPath string, r *os.File) []string {
	var out []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		// 1. Inline style prop.
		if inlineStylePropPattern.MatchString(line) {
			out = append(out, fmt.Sprintf(
				"%s:%d: inline-style: `style={...}` JSX prop — Tailwind utility class via className instead",
				relPath, lineNum,
			))
		}
		// 2. CSS import (except the allow-listed global Tailwind entry).
		if m := cssImportPattern.FindStringSubmatch(line); m != nil {
			imported := m[1]
			if !allowedGlobalCSS[filepath.Base(imported)] {
				out = append(out, fmt.Sprintf(
					"%s:%d: css-import: `%s` — Tailwind-only codebase; the one allowed CSS file is the global Tailwind entry (globals.css / global.css)",
					relPath, lineNum, imported,
				))
			}
		}
		// 3. styled-jsx `<style>` tag.
		if styleTagPattern.MatchString(line) {
			out = append(out, fmt.Sprintf(
				"%s:%d: style-tag: `<style>` JSX — use Tailwind classes, not styled-jsx",
				relPath, lineNum,
			))
		}
	}
	return out
}
