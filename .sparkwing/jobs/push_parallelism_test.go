package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestAffectedPushTestsIgnoreSubmitterGoLimits(t *testing.T) {
	previous := runtime.GOMAXPROCS(2)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	t.Setenv("GOMAXPROCS", "2")
	t.Setenv("GOFLAGS", "-trimpath -p=1")
	// safety: the local SDK sets CI without granting exclusive use of the host.
	t.Setenv("CI", "true")
	want := max(1, (runtime.NumCPU()-1)/2)
	for _, queued := range []bool{false, true} {
		name := "pre-push"
		if queued {
			name = "queued-land"
		}
		t.Run(name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			marker := filepath.Join(t.TempDir(), "executed")
			body := fmt.Sprintf(`package controller
import ("os"; "runtime"; "testing")
func check(t *testing.T) {
 t.Helper()
 if got := runtime.GOMAXPROCS(0); got != %d { t.Fatalf("test GOMAXPROCS=%%d, want %d", got) }
 if got := os.Getenv("GOFLAGS"); got != "-mod=readonly" { t.Fatalf("inherited GOFLAGS=%%q", got) }
 suffix := "-critical"; if testing.Short() { suffix = "-short" }
 if err := os.WriteFile(%q+suffix, []byte("body ran"), 0600); err != nil { t.Fatal(err) }
}
func TestShort(t *testing.T) { if !testing.Short() { t.Fatal("missing short class") }; check(t) }
%s
func TestCritical(t *testing.T) { if testing.Short() { t.Skip("critical class") }; check(t) }
`, want, want, marker, criticalTestMarker)
			writeGoFile(t, filepath.Join(root, "internal", "controller", "controller_test.go"), body)
			gitCommitAll(t, root, "base")
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			writeGoFile(t, filepath.Join(root, "internal", "controller", "controller.go"), "package controller\nconst Changed = true\n")
			gitCommitAll(t, root, "controller change")
			prepare := preparePlan
			if queued {
				prepare = prepareQueuedFixturePlan
			}
			host, plan, doc := prepare(t, func(ctx context.Context, plan *sparkwing.Plan) error {
				if queued {
					return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Check Go limits"}}, sparkwing.RunContext{Pipeline: "land", RunID: queuedFixtureRunID})
				}
				return (&PrePush{}).Plan(ctx, plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
			})
			t.Run("reservation", func(t *testing.T) {
				for _, job := range doc.Jobs {
					if job.ID == "affected-tests" {
						if job.Cores != float64(want) {
							t.Fatalf("reserved %g cores, want %d regardless of submitter limits", job.Cores, want)
						}
						return
					}
				}
				t.Fatal("missing affected-tests job")
			})
			if _, err := host.RunStep(t.Context(), plan, "affected-tests", "test"); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"-short", "-critical"} {
				if _, err := os.Stat(marker + suffix); err != nil {
					t.Fatalf("%s body did not execute: %v", suffix, err)
				}
			}
		})
	}
}

func TestAffectedPushTestsSelectTheBuildTheyExecute(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "process-flags"
		if saved {
			name = "saved-flags"
		}
		t.Run(name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			marker := filepath.Join(root, "consumer-ran")
			savedSettings := ""
			if saved {
				goenv := filepath.Join(root, "saved-go-env")
				writeGoFile(t, goenv, "GOFLAGS=-tags=alternate -p=1\nGOPROXY=https://proxy.example.invalid\nGOPRIVATE=private.example/*\nGONOSUMDB=checksum.example/*\n")
				t.Setenv("GOENV", goenv)
				for _, key := range []string{"GOPROXY", "GOPRIVATE", "GONOSUMDB"} {
					t.Setenv(key, "")
				}
				savedSettings = "https://proxy.example.invalid\nprivate.example/*\nchecksum.example/*"
			}
			writeGoFile(t, filepath.Join(root, "leaf", "leaf.go"), "package leaf\nconst Value = 1\n")
			writeGoFile(t, filepath.Join(root, "consumer", "default.go"), "//go:build !alternate\n\npackage consumer\nimport \"fixture/leaf\"\nconst Value = leaf.Value\n")
			writeGoFile(t, filepath.Join(root, "consumer", "alternate.go"), "//go:build alternate\n\npackage consumer\nconst Value = 1\n")
			writeGoFile(t, filepath.Join(root, "consumer", "consumer_test.go"), fmt.Sprintf(`package consumer
import ("os"; "os/exec"; "strings"; "testing")
func TestConsumer(t *testing.T) {
 if got := os.Getenv("GOFLAGS"); got != "-mod=readonly" { t.Fatalf("inherited GOFLAGS=%%q", got) }
 if want := %q; want != "" {
  out, err := exec.Command("go", "env", "GOPROXY", "GOPRIVATE", "GONOSUMDB").CombinedOutput()
  if err != nil { t.Fatalf("saved settings: %%s: %%v", out, err) }
  if got := strings.TrimSpace(string(out)); got != want { t.Fatalf("saved settings=%%q, want %%q", got, want) }
 }
 if err := os.WriteFile(%q, nil, 0600); err != nil { t.Fatal(err) }
}
`, savedSettings, marker))
			gitCommitAll(t, root, "base")
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			writeGoFile(t, filepath.Join(root, "leaf", "leaf.go"), "package leaf\nconst Value = 2\n")
			gitCommitAll(t, root, "leaf change")
			t.Setenv("GOFLAGS", "-tags=alternate -p=1")
			host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
				return (&PrePush{}).Plan(ctx, plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
			})
			if _, err := host.RunStep(t.Context(), plan, "affected-tests", "test"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("default-build reverse dependency was not tested: %v", err)
			}
		})
	}
}
