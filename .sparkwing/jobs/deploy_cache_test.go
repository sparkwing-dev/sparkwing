package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeployCLIUsesSharedCacheWithPrivateHomes(t *testing.T) {
	source := t.TempDir()
	t.Setenv("SPARKWING_HOME", source)
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "private-fallback"}[blocked], func(t *testing.T) {
			var previous string
			for range 2 {
				d := &deployer{tmp: t.TempDir(), tools: deployTools{
					say: func(context.Context, string) {},
					run: func(_ context.Context, c deployCommand) (string, error) {
						home := c.Env["SPARKWING_HOME"]
						if home == source || home == previous || c.Env["HOME"] == "" || c.Env["XDG_CONFIG_HOME"] == "" {
							t.Fatalf("CLI lost its private home: %+v", c.Env)
						}
						previous = home
						cache := filepath.Join(home, "cache", "pipelines")
						target, err := os.Readlink(cache)
						if !blocked && (err != nil || target != filepath.Join(source, "cache", "pipelines")) {
							t.Fatalf("CLI missed the runner cache: %s %v", target, err)
						}
						if blocked && err == nil {
							t.Fatal("existing private cache was replaced")
						}
						return "run-accepted", nil
					},
				}}
				if blocked {
					if err := os.MkdirAll(filepath.Join(d.tmp, "cli-home", ".sparkwing", "cache", "pipelines"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if out, err := d.sw(t.Context(), "", "pipeline", "trigger", "deploy-smoke"); err != nil || out != "run-accepted" {
					t.Fatalf("cache availability blocked submission: %q %v", out, err)
				}
			}
		})
	}
}
