package repos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

const killedSaveEntries = 100000

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
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	waitForStagingFile(t, dir)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait()
	waited = true

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the registry a killed writer left does not parse: %v", err)
	}
	if !isOriginalRegistry(cfg) && !isKilledChildRegistry(cfg) {
		t.Fatalf("the interrupted save replaced the previous registry: %+v", cfg.Repos)
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
	cfg := &Config{FallbackPaths: []string{"~/code"}, Repos: make([]*Entry, 0, killedSaveEntries)}
	for i := range killedSaveEntries {
		cfg.Repos = append(cfg.Repos, &Entry{Path: fmt.Sprintf("/child/checkout-%06d", i)})
	}
	for {
		if err := Save(path, cfg); err != nil {
			return
		}
	}
}

func isOriginalRegistry(cfg *Config) bool {
	return len(cfg.FallbackPaths) == 1 && cfg.FallbackPaths[0] == "~/code" &&
		len(cfg.Repos) == 1 && cfg.Repos[0] != nil && cfg.Repos[0].Path == "/keep/me"
}

func isKilledChildRegistry(cfg *Config) bool {
	if len(cfg.FallbackPaths) != 1 || cfg.FallbackPaths[0] != "~/code" || len(cfg.Repos) != killedSaveEntries {
		return false
	}
	for i, entry := range cfg.Repos {
		if entry == nil || entry.Path != fmt.Sprintf("/child/checkout-%06d", i) {
			return false
		}
	}
	return true
}

func waitForStagingFile(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(dir, ".repos-*.yaml"))
		if err != nil {
			t.Fatalf("match staging file: %v", err)
		}
		if len(matches) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the child never exposed an in-progress staging file")
}
