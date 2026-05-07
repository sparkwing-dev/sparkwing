package web

import (
	"net/http"
	"os"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/pipelines"
)

// pipelinesPayload mirrors web/src/lib/api.ts:PipelineMeta.
type pipelinesPayload struct {
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

// pipelinesHandler serves pipelines discovered from the nearest
// .sparkwing/pipelines.yaml. Args schemas are empty until argument
// introspection is plumbed in from the compiled pipeline binary.
func pipelinesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload := pipelinesPayload{Pipelines: map[string]pipelineEntry{}}
		cwd, err := os.Getwd()
		if err != nil {
			writeJSON(w, http.StatusOK, payload)
			return
		}
		_, cfg, err := pipelines.Discover(cwd)
		if err != nil {
			// No .sparkwing nearby (e.g. prod dashboard pod): empty map
			// lets TriggerForm fall back to a free-text input.
			writeJSON(w, http.StatusOK, payload)
			return
		}
		for _, p := range cfg.Pipelines {
			payload.Pipelines[p.Name] = pipelineEntry{
				Args: []pipelineArg{},
				Tags: p.Tags,
			}
		}
		writeJSON(w, http.StatusOK, payload)
	}
}
