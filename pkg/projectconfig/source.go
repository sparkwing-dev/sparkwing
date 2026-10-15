package projectconfig

import (
	"path/filepath"
	"strings"
)

func PipelineSource(repoDir, name string) (string, error) {
	cfg, err := Load(filepath.Join(repoDir, ".sparkwing", Filename))
	if err != nil || cfg == nil {
		return "", err
	}
	for _, pipeline := range cfg.Pipelines {
		if pipeline.Name == name {
			if pipeline.Source != nil {
				return strings.TrimSpace(*pipeline.Source), nil
			}
			return "", nil
		}
	}
	return "", nil
}
