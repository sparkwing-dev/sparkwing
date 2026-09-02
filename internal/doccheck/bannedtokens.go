package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type bannedPattern struct {
	re   *regexp.Regexp
	want string
}

var banned = []bannedPattern{
	{
		regexp.MustCompile(`pipelines\.yaml`),
		"the config file is sparkwing.yaml; the legacy pipelines.yaml name is a hard error",
	},
	{
		regexp.MustCompile(`\bsparkwing\.db\b`),
		"the SQLite store file is state.db, not sparkwing.db",
	},
	{
		regexp.MustCompile(`\.sparkwing/logs\b`),
		"per-run logs live under ~/.sparkwing/runs/<runID>/, not ~/.sparkwing/logs/",
	},
	{
		regexp.MustCompile(`ReservedFlagNames`),
		"removed; run control flags are sw-* prefixed, so pipelines own the full unprefixed flag namespace (no reserved-name collision)",
	},
	{
		regexp.MustCompile(`\bruns_on\b`),
		"not a sparkwing.yaml field (the strict parser rejects it); use pipeline `requires:` or node `.Requires()`/`.Prefers()`/`.WhenRunner()`",
	},
	{
		regexp.MustCompile(`(?:\brun\b|\btrigger\b)[^\n]*\s--from\b`),
		"the git-ref flag is --sw-ref, not --from",
	},
	{
		regexp.MustCompile(`--mode=`),
		"the run-mode flag is --sw-mode, not --mode",
	},
	{
		regexp.MustCompile(`--workers=`),
		"the worker-cap flag is --sw-workers, not --workers",
	},
	{
		regexp.MustCompile(`--no-update\b`),
		"the skip-resolve flag is --sw-no-update, not --no-update",
	},
	{
		regexp.MustCompile(`tokens (?:revoke|lookup|rotate) [^-\s]`),
		"token verbs are flag-only: pass --prefix <prefix>, not a positional argument",
	},
	{
		regexp.MustCompile(`--sw-box-slots\b`),
		"removed; local host admission is owned by the admission daemon, not a per-run box-slot cap",
	},
	{
		regexp.MustCompile(`--sw-no-wait\b`),
		"removed; runs queue in the admission daemon and Ctrl-C cancels the wait cleanly",
	},
	{
		regexp.MustCompile(`SPARKWING_BOX_SLOTS_PIN|SPARKWING_BOX_NO_WAIT`),
		"removed; local host admission is owned by the admission daemon",
	},
	{
		regexp.MustCompile(`SPARKWING_PLAN_ADMISSION`),
		"removed; children inherit admission by attaching to the parent's daemon lease (SPARKWING_LEASE_TOKEN)",
	},
	{
		regexp.MustCompile(`\bHostAdmission\b`),
		"removed from the SDK; host admission is universal and implicit, never a flag",
	},
	{
		regexp.MustCompile(`\bbox-slots\b`),
		"removed; the admission daemon owns host admission -- read it with `sparkwing queue` and clear leftovers with `sparkwing doctor`",
	},
	{
		regexp.MustCompile(`sparkwing maintenance\b`),
		"removed; the admission daemon converges local state without a sweep -- `sparkwing doctor` clears provably-dead leftovers",
	},
	{
		regexp.MustCompile(`SPARKWING_BOX_SLOT`),
		"removed; the admission daemon measures host capacity, so there is no box-slot cap or stall-ttl env to set",
	},
	{
		regexp.MustCompile(`sparkwing pipeline add\b`),
		"there is no `sparkwing pipeline add` verb; register a repo with `sparkwing configure xrepo add <path>`",
	},
	{
		regexp.MustCompile(`pipeline templates\b`),
		"the registry is browsed with `sparkwing examples`; `pipeline templates` is not a verb",
	},
	{
		regexp.MustCompile(`sparkwing run [^\n]*\s--dry-run\b`),
		"run-control flags are sw-prefixed: the dry-run flag is --sw-dry-run",
	},
	{
		regexp.MustCompile(`jobs:(?:read|write)`),
		"not a recognized token scope; the scope set is the runs./nodes./logs./triggers./approvals. family in auth.md (e.g. runs.read, runs.write)",
	},
	{
		regexp.MustCompile(`pipeline new\b[^\n]*--param\b`),
		"removed; `pipeline new --template` takes one of five shapes and renders no parameters (`--param` lives on `examples scaffold`, which registry entries are read through)",
	},
}

const narrativeExempt = "changelog-style.md"

var bannedNarrative = []bannedPattern{
	{
		regexp.MustCompile(`(?i)(?:pre|post)-rewrite`),
		"don't narrate the rewrite; describe current behavior directly (history goes in migrations/)",
	},
	{
		regexp.MustCompile(`(?i)\bformerly\b`),
		"don't narrate renames; describe the current name directly (history goes in migrations/)",
	},
	{
		regexp.MustCompile(`(?i)^#+\s+historical`),
		"remove historical sections; change history belongs in docs/migrations/",
	},
	{
		regexp.MustCompile(`(?i)\bdeprecat(?:e|ed|es|ing|ion)\b`),
		"don't mark things deprecated in the reference docs; remove the feature or document its replacement as current (deprecation notices go in the CHANGELOG / migrations/)",
	},
	{
		regexp.MustCompile(`(?i)\bobsolete\b`),
		"don't flag things obsolete in the reference docs; describe the current way directly (history goes in migrations/)",
	},
}

func checkBannedTokens(contentDir, repoRoot string) bool {
	targets := []string{filepath.Join(repoRoot, "cmd", "sparkwing", "help_registry.go")}
	// #nosec G703 -- a build-time tool reading paths the operator names
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		targets = append(targets, path)
		return nil
	})

	var hits []string
	for _, path := range targets {
		// #nosec G703 -- a build-time tool reading paths the operator names
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
