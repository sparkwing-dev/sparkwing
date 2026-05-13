package localws

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/repos"
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
	Tags []string      `json:"tags,omitempty"`
}

type pipelineArg struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Desc     string `json:"desc"`
	Default  string `json:"default,omitempty"`
}

// aggregatedPipelinesHandler enumerates every `.sparkwing/pipelines.yaml`
// across the repos registered in `~/.config/sparkwing/repos.yaml` and
// merges them into one map keyed by pipeline name. The dashboard's
// TriggerForm uses the result to drive its pipeline picker.
//
// Conflict policy: first repo to register a given name wins. The
// repos.yaml order is preserved so users can promote a primary
// checkout above feature worktrees of the same project.
//
// The handler is best-effort: an unreadable repos.yaml, a missing
// `.sparkwing/`, or a malformed pipelines.yaml in any one repo
// degrades to "skip that repo" rather than 5xx-ing the whole probe --
// a broken side checkout shouldn't black-hole the picker.
func aggregatedPipelinesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		out := pipelinesResponse{Pipelines: map[string]pipelineEntry{}}
		path, err := repos.DefaultPath()
		if err != nil {
			writeJSON(w, out)
			return
		}
		cfg, err := repos.Load(path)
		if err != nil {
			writeJSON(w, out)
			return
		}
		for _, e := range cfg.Repos {
			ymlPath := filepath.Join(e.Path, ".sparkwing", "pipelines.yaml")
			loaded, lerr := pipelines.Load(ymlPath)
			if lerr != nil {
				continue
			}
			for _, p := range loaded.Pipelines {
				if p.Hidden {
					continue
				}
				if _, dup := out.Pipelines[p.Name]; dup {
					continue
				}
				out.Pipelines[p.Name] = pipelineEntry{
					Args: []pipelineArg{},
					Tags: p.Tags,
				}
			}
		}
		// TriggerForm sorts keys client-side, so the unordered map
		// shape matches internal/web.pipelinesHandler over the wire.
		writeJSON(w, out)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
