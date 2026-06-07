package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// banned is the append-only denylist of dead tokens that must never
// reappear in user- or agent-facing surfaces (the bundled docs and the
// CLI help registry). Each entry pairs a precise pattern with the
// correct replacement, printed verbatim when it fires.
//
// The compiler catches renamed Go symbols; prose, YAML, and shell
// snippets have no such coupling, so a flag/file/key rename silently
// rots every doc that mentions the old name. This list is that coupling:
// once a divergence is fixed, add the dead token here so it can never
// quietly return. Patterns must be tight enough to match zero times in
// the current (clean) tree -- the gate self-tests that on every run.
type bannedPattern struct {
	re   *regexp.Regexp
	want string
}

var banned = []bannedPattern{
	{regexp.MustCompile(`pipelines\.yaml`),
		"the config file is sparkwing.yaml; the legacy pipelines.yaml name is a hard error"},
	{regexp.MustCompile(`\bsparkwing\.db\b`),
		"the SQLite store file is state.db, not sparkwing.db"},
	{regexp.MustCompile(`\.sparkwing/logs\b`),
		"per-run logs live under ~/.sparkwing/runs/<runID>/, not ~/.sparkwing/logs/"},
	{regexp.MustCompile(`ReservedFlagNames`),
		"removed; run control flags are sw-* prefixed, so pipelines own the full unprefixed flag namespace (no reserved-name collision)"},
	{regexp.MustCompile(`\bruns_on\b`),
		"not a sparkwing.yaml field (the strict parser rejects it); use pipeline `requires:` or node `.Requires()`/`.Prefers()`/`.WhenRunner()`"},
	{regexp.MustCompile(`(?:\brun\b|\btrigger\b)[^\n]*\s--from\b`),
		"the git-ref flag is --sw-ref, not --from"},
	{regexp.MustCompile(`--mode=`),
		"the run-mode flag is --sw-mode, not --mode"},
	{regexp.MustCompile(`--workers=`),
		"the worker-cap flag is --sw-workers, not --workers"},
	{regexp.MustCompile(`--no-update\b`),
		"the skip-resolve flag is --sw-no-update, not --no-update"},
	{regexp.MustCompile(`tokens (?:revoke|lookup|rotate) [^-\s]`),
		"token verbs are flag-only: pass --prefix <prefix>, not a positional argument"},
}

// narrativeExempt names the one doc where change/deprecation vocabulary
// is the subject matter rather than rot: the changelog style guide
// teaches how to write changelog entries (which inherently record
// change), so bannedNarrative is not enforced there.
const narrativeExempt = "changelog-style.md"

// bannedNarrative lists history- and deprecation-narrative phrasings.
// Docs describe what IS, not what changed or what is going away; that
// story belongs in pkg/docs/content/migrations/ (gate-excluded) or the
// CHANGELOG, not the reference pages. These are scanned everywhere
// except narrativeExempt. Kept high-precision on purpose -- fuzzy words
// ("no longer"/"replaced"/"previously") have legitimate present-tense
// uses and are left to review.
var bannedNarrative = []bannedPattern{
	{regexp.MustCompile(`(?i)(?:pre|post)-rewrite`),
		"don't narrate the rewrite; describe current behavior directly (history goes in migrations/)"},
	{regexp.MustCompile(`(?i)\bformerly\b`),
		"don't narrate renames; describe the current name directly (history goes in migrations/)"},
	{regexp.MustCompile(`(?i)^#+\s+historical`),
		"remove historical sections; change history belongs in pkg/docs/content/migrations/"},
	{regexp.MustCompile(`(?i)\bdeprecat(?:e|ed|es|ing|ion)\b`),
		"don't mark things deprecated in the reference docs; remove the feature or document its replacement as current (deprecation notices go in the CHANGELOG / migrations/)"},
	{regexp.MustCompile(`(?i)\bobsolete\b`),
		"don't flag things obsolete in the reference docs; describe the current way directly (history goes in migrations/)"},
}

// checkBannedTokens scans the bundled docs and the CLI help registry for
// any dead token in `banned`. Returns false on any hit.
func checkBannedTokens(repoRoot string) bool {
	targets := []string{filepath.Join(repoRoot, "cmd", "sparkwing", "help_registry.go")}
	contentDir := filepath.Join(repoRoot, "pkg", "docs", "content")
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		// migrations/ and proposals/ are design history and may show old
		// or future names on purpose -- same exclusion as the go-block gate.
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		targets = append(targets, path)
		return nil
	})

	var hits []string
	for _, path := range targets {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Println("banned-tokens: read error:", err)
			return false
		}
		rel, _ := filepath.Rel(repoRoot, path)
		patterns := banned
		if filepath.Base(path) != narrativeExempt {
			patterns = append(append([]bannedPattern{}, banned...), bannedNarrative...)
		}
		for ln, line := range strings.Split(string(data), "\n") {
			for _, b := range patterns {
				if m := b.re.FindString(line); m != "" {
					hits = append(hits, fmt.Sprintf("%s:%d: %q -- %s", rel, ln+1, m, b.want))
				}
			}
		}
	}

	fmt.Printf("doccheck/banned-tokens: %d+%d pattern(s) over docs + help registry -- %d hit(s)\n",
		len(banned), len(bannedNarrative), len(hits))
	if len(hits) > 0 {
		fmt.Printf("\n%d dead token(s) that must not reappear:\n\n", len(hits))
		for _, h := range hits {
			fmt.Println("  " + h)
		}
		return false
	}
	fmt.Println("\nNO DEAD TOKENS IN DOCS OR HELP")
	return true
}
