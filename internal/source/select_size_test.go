package source_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
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
