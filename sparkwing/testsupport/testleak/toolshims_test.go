package testleak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestToolResolutionPinsTheRealAsdfAndMiseLocations(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	pkg := filepath.Join(repo, "charts")
	writeFile(t, filepath.Join(home, ".asdf", "shims", "helm"), "")
	writeFile(t, filepath.Join(home, ".asdfrc"), "")
	writeFile(t, filepath.Join(home, ".fixture-tool-versions"), "golang 1.26.9\nhelm 4.2.4 4.1.0 # pinned\nnodejs-lts 26.8.1\n")
	writeFile(t, filepath.Join(repo, ".fixture-tool-versions"), "helm 4.3.0\n")
	if err := os.MkdirAll(pkg, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config", "mise", "config.toml"), "")
	writeFile(t, filepath.Join(home, ".local", "share", "mise", "installs", ".keep"), "")

	set := map[string]string{"ASDF_NODEJS_LTS_VERSION": "already", "ASDF_DEFAULT_TOOL_VERSIONS_FILENAME": ".fixture-tool-versions"}
	env := toolResolutionEnv(home, pkg, func(k string) string { return set[k] })
	want := map[string]string{
		"ASDF_DATA_DIR":       filepath.Join(home, ".asdf"),
		"ASDF_CONFIG_FILE":    filepath.Join(home, ".asdfrc"),
		"ASDF_GOLANG_VERSION": "1.26.9",
		"ASDF_HELM_VERSION":   "4.3.0",
		"MISE_CONFIG_DIR":     filepath.Join(home, ".config", "mise"),
		"MISE_DATA_DIR":       filepath.Join(home, ".local", "share", "mise"),
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["ASDF_NODEJS_LTS_VERSION"]; ok {
		t.Error("a version the developer already set was replaced")
	}
	if len(env) != len(want) {
		t.Errorf("env = %v, want exactly %v", env, want)
	}
}

func TestToolResolutionPinsNothingWithoutAVersionManager(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".tool-versions"), "helm 4.2.4\n")
	if env := toolResolutionEnv(home, t.TempDir(), func(string) string { return "" }); len(env) != 0 {
		t.Fatalf("env = %v, want none when neither asdf nor mise is installed", env)
	}
}

func TestGoRootGoesFirstOnPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := goRootFirstOnPath(root, "/usr/bin")
	if want := filepath.Join(root, "bin") + string(os.PathListSeparator) + "/usr/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
	if goRootFirstOnPath(filepath.Join(root, "missing"), "/usr/bin") != "" {
		t.Fatal("a GOROOT without a bin directory changed PATH")
	}
}

func TestTheSuiteRunsGoFromItsOwnToolchain(t *testing.T) {
	path := os.Getenv("PATH")
	first, _, _ := strings.Cut(path, string(os.PathListSeparator))
	if !strings.HasSuffix(first, string(filepath.Separator)+"bin") {
		t.Skip("go env GOROOT was unavailable when the sandbox started")
	}
	if _, err := os.Stat(filepath.Join(first, "go")); err != nil {
		if _, err := os.Stat(filepath.Join(first, "go.exe")); err != nil {
			t.Fatalf("PATH starts with %q, which holds no go binary", first)
		}
	}
}
