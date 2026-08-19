package localws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// pipelinesResponse mirrors the shape the dashboard's TriggerForm
// consumes (web/src/lib/api.ts:getPipelines). Empty Args is fine --
// the form falls through to a free-text input when a pipeline has no
// declared schema.
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

// aggregatedPipelinesHandler enumerates every `.sparkwing/sparkwing.yaml`
// across the repos registered in `~/.config/sparkwing/repos.yaml` and
// merges them into one map keyed by pipeline name. The dashboard's
// TriggerForm uses the result to drive its pipeline picker.
//
// Conflict policy: first repo to register a given name wins. The
// repos.yaml order is preserved so users can promote a primary
// checkout above feature worktrees of the same project.
//
// A missing or malformed sparkwing.yaml in one checkout is skipped so a
// broken side checkout does not black-hole the picker. The registry itself
// is authoritative: if it is unreadable, returning an empty fleet would hide
// the failure and misrepresent every registered checkout as absent.
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
