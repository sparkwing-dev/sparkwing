package userconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// safety: the config directory sits inside the sparkwing home, or the sandbox
// guard refuses the migration's writes.
func legacyHome(t *testing.T) (dir, path string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv(PathEnv, "")
	dir = filepath.Join(root, "config", "sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := legacyValidators
	legacyValidators = map[string]func(*yaml.Node) error{}
	t.Cleanup(func() { legacyValidators = saved })
	for _, s := range sections {
		RegisterLegacyValidator(s, func(*yaml.Node) error { return nil })
	}
	return dir, filepath.Join(dir, Filename)
}

func writeLegacy(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readBody(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestMigrationMovesEveryLegacyFileIntoItsSection(t *testing.T) {
	dir, path := legacyHome(t)
	writeLegacy(t, dir, "admission.yaml", "mode: auto\n")
	writeLegacy(t, dir, "budget", "# leave room for the desktop\n\n50%,8gb\n")
	writeLegacy(t, dir, "agent.yaml", "controller: http://ctrl\ntoken: tok\n")
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:4346\n")
	writeLegacy(t, dir, "profiles.yaml", "profiles:\n  prod:\n    controller: {url: http://ctrl}\n")
	writeLegacy(t, dir, "repos.yaml", "repos:\n  - path: /src/a\nfallback_paths: [~/code]\n")

	var admission struct {
		Mode   string `yaml:"mode"`
		Budget string `yaml:"budget"`
	}
	if _, err := Read(path, Admission, &admission); err != nil {
		t.Fatal(err)
	}
	if admission.Mode != "auto" || admission.Budget != "50%,8gb" {
		t.Fatalf("admission = %+v, want mode and budget moved", admission)
	}
	for _, want := range []struct{ section, key string }{
		{Agent, "token"}, {Fleet, "listen"}, {Profiles, "prod"}, {Repos, "fallback_paths"},
	} {
		got := map[string]any{}
		if found, err := Read(path, want.section, &got); err != nil || !found || got[want.key] == nil {
			t.Errorf("%s = %v, %v, %v; want %s moved", want.section, got, found, err, want.key)
		}
	}
	for _, name := range []string{"admission.yaml", "budget", "agent.yaml", "fleet.yaml", "profiles.yaml", "repos.yaml"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still in place", name)
		}
		if _, err := os.Lstat(filepath.Join(dir, name+MigratedSuffix)); err != nil {
			t.Errorf("%s was not kept as %s%s: %v", name, name, MigratedSuffix, err)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("config.yaml mode = %v, %v; want 0600", info, err)
	}
	if got := Leftovers(); len(got) != 0 {
		t.Errorf("Leftovers = %+v after migrating", got)
	}
}

func TestMigrationMergesIntoAnExistingFileAndKeepsItsComments(t *testing.T) {
	dir, path := legacyHome(t)
	if err := os.WriteFile(path, []byte("# my machine\nprofiles:\n  # home lab\n  lab: {}\nadmission:\n  mode: auto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLegacy(t, dir, "budget", "6\n")
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:4346\n")

	if err := MigrateLegacy(); err != nil {
		t.Fatal(err)
	}
	body := readBody(t, path)
	for _, want := range []string{"# my machine", "# home lab", "lab: {}", "mode: auto", `budget: "6"`, "listen: 127.0.0.1:4346"} {
		if !strings.Contains(body, want) {
			t.Errorf("config.yaml lost or lacks %q:\n%s", want, body)
		}
	}
}

func TestMigrationRefusesALegacyFileThatDisagreesWithItsSection(t *testing.T) {
	dir, path := legacyHome(t)
	existing := "fleet:\n  listen: 127.0.0.1:1\nadmission:\n  budget: \"4\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:2\n")
	writeLegacy(t, dir, "budget", "6\n")
	writeLegacy(t, dir, "repos.yaml", "repos:\n  - path: /src/a\n")

	var got map[string]any
	_, err := Read(path, Profiles, &got)
	for _, want := range []string{filepath.Join(dir, "fleet.yaml"), "the fleet section", filepath.Join(dir, "budget"), "admission.budget"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Read error = %v, want it to name %q", err, want)
		}
	}
	if body := readBody(t, path); body != existing {
		t.Fatalf("config.yaml changed on a refused migration:\n%s", body)
	}
	for _, name := range []string{"fleet.yaml", "budget", "repos.yaml"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was moved aside on a refused migration: %v", name, err)
		}
	}
}

func TestMigrationFinishesAfterACrashBetweenWriteAndRename(t *testing.T) {
	dir, path := legacyHome(t)
	written := "admission:\n  mode: auto\n  budget: 6\nrepos:\n  repos:\n    - path: /src/a\n"
	if err := os.WriteFile(path, []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLegacy(t, dir, "admission.yaml", "mode: auto\n")
	writeLegacy(t, dir, "budget", "6\n")
	writeLegacy(t, dir, "repos.yaml", "repos:\n  - path: /src/a\n")

	if err := MigrateLegacy(); err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, path); body != written {
		t.Fatalf("config.yaml changed when its sections already held the legacy content:\n%s", body)
	}
	if got := Leftovers(); len(got) != 0 {
		t.Fatalf("Leftovers = %+v, want every file set aside", got)
	}
}

func TestMigrationRunsOnce(t *testing.T) {
	dir, path := legacyHome(t)
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:4346\n")
	if err := MigrateLegacy(); err != nil {
		t.Fatal(err)
	}
	first := readBody(t, path)
	if err := MigrateLegacy(); err != nil {
		t.Fatal(err)
	}
	if second := readBody(t, path); second != first {
		t.Fatalf("a second migration changed config.yaml:\n%s\nthen\n%s", first, second)
	}
}

func TestMigrationRefusesContentItsSectionRejects(t *testing.T) {
	dir, path := legacyHome(t)
	RegisterLegacyValidator(Fleet, func(*yaml.Node) error { return errors.New("listen must be a fixed host:port") })
	writeLegacy(t, dir, "fleet.yaml", "listen: nowhere\n")
	writeLegacy(t, dir, "repos.yaml", "repos: []\nfallback_paths: [~/code]\n")

	err := MigrateLegacy()
	if err == nil || !strings.Contains(err.Error(), "listen must be a fixed host:port") || !strings.Contains(err.Error(), "fleet.yaml") {
		t.Fatalf("MigrateLegacy error = %v, want the section's refusal naming the file", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config.yaml was written for invalid content: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "fleet.yaml")); err != nil {
		t.Fatalf("fleet.yaml was moved aside: %v", err)
	}
}

func TestMigrationLeavesAFileNoLinkedOwnerCanValidate(t *testing.T) {
	dir, path := legacyHome(t)
	delete(legacyValidators, Fleet)
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:4346\n")
	var got map[string]any
	if _, err := Read(path, Profiles, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "fleet.yaml")); err != nil {
		t.Fatalf("fleet.yaml moved without a validator: %v", err)
	}
}

func TestMigrationFollowsTheOverrideButReadsTheConfigDirectory(t *testing.T) {
	dir, _ := legacyHome(t)
	elsewhere := filepath.Join(filepath.Dir(dir), "elsewhere.yaml")
	t.Setenv(PathEnv, elsewhere)
	writeLegacy(t, dir, "profiles.yaml", "profiles:\n  prod: {}\n")

	got := map[string]any{}
	if found, err := Read(elsewhere, Profiles, &got); err != nil || !found || got["prod"] == nil {
		t.Fatalf("Read = %v, %v, %v; want the profile moved into the override", got, found, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration wrote the config directory's config.yaml instead of the override: %v", err)
	}
}

func TestLegacyPathVariableRefuses(t *testing.T) {
	_, path := legacyHome(t)
	t.Setenv("SPARKWING_PROFILES", "/somewhere/profiles.yaml")
	var got fleetish
	_, err := Read(path, Fleet, &got)
	if err == nil || !strings.Contains(err.Error(), "SPARKWING_PROFILES no longer moves any setting") {
		t.Fatalf("Read error = %v, want the variable named", err)
	}
	if got := Leftovers(); len(got) != 1 || got[0].Name != "SPARKWING_PROFILES" {
		t.Fatalf("Leftovers = %+v", got)
	}
}

func TestALegacyPathReadsTheConfigThatReplacedIt(t *testing.T) {
	dir, path := legacyHome(t)
	agentYAML := filepath.Join(dir, "agent.yaml")
	writeLegacy(t, dir, "agent.yaml", "controller: http://ctrl\ntoken: tok\n")

	for range 2 {
		got := map[string]any{}
		if found, err := Read(agentYAML, Agent, &got); err != nil || !found || got["token"] != "tok" {
			t.Fatalf("Read(agent.yaml) = %v, %v, %v; want the agent section of config.yaml", got, found, err)
		}
	}
	if !strings.Contains(readBody(t, path), "token: tok") {
		t.Fatalf("config.yaml lacks the moved agent section:\n%s", readBody(t, path))
	}
}
