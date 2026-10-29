package source_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestSelectAndAcquisitionFollowContainedConfigurationSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native symlinks require Windows symlink privileges")
	}
	repo, codeSHA := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "branch", "main")
	sourcetest.Git(t, repo, "update-ref", "refs/remotes/origin/main", codeSHA)
	if err := os.Rename(filepath.Join(repo, ".sparkwing"), filepath.Join(repo, "ci")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ci", filepath.Join(repo, ".sparkwing")); err != nil {
		t.Fatal(err)
	}
	sourcetest.Write(t, filepath.Join(repo, "ci", projectconfig.Filename), "pipelines:\n  - name: build\n    entrypoint: Build\n    source: origin/main\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "contained configuration")
	execution := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	for _, local := range []bool{true, false} {
		t.Run(map[bool]string{true: "local", false: "remote"}[local], func(t *testing.T) {
			spec := source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote}
			if local {
				spec.LocalCheckout, spec.RemoteURL = repo, ""
			}
			options := source.Options{Workspace: t.TempDir(), Access: &access{}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}
			git, code, cfg, err := source.Select(t.Context(), spec, "build", runspec.Git{SHA: execution}, runspec.Source{}, "", options)
			if err != nil {
				t.Fatal(err)
			}
			if git.SHA != execution || code.Ref != codeSHA || code.RequestedRef != "origin/main" || cfg == nil {
				t.Fatalf("lost symlinked source pin: git=%s code=%s pin=%s config=%v", git.SHA, code.Ref, code.RequestedRef, cfg)
			}
			spec.SHA, spec.CommittedCheckout = execution, true
			acquired, err := source.PrepareCheckout(t.Context(), spec, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := acquired.Close(); err != nil {
					t.Error(err)
				}
			}()
			loaded, err := projectconfig.Load(filepath.Join(acquired.Directory, projectconfig.Filename))
			if err != nil || loaded == nil || loaded.Registry().Find("build").Source != code.RequestedRef {
				t.Fatalf("selection/acquisition disagree: %v", err)
			}
		})
	}
}

func FuzzCommittedConfigurationStaysInsideAcquiredCheckout(f *testing.F) {
	f.Add("ci")
	f.Add("../outside")
	f.Add("/outside")
	f.Add(".sparkwing")
	f.Add("ci/../../outside")
	f.Fuzz(func(t *testing.T, target string) {
		if runtime.GOOS == "windows" {
			t.Skip("native symlinks require Windows symlink privileges")
		}
		if target == "" || len(target) > 256 || strings.ContainsRune(target, 0) {
			t.Skip()
		}
		repo, _ := sourcetest.Repository(t)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		sourcetest.Write(t, filepath.Join(outside, projectconfig.Filename), "pipelines:\n  - name: outside\n    entrypoint: Outside\n")
		escape := target == "../outside" || target == "/outside"
		if target == "../outside" {
			var err error
			target, err = filepath.Rel(repo, outside)
			if err != nil {
				t.Fatal(err)
			}
		} else if target == "/outside" {
			target = outside
		}
		if err := os.Rename(filepath.Join(repo, ".sparkwing"), filepath.Join(repo, "ci")); err != nil {
			t.Fatal(err)
		}
		sourcetest.Write(t, filepath.Join(repo, "ci", projectconfig.Filename), "pipelines:\n  - name: build\n    entrypoint: Build\n")
		if err := os.Symlink(target, filepath.Join(repo, ".sparkwing")); err != nil {
			t.Skip()
		}
		sourcetest.Git(t, repo, "add", ".")
		sourcetest.Git(t, repo, "commit", "-qm", "symlink")
		_, _, cfg, err := source.Select(t.Context(), source.Spec{RepoKey: "local:fixture", LocalCheckout: repo}, "build", runspec.Git{}, runspec.Source{}, "", source.Options{Workspace: t.TempDir(), Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}})
		if cfg != nil && cfg.Registry().Find("outside") != nil {
			t.Fatal("configuration escaped its acquired checkout")
		}
		if target == "ci" && (err != nil || cfg == nil || cfg.Registry().Find("build") == nil) {
			t.Fatalf("contained configuration lost: %v", err)
		}
		if escape && err == nil {
			t.Fatal("outside configuration symlink was silently ignored")
		}
	})
}

func TestSelectUsesHostAuthorityForExecutionAndConfiguredSourceRevisions(t *testing.T) {
	const execution = "0123456789abcdef0123456789abcdef01234567"
	const pinned = "abcdef0123456789abcdef0123456789abcdef01"
	for _, pin := range []string{"", "origin/code", pinned} {
		t.Run(pin, func(t *testing.T) {
			var lookedUp []string
			options := source.Options{
				ResolveRef: func(_ context.Context, ref string) (string, error) {
					lookedUp = append(lookedUp, ref)
					switch ref {
					case "refs/heads/main":
						return execution, nil
					case "refs/heads/code":
						return pinned, nil
					case "refs/tags/main":
						return "", nil
					default:
						t.Fatalf("unexpected revision lookup %q", ref)
						return "", nil
					}
				},
				ReadFile: func(_ context.Context, sha, name string) ([]byte, bool, error) {
					if sha != execution || name != ".sparkwing/sparkwing.yaml" {
						t.Fatalf("read %s:%s, want execution configuration", sha, name)
					}
					config := "pipelines:\n  - name: build\n    entrypoint: Build\n"
					if pin != "" {
						config += "    source: " + pin + "\n"
					}
					return []byte(config), true, nil
				},
			}
			git, code, _, err := source.Select(t.Context(), source.Spec{RepoKey: "gh:701", RemoteURL: "https://github.invalid/acme/repo.git"}, "build", runspec.Git{Ref: "main"}, runspec.Source{}, "", options)
			if err != nil {
				t.Fatal(err)
			}
			wantSHA, wantLookups := execution, []string{"refs/heads/main", "refs/tags/main"}
			if pin != "" {
				wantSHA = pinned
			}
			if pin == "origin/code" {
				wantLookups = append(wantLookups, "refs/heads/code")
			}
			if git.SHA != execution || code.Ref != wantSHA || code.RequestedRef != pin || !slices.Equal(lookedUp, wantLookups) {
				t.Fatalf("selection git=%s source=%+v lookups=%v, want source=%s lookups=%v", git.SHA, code, lookedUp, wantSHA, wantLookups)
			}
		})
	}
}
