package nodehost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestDirectoryCacheKeyMatchesSDK(t *testing.T) {
	property := func(body []byte, suffix uint32) bool {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "lock"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("deps-%x", suffix)
		want, err := sparkwing.Dir(name, sparkwing.KeyFromFile("lock")).Key(workspace)
		if err != nil {
			t.Fatal(err)
		}
		got, err := directoryCacheKey(t.Context(), workspace, nodeproto.CacheSpec{Name: name, Path: name, KeyScope: name, KeyFiles: []string{"missing", "lock"}})
		return err == nil && got == want
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestCacheDirectoryUsesPersistedBodyEnvironment(t *testing.T) {
	for _, resolver := range []string{"go-modules", "npm"} {
		t.Run(resolver, func(t *testing.T) {
			input, state, _, _ := executorFixture(t)
			input.Entry.Directory = t.TempDir()
			wanted := filepath.Join(t.TempDir(), "declared-cache")
			name := "GOMODCACHE"
			if resolver == "npm" {
				name = "npm_config_cache"
			}
			job := nodeproto.JobSpec{ID: input.NodeID, Env: map[string]nodeproto.EnvValue{name: {Value: wanted}}}
			state.row.Spec, _ = json.Marshal(job)
			environments := SDKEnvironment{Store: state, Base: procenv.Base{Ambient: []string{name + "=host-cache"}, Masker: secrets.NewMasker()}}
			got, err := environments.CacheDirectory(t.Context(), input, nodeproto.CacheSpec{Resolver: resolver})
			if err != nil || got != wanted {
				t.Fatalf("resolved cache=%q, %v", got, err)
			}
		})
	}
}

func TestCacheDirectoryDefaultsUseCapturedHome(t *testing.T) {
	input, state, _, _ := executorFixture(t)
	input.Entry.Directory = t.TempDir()
	home := t.TempDir()
	environments := SDKEnvironment{Store: state, Base: procenv.Base{Ambient: []string{"HOME=" + home, "USERPROFILE=" + home, "PATH="}, Masker: secrets.NewMasker()}}
	for _, resolver := range []string{"go-modules", "npm"} {
		got, err := environments.CacheDirectory(t.Context(), input, nodeproto.CacheSpec{Resolver: resolver})
		want := filepath.Join(home, "go", "pkg", "mod")
		if resolver == "npm" {
			want = filepath.Join(home, ".npm")
			if runtime.GOOS == "windows" {
				want = filepath.Join(home, "AppData", "Local", "npm-cache")
			}
		}
		if err != nil || got != want {
			t.Fatalf("%s default=%q, %v", resolver, got, err)
		}
	}
}
