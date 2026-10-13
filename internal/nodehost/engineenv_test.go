package nodehost

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// safety: names engine Go code reads or sets that are not the engine's own and so reach pipeline code: sparks and
// SDK settings, the caller's sparkwing configuration and cache URLs, run metadata and test helpers. A trailing _
// matches a prefix. A name engine code adds must land here or in engineEnv, or this test fails.
var passesThrough = []string{
	"SPARKWING_ADMISSION_CLASS", "SPARKWING_LOG_FORMAT", "SPARKWING_TOOLCHAIN", "SPARKWING_TOOLCHAIN_ACTIVE",
	"SPARKWING_WINGD_BIN", "SPARKWING_CACHE_URL", "SPARKWING_GITCACHE_URL", "SPARKWING_DRY_RUN", "SPARKWING_DEBUG",
	"SPARKWING_GATE_INDEX", "SPARKWING_HOME", "SPARKWING_CONFIG", "SPARKWING_CONFIG_ENV", "SPARKWING_FLEET_CONFIG",
	"SPARKWING_PROFILES", "SPARKWING_REPOS", "SPARKWING_REPO_", "SPARKWING_SECRETS", "SPARKWING_RELEASE_SIGNING_KEY",
	"SPARKWING_BASH_PROGRAM", "SPARKWING_BRANCH", "SPARKWING_COMMIT", "SPARKWING_REPO", "SPARKWING_TRIGGER_SOURCE",
	"SPARKWING_RUNNER_", "SPARKWING_HOOK_SELFTEST", "SPARKWING_TESTLEAK_HOST", "SPARKWING_TEST_",
}

// safety: SPARKWING_ tokens in engine code that no process reads from its environment: banned doc tokens,
// redaction patterns, service settings and comments.
var notNodeEnv = []string{
	"SPARKWING_BOX_", "SPARKWING_BUDGET", "SPARKWING_CACHE_ALLOW_UNAUTHENTICATED", "SPARKWING_CACHE_PUBLIC_URL",
	"SPARKWING_PG_URL", "SPARKWING_PLAN_ADMISSION", "SPARKWING_REQUIRE_AUTH", "SPARKWING_WORK_DIR", "SPARKWING_",
}

func listed(list []string, key string) bool {
	return slices.ContainsFunc(list, func(name string) bool {
		if strings.HasSuffix(name, "_") {
			return strings.HasPrefix(key, name)
		}
		return key == name
	})
}

var envLiteral = regexp.MustCompile(`"(_?SPARKWING_[A-Z0-9_]*)`)

func TestEveryEngineEnvNameIsClassified(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := map[string]string{}
	for _, dir := range []string{"cmd", "internal", "pkg", "sparkwing"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range envLiteral.FindAllStringSubmatch(string(data), -1) {
				seen[m[1]] = path
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "environment-variables.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile("`(SPARKWING_[A-Z0-9_]+)`").FindAllStringSubmatch(string(doc), -1) {
		seen[m[1]] = "docs/environment-variables.md"
	}
	if len(seen) < 100 {
		t.Fatalf("found only %d SPARKWING_ names; the scan is not reading engine code", len(seen))
	}
	for name, where := range seen {
		switch {
		case name == "SPARKWING_" || listed(notNodeEnv[:len(notNodeEnv)-1], name):
		case engineOwned(name) && listed(passesThrough, name):
			t.Errorf("%s (%s) is both denied to nodes and listed as passing through", name, where)
		case !engineOwned(name) && !listed(passesThrough, name):
			t.Errorf("%s (%s) is neither the engine's (engineEnv, stripped from nodes) nor listed as passing through to "+
				"pipeline code; classify it", name, where)
		}
	}
}

func TestPipelineEnvRemovesEngineCredentials(t *testing.T) {
	for _, key := range []string{
		"SPARKWING_SECRETS_KEY", "SPARKWING_SECRETS_PREVIOUS_KEY", "SPARKWING_EXECUTION_CAPABILITY",
		"SPARKWING_FLEET_PARENT_TOKEN", "SPARKWING_SOURCE_TOKEN", "sparkwing_agent_token", "_SPARKWING_SUBMIT_PRIORITY",
	} {
		got := PipelineEnv([]string{key + "=synthetic", "SPARKWING_GATE_INDEX=staged", "SPARKWING_TEST_PG_URL=caller"})
		if !slices.Equal(got, []string{"SPARKWING_GATE_INDEX=staged", "SPARKWING_TEST_PG_URL=caller"}) {
			t.Errorf("%s: pipeline env = %v, want the engine's credential gone and the caller's settings kept", key, got)
		}
	}
}

func TestPlanSessionSeesTheDryRunSwitch(t *testing.T) {
	cfg := Config{Session: SessionConfig{Env: []string{"PATH=/bin"}}}
	cfg.Run.DryRun = true
	if got := planSession(cfg).Env; !slices.Equal(got, []string{"PATH=/bin", "SPARKWING_DRY_RUN=1"}) {
		t.Errorf("plan env = %v, want SPARKWING_DRY_RUN=1 as its nodes get", got)
	}
	if cfg.Session.Env[len(cfg.Session.Env)-1] != "PATH=/bin" {
		t.Error("planSession changed the attempts' base environment")
	}
}
