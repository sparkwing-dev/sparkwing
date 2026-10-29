package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// safety: CHANGELOG.md, docs/migrations/ and design/rewrite/contracts/ name these
// surfaces as history, so the scan covers only text describing the shipped product.
var retiredNames = []struct{ name, use string }{
	{"--free-team-slots", "automatic free and paid storage tiers"},
	{"max_global_runs_per_hour", "per-principal hourly limits and prepaid credits"},
	{"--trigger-dedupe-window", "an explicit idempotency key"},
	{"triggerDedupeWindow", "an explicit idempotency key"},
	{"--argon2-memory-budget-mb", "nothing; the Argon2 verifier is gone"},
	{"argon2MemoryBudgetMB", "nothing; the Argon2 verifier is gone"},
	{"controller.requireAuth", "nothing; the chart always passes --require-auth"},
	{"controller.allowOpenBootstrap", "nothing; there is no open bootstrap window"},
	{"runner.trustClass", "launcher.enabled and launcher.tokenSecret"},
	{"runner.reservedPools", "launcher.enabled and launcher.tokenSecret"},
	{"runs bounce", "sparkwing runs retry"},
	{"debug rerun", "sparkwing runs retry"},
	{"debug replay", "pipeline plan --pipeline NAME"},
	{"handle-trigger", "sparkwing run, which hosts the run itself"},
	{"--sw-local-only", "nothing; a run records to this machine's store"},
	{"SPARKWING_LOCAL_ONLY", "nothing; a run records to this machine's store"},
	{"SPARKWING_ALLOW_UNADMITTED", "--sw-allow-unadmitted"},
	{"SPARKWING_LEASE_TOKEN", "the admission daemon lease a child attaches to"},
	{"SPARKWING_CHILD_LEASE_TOKEN", "the admission daemon lease a child attaches to"},
	{"SPARKWING_API_SOCKET", "the node protocol channel"},
	{"SPARKWING_MASK_VALUES_FD", "the node protocol channel"},
	{"SPARKWING_EXECUTION_CAPABILITY_STDIN", "the awarded claim capability"},
	{"SPARKWING_BROKERED_NODE_CLAIM", "the awarded claim capability"},
	{"SPARKWING_LOGS_URL", "controller-proxied log segments"},
	{"sparkwing-logs", "controller-proxied log segments"},
	{"--logs-url", "controller-proxied log segments"},
	{"--logs-archive-store", "--cache-blob-store"},
	{"--log-store", "--cache-blob-store"},
	{"/api/v1/team/billing/pay", "PUT /api/v1/team/billing/refill"},
	{"/api/v1/team/billing/budget", "PUT /api/v1/team/billing/refill"},
	{"/api/v1/credits/grants", "verified checkout settlement or an operator grant"},
	{"/git-credential", "POST /api/v1/runs/{id}/source-credential"},
	{"/source-token", "POST /api/v1/runs/{id}/source-credential"},
	{"include=secret_values", "nothing; secret values are never served"},
	{"pkg/secretname.ErrMissing", "sparkwing.ErrSecretMissing"},
	{"WithConcurrencyStore", "the admission owner; groups are read-only over HTTP"},
	{"unit_asks", "ranking holds on the job"},
	{"RankingAward", "ranking holds on the job"},
	{"AcquireSingletonLease", "the one-controller advisory lock"},
	{"ReplicaID", "the one-controller advisory lock"},
	{"collection_session", "prepaid credits with automatic refill"},
	{"pay-now", "prepaid credits with automatic refill"},
	{"--grace-seconds", "prepaid credits; there is no grace window"},
	{"--max-charge-seconds", "prepaid credits; there is no post-paid charge"},
	{"trigger_claim_tokens", "awarded claim capabilities (swk_/swn_)"},
}

var retiredExtraGlobs = []string{
	"cmd/sparkwing/help_registry.go",
	"internal/clisurface/registry.go",
	"design/rewrite/ORIENTATION.md",
	"design/rewrite/ADMISSION.md",
	"design/rewrite/RUN-ANYWHERE.md",
	"internal/*/[A-Z]*.md",
	"internal/*/*/[A-Z]*.md",
}

var retiredPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(retiredNames))
	for i, r := range retiredNames {
		out[i] = regexp.MustCompile(`(?:^|[^\w-])` + regexp.QuoteMeta(r.name) + `(?:$|[^\w-])`)
	}
	return out
}()

func retiredNameTargets(repoRoot string) []string {
	targets := auxDocFiles(repoRoot)
	for _, g := range retiredExtraGlobs {
		matches, _ := filepath.Glob(filepath.Join(repoRoot, g))
		targets = append(targets, matches...)
	}
	// #nosec G703 -- a build-time tool reading paths the operator names
	_ = filepath.Walk(filepath.Join(repoRoot, "docs"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".md") && !strings.Contains(path, "/migrations/") {
			targets = append(targets, path)
		}
		return err
	})
	return targets
}

func checkRetiredNames(repoRoot string) bool {
	targets := retiredNameTargets(repoRoot)
	var hits []string
	for _, path := range targets {
		// #nosec G703 -- a build-time tool reading paths the operator names
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Println("retired-names: read error:", err)
			return false
		}
		rel, _ := filepath.Rel(repoRoot, path)
		for ln, line := range strings.Split(string(data), "\n") {
			if rejectedEnvironmentRow(line) {
				continue
			}
			for i, re := range retiredPatterns {
				if re.MatchString(line) {
					hits = append(hits, fmt.Sprintf("%s:%d: %q is retired; use %s", rel, ln+1, retiredNames[i].name, retiredNames[i].use))
				}
			}
		}
	}
	fmt.Printf("doccheck/retired-names: %d name(s) over %d file(s) -- %d hit(s)\n", len(retiredNames), len(targets), len(hits))
	for _, h := range hits {
		fmt.Println("  " + h)
	}
	return len(hits) == 0
}
