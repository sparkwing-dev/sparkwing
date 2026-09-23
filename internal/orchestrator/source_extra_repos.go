package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// PipelineExtraRepos returns the reader a direct fetch declares pipeline's
// source.extra_repos with: it reads them from the fetched checkout's
// .sparkwing/sparkwing.yaml. A checkout with no config, or a config that
// does not name the pipeline, declares none.
func PipelineExtraRepos(pipeline string) func(checkout string) ([]string, error) {
	return func(checkout string) ([]string, error) {
		path := filepath.Join(checkout, ".sparkwing", projectconfig.Filename)
		// safety: the file is the fetched tree's, so one that is a symlink
		// could point the read anywhere on the runner.
		fi, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", path)
		}
		cfg, err := projectconfig.Load(path)
		if err != nil {
			return nil, err
		}
		if cfg == nil {
			return []string{}, nil
		}
		for _, p := range cfg.Pipelines {
			if p.Name == pipeline {
				return append([]string{}, p.Source.ExtraRepos...), nil
			}
		}
		return []string{}, nil
	}
}
