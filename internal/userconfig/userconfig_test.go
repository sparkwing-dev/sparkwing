package userconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), Filename)
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PathEnv, path)
	return path
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSectionReadValidatesWholeFile(t *testing.T) {
	path := writeFile(t, "repos: {}\nrunner: {max_concurrent: 0}\n")
	var got ReposConfig
	if _, err := Read(path, Repos, &got); err == nil {
		t.Fatal("ignored invalid unrelated section")
	}
	if _, err := Node(path, Repos); err == nil {
		t.Fatal("node ignored invalid unrelated section")
	}
}

func TestReadReportsAbsentFileOrSection(t *testing.T) {
	var got ReposConfig
	for _, path := range []string{filepath.Join(t.TempDir(), Filename), writeFile(t, "# empty\n"), writeFile(t, "profiles: {}\n")} {
		if found, err := Read(path, Repos, &got); err != nil || found {
			t.Fatalf("Read = %v, %v", found, err)
		}
	}
}

func TestReadRefusesAFileOthersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACL refusal is tested by fssecure")
	}
	path := writeFile(t, "repos: {}\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err == nil {
		t.Fatal("read public file")
	}
}

func TestUpdateKeepsOtherSectionsAndComments(t *testing.T) {
	path := writeFile(t, "# machine settings\nprofiles:\n  # production\n  prod:\n    controller:\n      url: https://example.com # connection\nrepos:\n  fallback_paths: [first]\n")
	var cfg ReposConfig
	err := Update(path, Repos, "repos", &cfg, func(found bool) error {
		if !found || cfg.FallbackPaths[0] != "first" {
			return errors.New("missing current value")
		}
		cfg.FallbackPaths = []string{"second"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# machine settings", "# production", "# connection", "second"} {
		if !strings.Contains(readBack(t, path), want) {
			t.Errorf("lost %s", want)
		}
	}
}

func TestRejectedUpdateLeavesFileUnchanged(t *testing.T) {
	path := writeFile(t, "cache: {max_entries: 1}\n")
	before := readBack(t, path)
	refused := errors.New("refused")
	var cfg CacheConfig
	if err := Update(path, Cache, "cache", &cfg, func(bool) error { return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if err := Write(path, Cache, "cache", map[string]int{"max_entries": -1}); err == nil {
		t.Fatal("wrote invalid config")
	}
	if after := readBack(t, path); after != before {
		t.Fatal("rejected edit changed file")
	}
}

func TestConcurrentWritersKeepAllUpdates(t *testing.T) {
	path := writeFile(t, "")
	const count = 6
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var cfg ReposConfig
			errs <- Update(path, Repos, "repos", &cfg, func(bool) error {
				cfg.Repos = append(cfg.Repos, RepoPath{Path: fmt.Sprintf("/repos/%d", i)})
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos.Repos) != count {
		t.Fatalf("lost updates: %+v", cfg.Repos)
	}
}

func TestPathHonorsOverrideAndSandbox(t *testing.T) {
	want := filepath.Join(t.TempDir(), "elsewhere.yaml")
	t.Setenv(PathEnv, want)
	if got, err := Path(); err != nil || got != want {
		t.Fatalf("%q %v", got, err)
	}
	t.Setenv(PathEnv, "")
	if _, err := Path(); err == nil {
		t.Fatal("explicit empty config path fell back to default")
	}
	if err := os.Unsetenv(PathEnv); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := Path()
	if err != nil || !strings.HasPrefix(got, os.TempDir()) {
		t.Fatalf("%q %v", got, err)
	}
}

func TestWritePreservesCommentsAndAliases(t *testing.T) {
	path := writeFile(t, "# machine\nprofiles:\n  prod: &shared\n    controller: {url: https://first.example}\n  lab: *shared\n")
	profiles := map[string]Profile{"prod": {Controller: Connection{URL: "https://second.example"}}, "lab": {Controller: Connection{URL: "https://second.example"}}}
	if err := Write(path, Profiles, "profiles", profiles); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadConfig(path)
	if err != nil || cfg.Profiles["lab"].Controller.URL != "https://second.example" {
		t.Fatalf("%+v %v", cfg, err)
	}
	path = writeFile(t, "# machine settings\n")
	if err := Write(path, Repos, "repos", ReposConfig{FallbackPaths: []string{"/code"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readBack(t, path), "# machine settings") {
		t.Fatal("lost comment")
	}
}

func TestWriteRetainsExplicitEmptyMapping(t *testing.T) {
	path := writeFile(t, "")
	if err := Write(path, Profiles, "profiles", map[string]Profile{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadConfig(path)
	if err != nil || cfg.Profiles == nil {
		t.Fatalf("empty mapping lost: %+v %v", cfg, err)
	}
}

func TestReadFindsSectionsUnderMergeKeys(t *testing.T) {
	path := writeFile(t, "<<: {cache: {max_entries: 3}}\n")
	var cfg CacheConfig
	found, err := Read(path, Cache, &cfg)
	if err != nil || !found || cfg.MaxEntries == nil || *cfg.MaxEntries != 3 {
		t.Fatalf("merged section missing: %v %+v %v", found, cfg, err)
	}
}
