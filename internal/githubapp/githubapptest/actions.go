package githubapptest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (g *GitHub) actionsAllowed(w http.ResponseWriter, r *http.Request) bool {
	token := g.installationToken(r)
	readable := r.Method == http.MethodGet && token != nil && token.permissions["actions"] == "read"
	if token == nil || !token.repos[strings.ToLower(r.PathValue("owner")+"/"+r.PathValue("repo"))] || token.permissions["actions"] != "write" && !readable {
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	return true
}

func (g *GitHub) handleActionsDispatch(w http.ResponseWriter, r *http.Request) {
	if !g.actionsAllowed(w, r) {
		return
	}
	var input struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	g.actionsCalls = append(g.actionsCalls, map[string]string{"receipt": input.Inputs["receipt"], "runner": input.Inputs["runner"], "ref": input.Ref, "workflow": r.PathValue("workflow")})
	g.actionsReceipt = input.Inputs["receipt"]
	g.mu.Unlock()
	if g.DispatchStarted != nil {
		g.DispatchStarted <- input.Inputs["receipt"]
	}
	if g.DispatchContinue != nil {
		<-g.DispatchContinue
	}
	if g.LostDispatch {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			if err := conn.Close(); err != nil {
				g.t.Error(err)
			}
		}
		return
	}
	status := g.ActionsStatus
	if status == 0 {
		status = http.StatusNoContent
	}
	w.WriteHeader(status)
}

// ActionsCalls returns outbound workflow dispatches observed at the HTTP boundary.
func (g *GitHub) ActionsCalls() []map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]map[string]string, len(g.actionsCalls))
	copy(out, g.actionsCalls)
	return out
}

// CancelledRuns returns provider runs cancelled through the App API.
func (g *GitHub) CancelledRuns() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.cancelledRuns...)
}

func (g *GitHub) handleActionsRun(w http.ResponseWriter, r *http.Request) {
	if !g.actionsAllowed(w, r) {
		return
	}
	g.mu.Lock()
	run := g.ActionsRun
	run.Title = "sparkwing:" + g.actionsReceipt
	g.mu.Unlock()
	writeJSON(w, http.StatusOK, run)
}

func (g *GitHub) handleActionsJobs(w http.ResponseWriter, r *http.Request) {
	if !g.actionsAllowed(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": []map[string]any{{"check_run_url": "https://api.github.com/repos/" + r.PathValue("owner") + "/" + r.PathValue("repo") + "/check-runs/" + strconv.FormatInt(9000, 10), "labels": g.ActionsLabels}}})
}

func (g *GitHub) handleActionsCancel(w http.ResponseWriter, r *http.Request) {
	if !g.actionsAllowed(w, r) {
		return
	}
	g.mu.Lock()
	status := g.CancelStatus[r.PathValue("run")]
	if status == 0 {
		status = http.StatusAccepted
		g.cancelledRuns = append(g.cancelledRuns, r.PathValue("run"))
	}
	g.mu.Unlock()
	w.WriteHeader(status)
}
