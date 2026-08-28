package repos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSave_ConcurrentWritersNeverLeaveAnUnparseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	const writers = 24

	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := &Config{FallbackPaths: []string{"~/code"}}
			for j := 0; j <= i*8; j++ {
				cfg.Repos = append(cfg.Repos, &Entry{Path: fmt.Sprintf("/checkout/number-%d-%d", i, j)})
			}
			<-start
			errs[i] = Save(path, cfg)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d failed: %v", i, err)
		}
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the registry %d concurrent writers left does not parse: %v", writers, err)
	}
	if len(cfg.FallbackPaths) != 1 || cfg.FallbackPaths[0] != "~/code" {
		t.Errorf("fallback_paths came out as %v, want exactly [~/code]: the file is a blend of two writes", cfg.FallbackPaths)
	}
	if len(cfg.Repos) == 0 {
		t.Error("the surviving registry has no entries, so no single writer's config won")
	}
	for _, e := range cfg.Repos {
		if !strings.HasPrefix(e.Path, "/checkout/number-") {
			t.Fatalf("entry %q is not a whole path any writer wrote", e.Path)
		}
	}
}

func TestSave_LeavesNoStagingFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	for i := range 5 {
		if err := Save(path, &Config{Repos: []*Entry{{Path: fmt.Sprintf("/r%d", i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "repos.yaml" {
			t.Errorf("staging file %q survived the save", e.Name())
		}
	}
}

func TestSave_AFailedWriteLeavesThePreviousRegistryIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	good := &Config{Repos: []*Entry{{Path: "/keep/me"}}, FallbackPaths: []string{"~/code"}}
	if err := Save(path, good); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := Save(path, &Config{Repos: []*Entry{{Path: "/never/lands"}}}); err == nil {
		t.Fatal("Save into a read-only directory reported success")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the previous registry is gone after a failed save: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a failed save rewrote the registry:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the surviving registry does not parse: %v", err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Path != "/keep/me" {
		t.Errorf("the surviving registry lost its entry: %+v", cfg.Repos)
	}
}

const killSentinel = "SPARKWING_TEST_SAVE_UNTIL_KILLED"

func TestSave_AProcessKilledMidWriteLeavesThePreviousRegistryIntact(t *testing.T) {
	if os.Getenv(killSentinel) != "" {
		saveUntilKilled(os.Getenv(killSentinel))
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	good := &Config{Repos: []*Entry{{Path: "/keep/me"}}, FallbackPaths: []string{"~/code"}}
	if err := Save(path, good); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0],
		"-test.run", "^TestSave_AProcessKilledMidWriteLeavesThePreviousRegistryIntact$")
	cmd.Env = append(os.Environ(), killSentinel+"="+path)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	waitForGrowth(t, path)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait()

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the registry a killed writer left does not parse: %v", err)
	}
	if len(cfg.Repos) == 0 {
		t.Fatal("the registry came back empty, so the kill lost the file")
	}
	for _, e := range cfg.Repos {
		if e.Path != "/keep/me" && !strings.HasPrefix(e.Path, "/child/") {
			t.Errorf("entry %q belongs to neither the original nor the child config", e.Path)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "repos.yaml" && !strings.HasPrefix(e.Name(), ".repos-") {
			t.Errorf("unexpected leftover %q", e.Name())
		}
	}
}

func saveUntilKilled(path string) {
	cfg := &Config{FallbackPaths: []string{"~/code"}}
	for i := 0; ; i++ {
		cfg.Repos = append(cfg.Repos, &Entry{Path: fmt.Sprintf("/child/checkout-%06d", i)})
		if err := Save(path, cfg); err != nil {
			return
		}
	}
}

func waitForGrowth(t *testing.T, path string) {
	t.Helper()
	for range 20000 {
		cfg, err := Load(path)
		if err == nil && len(cfg.Repos) > 200 {
			return
		}
	}
	t.Fatal("the child never wrote a registry large enough to interrupt")
}
