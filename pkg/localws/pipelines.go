package localws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type pipelinesResponse struct {
	Pipelines map[string]pipelineEntry `json:"pipelines"`
}

type pipelineEntry struct {
	Args []pipelineArg `json:"args"`
}

type pipelineArg struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Desc     string `json:"desc"`
	Default  string `json:"default,omitempty"`
}

func aggregatedPipelinesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		out := pipelinesResponse{Pipelines: map[string]pipelineEntry{}}
		path, err := repos.DefaultPath()
		if err != nil {
			http.Error(w, fmt.Sprintf("resolve repository registry: %v", err), http.StatusInternalServerError)
			return
		}
		cfg, err := repos.Load(path)
		if err != nil {
			http.Error(w, fmt.Sprintf("read repository registry: %v", err), http.StatusInternalServerError)
			return
		}
		for _, e := range cfg.Repos {
			ymlPath := filepath.Join(e.Path, ".sparkwing", projectconfig.Filename)
			loaded, lerr := projectconfig.Load(ymlPath)
			if lerr != nil || loaded == nil {
				continue
			}
			for _, p := range loaded.Pipelines {
				if _, dup := out.Pipelines[p.Name]; dup {
					continue
				}
				out.Pipelines[p.Name] = pipelineEntry{
					Args: []pipelineArg{},
				}
			}
		}
		writeJSON(w, out)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
