package source_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestSelectRefusesConfigurationAboveTheSizeLimit(t *testing.T) {
	head := "pipelines:\n  - name: build\n    entrypoint: Build\n#"
	repo, _ := sourcetest.Repository(t)
	remote := "https://8.8.8.8/team/sized"
	sourcetest.Remote(t, map[string]string{remote: repo})
	for _, size := range []int64{limits.ProjectConfigBytes, limits.ProjectConfigBytes + 1} {
		sourcetest.Write(t, filepath.Join(repo, ".sparkwing", projectconfig.Filename), head+strings.Repeat("x", int(size)-len(head)-1)+"\n")
		sourcetest.Git(t, repo, "add", ".")
		sourcetest.Git(t, repo, "commit", "-qm", "sized configuration")
		execution := sourcetest.Git(t, repo, "rev-parse", "HEAD")
		for _, local := range []bool{true, false} {
			spec := source.Spec{RepoKey: "git:8.8.8.8/team/sized", RemoteURL: remote}
			if local {
				spec.LocalCheckout, spec.RemoteURL = repo, ""
			}
			options := source.Options{Workspace: t.TempDir(), Access: &access{}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}}
			_, _, cfg, err := source.Select(t.Context(), spec, "build", runspec.Git{SHA: execution}, runspec.Source{}, "", options)
			var refused *apierr.Error
			if size == limits.ProjectConfigBytes {
				if err != nil || cfg == nil || cfg.Registry().Find("build") == nil {
					t.Fatalf("local=%v: configuration at the limit = %v, %v", local, cfg, err)
				}
				continue
			}
			if !errors.As(err, &refused) || refused.Code != apierr.InvalidArgs || !strings.Contains(err.Error(), "1 MiB") || cfg != nil {
				t.Fatalf("local=%v: oversized configuration = %v, %v", local, cfg, err)
			}
		}
	}
}

func TestParseRefusesConfigurationAboveTheSizeLimit(t *testing.T) {
	raw := "pipelines: []\n#" + strings.Repeat("x", int(limits.ProjectConfigBytes)) + "\n"
	if _, err := projectconfig.Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized parse = %v", err)
	}
}

func TestTenantSelectionNeverDownloadsAnOversizedConfiguration(t *testing.T) {
	repo, _ := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "config", "uploadpack.allowFilter", "true")
	big := make([]byte, 20<<20)
	for i := range big {
		big[i] = byte('a' + i*7919%26)
	}
	copy(big, "pipelines: []\n#")
	sourcetest.Write(t, filepath.Join(repo, ".sparkwing", projectconfig.Filename), string(big)+"\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "oversized configuration")
	execution := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	remote := "https://8.8.8.8/team/oversized"
	sourcetest.Remote(t, map[string]string{remote: "file://" + repo})
	shim, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, sizes := t.TempDir(), filepath.Join(t.TempDir(), "sizes")
	wrapper := "#!/bin/sh\n" + shellWord(shim) + " \"$@\"\nrc=$?\nfor a in \"$@\"; do case \"$a\" in */objects.git) du -sk \"$a\" >> " + shellWord(sizes) + ";; esac; done\nexit $rc\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	spec := source.Spec{RepoKey: "git:8.8.8.8/team/oversized", RemoteURL: remote}
	options := source.Options{Workspace: t.TempDir(), Access: &access{}, Base: procenv.Base{Ambient: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}
	_, _, cfg, err := source.Select(t.Context(), spec, "build", runspec.Git{SHA: execution}, runspec.Source{}, "", options)
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.InvalidArgs || !strings.Contains(err.Error(), "1 MiB") || cfg != nil {
		t.Fatalf("oversized tenant configuration = %v, %v", cfg, err)
	}
	raw, err := os.ReadFile(sizes)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		kb, err := strconv.Atoi(strings.Fields(line)[0])
		if err != nil || int64(kb)<<10 > limits.ProjectConfigBytes {
			t.Fatalf("selection object store grew to %s", line)
		}
	}
}

func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestTenantSelectionReportsAGitFailureAsItself(t *testing.T) {
	repo, _ := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "config", "uploadpack.allowFilter", "true")
	sourcetest.Write(t, filepath.Join(repo, ".sparkwing", projectconfig.Filename), "pipelines: []\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "configuration")
	execution := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	remote := "https://8.8.8.8/team/broken"
	sourcetest.Remote(t, map[string]string{remote: "file://" + repo})
	shim, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = cat-file ] && { echo 'fatal: bad object store' >&2; exit 128; }; done\nexec " + shellWord(shim) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := source.Options{Workspace: t.TempDir(), Access: &access{}, Base: procenv.Base{Ambient: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}
	_, _, _, err = source.Select(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/broken", RemoteURL: remote}, "build", runspec.Git{SHA: execution}, runspec.Source{}, "", options)
	if err == nil || strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("git failure reported as = %v", err)
	}
}
