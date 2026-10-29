//go:build !windows

package source_test

import (
	"errors"
	"os"
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

func TestTenantSelectionRefusesOversizedConfigurationBehindSymlink(t *testing.T) {
	repo, _ := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "config", "uploadpack.allowFilter", "true")
	if err := os.Rename(filepath.Join(repo, ".sparkwing"), filepath.Join(repo, "ci")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ci", filepath.Join(repo, ".sparkwing")); err != nil {
		t.Fatal(err)
	}
	sourcetest.Write(t, filepath.Join(repo, "ci", projectconfig.Filename), "pipelines: []\n#"+strings.Repeat("x", int(limits.ProjectConfigBytes))+"\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "oversized symlinked configuration")
	execution := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	remote := "https://8.8.8.8/team/symlinked-config"
	sourcetest.Remote(t, map[string]string{remote: "file://" + repo})
	options := source.Options{Workspace: t.TempDir(), Access: &access{}, Base: procenv.Base{Ambient: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}
	_, _, cfg, err := source.Select(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/symlinked-config", RemoteURL: remote}, "build", runspec.Git{SHA: execution}, runspec.Source{}, "", options)
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.InvalidArgs || cfg != nil {
		t.Fatalf("oversized symlinked tenant configuration = %v, %v", cfg, err)
	}
}
