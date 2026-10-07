package jobs

import (
	"context"
	"fmt"
	"go/version"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"golang.org/x/mod/modfile"

	templates "github.com/sparkwing-dev/sparks-core/templates"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// CheckGoDirectives rejects patch-level Go requirements in published modules and shipped templates.
func CheckGoDirectives(repoRoot string) error {
	name := filepath.Join(repoRoot, "go.mod")
	data, err := os.ReadFile(name)
	if err != nil {
		return fmt.Errorf("go directive policy: %w", err)
	}
	if err := checkGoDirective("go.mod", data); err != nil {
		return err
	}
	return checkTemplateGoDirectives(templates.FS)
}

func checkTemplateGoDirectives(registry fs.FS) error {
	return fs.WalkDir(registry, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Base(name) != "go.mod" {
			return nil
		}
		data, err := fs.ReadFile(registry, name)
		if err != nil {
			return err
		}
		return checkGoDirective("template "+name, data)
	})
}

func checkGoDirective(name string, data []byte) error {
	mod, err := modfile.Parse(name, data, nil)
	if err != nil {
		return fmt.Errorf("go directive policy: %w", err)
	}
	if mod.Go == nil {
		return fmt.Errorf("go directive policy: %s has no go directive", name)
	}
	v := "go" + mod.Go.Version
	floor := version.Lang(v)
	if version.Compare(v, floor+".0") > 0 {
		return fmt.Errorf("go directive policy: %s requires go %s; published modules and generated go.mod files must use %s or %s.0 so users can choose their Go patch release", name, mod.Go.Version, floor[2:], floor[2:])
	}
	return nil
}

func checkGoDirectives(ctx context.Context) error {
	if err := CheckGoDirectives(sparkwing.WorkDir()); err != nil {
		return err
	}
	return withProductTestHome(func(home string) error {
		command := "GOWORK=off go test ./cmd/sparkwing -run '^TestPipelineScaffoldDoesNotRequireGoPatch$' -count=1"
		_, err := sparkwing.Bash(ctx, productTestScript(command, home)).Run()
		if err != nil {
			return fmt.Errorf("go directive policy: check generated init and template modules: %w", err)
		}
		return nil
	})
}
