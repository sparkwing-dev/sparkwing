package userconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
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
	if err := fssecure.WriteFile(filepath.Join(dir, name), []byte(body)); err != nil {
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

func assertUntouched(t *testing.T, dir string, originals map[string]string) {
	t.Helper()
	for name, want := range originals {
		if got := readBody(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s changed to %q, want it left as %q", name, got, want)
		}
	}
}

func TestMigrationCopiesEveryLegacyFileAndLeavesItInPlace(t *testing.T) {
	dir, path := legacyHome(t)
	originals := map[string]string{
		"admission.yaml": "mode: auto\n",
		"budget":         "# leave room for the desktop\n\n50%,8gb\n",
		"agent.yaml":     "controller: http://ctrl\ntoken: tok\n",
		"fleet.yaml":     "listen: 127.0.0.1:4346\n",
		"profiles.yaml":  "profiles:\n  prod:\n    controller: {url: http://ctrl}\n",
		"repos.yaml":     "repos:\n  - path: /src/a\nfallback_paths: [~/code]\n",
	}
	for name, body := range originals {
		writeLegacy(t, dir, name, body)
	}

	var admission struct {
		Mode   string `yaml:"mode"`
		Budget string `yaml:"budget"`
	}
	if _, err := Read(path, Admission, &admission); err != nil {
		t.Fatal(err)
	}
	if admission.Mode != "auto" || admission.Budget != "50%,8gb" {
		t.Fatalf("admission = %+v, want mode and budget copied", admission)
	}
	for _, want := range []struct{ section, key string }{
		{Agent, "token"}, {Fleet, "listen"}, {Profiles, "prod"}, {Repos, "fallback_paths"},
	} {
		got := map[string]any{}
		if found, err := Read(path, want.section, &got); err != nil || !found || got[want.key] == nil {
			t.Errorf("%s = %v, %v, %v; want %s copied", want.section, got, found, err, want.key)
		}
	}
	assertUntouched(t, dir, originals)
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private file mode = %v, %v; want 0600", info, err)
		}
	}
	if file, err := fssecure.OpenPrivateConfig(path); err != nil {
		t.Fatalf("config.yaml is not private: %v", err)
	} else {
		_ = file.Close()
	}
	for _, l := range Leftovers() {
		if !l.Copied {
			t.Errorf("Leftover %+v is not marked copied", l)
		}
	}
}

func TestMigrationMergesIntoAnExistingFileAndKeepsItsComments(t *testing.T) {
	dir, path := legacyHome(t)
	if err := fssecure.WriteFile(path, []byte("# my machine\nprofiles:\n  # home lab\n  lab: {}\nadmission:\n  mode: auto\n")); err != nil {
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

func TestMigrationIgnoresALegacyFileWhoseSectionExists(t *testing.T) {
	dir, path := legacyHome(t)
	existing := "fleet:\n  listen: 127.0.0.1:1\nadmission:\n  budget: \"4\"\n"
	if err := fssecure.WriteFile(path, []byte(existing)); err != nil {
		t.Fatal(err)
	}
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:2\n")
	writeLegacy(t, dir, "budget", "6\n")

	var got map[string]any
	if _, err := Read(path, Fleet, &got); err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, path); body != existing {
		t.Fatalf("config.yaml changed although its sections exist:\n%s", body)
	}
}

func TestMigrationRunsOnceAndSurvivesARepeat(t *testing.T) {
	dir, path := legacyHome(t)
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:4346\n")
	if err := MigrateLegacy(); err != nil {
		t.Fatal(err)
	}
	first := readBody(t, path)
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:9999\n")
	if err := MigrateLegacy(); err != nil {
		t.Fatal(err)
	}
	if second := readBody(t, path); second != first {
		t.Fatalf("a second migration changed config.yaml:\n%s\nthen\n%s", first, second)
	}
}

func TestMigrationRefusesOnlyTheSectionItsInvalidFileFeeds(t *testing.T) {
	dir, path := legacyHome(t)
	RegisterLegacyValidator(Fleet, func(section *yaml.Node) error {
		var cfg struct {
			Listen string `yaml:"listen"`
		}
		return DecodeStrict(section, &cfg)
	})
	writeLegacy(t, dir, "fleet.yaml", "listen: 127.0.0.1:1\nsecret_token: nope\n")
	writeLegacy(t, dir, "repos.yaml", "repos: []\nfallback_paths: [~/code]\n")

	var fleet map[string]any
	_, err := Read(path, Fleet, &fleet)
	want := filepath.Join(dir, "fleet.yaml") + " sets secret_token, which sparkwing does not accept; remove that key from " +
		filepath.Join(dir, "fleet.yaml") + " and rerun"
	if err == nil || err.Error() != want {
		t.Fatalf("Read(fleet) error = %v, want %q", err, want)
	}
	repos := map[string]any{}
	if found, err := Read(path, Repos, &repos); err != nil || !found {
		t.Fatalf("Read(repos) = %v, %v; an invalid fleet.yaml must not block the repos section", found, err)
	}
	if strings.Contains(readBody(t, path), "fleet") {
		t.Fatalf("config.yaml took the invalid fleet section:\n%s", readBody(t, path))
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
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config.yaml was written without a fleet validator: %v", err)
	}
}

func TestMigrationNeverCopiesIntoAnOverride(t *testing.T) {
	dir, _ := legacyHome(t)
	elsewhere := filepath.Join(filepath.Dir(dir), "elsewhere.yaml")
	t.Setenv(PathEnv, elsewhere)
	writeLegacy(t, dir, "profiles.yaml", "profiles:\n  prod: {controller: {token: secret}}\n")

	got := map[string]any{}
	if found, err := Read(elsewhere, Profiles, &got); err != nil || found {
		t.Fatalf("Read = %v, %v, %v; want the override read as it is", got, found, err)
	}
	for _, p := range []string{elsewhere, filepath.Join(dir, Filename)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migration wrote %s under an override: %v", p, err)
		}
	}
}

func TestMigrationUnderASandboxHomeReadsConfigAsItIs(t *testing.T) {
	dir, path := legacyHome(t)
	if err := fssecure.WriteFile(path, []byte("repos:\n  fallback_paths: [~/src]\n")); err != nil {
		t.Fatal(err)
	}
	writeLegacy(t, dir, "profiles.yaml", "profiles:\n  prod: {}\n")
	t.Setenv("SPARKWING_HOME", t.TempDir())

	repos := map[string]any{}
	if found, err := Read(path, Repos, &repos); err != nil || !found {
		t.Fatalf("Read = %v, %v; a sandboxed home must still read config.yaml", found, err)
	}
	if strings.Contains(readBody(t, path), "prod") {
		t.Fatal("migration wrote config.yaml from under a sandboxed home")
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
	if got := Leftovers(); len(got) != 1 || got[0].Name != "SPARKWING_PROFILES" || !got[0].Variable {
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
		t.Fatalf("config.yaml lacks the copied agent section:\n%s", readBody(t, path))
	}
	assertUntouched(t, dir, map[string]string{"agent.yaml": "controller: http://ctrl\ntoken: tok\n"})
}
