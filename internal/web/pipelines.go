package web

import (
	"net/http"
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type pipelinesPayload struct {
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

// pipelinesHandler answers an account session with its team's list from the
// controller, since this dashboard's working directory is the operator's
// checkout and says nothing about that team. Every other caller gets the
// pipelines declared in the working directory.
func pipelinesHandler(teamList http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := WebPrincipalFromContext(r.Context()); ok && p.accountBound && teamList != nil {
			teamList.ServeHTTP(w, r)
			return
		}
		payload := pipelinesPayload{Pipelines: map[string]pipelineEntry{}}
		cwd, err := os.Getwd()
		if err != nil {
			writeJSON(w, http.StatusOK, payload)
			return
		}
		_, cfg, err := projectconfig.DiscoverPipelines(cwd)
		if err != nil {
			writeJSON(w, http.StatusOK, payload)
			return
		}
		for _, p := range cfg.Pipelines {
			payload.Pipelines[p.Name] = pipelineEntry{
				Args: []pipelineArg{},
			}
		}
		writeJSON(w, http.StatusOK, payload)
	}
}
