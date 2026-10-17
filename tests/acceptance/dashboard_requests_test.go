package acceptance_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDashboardRequestsReachControllerHandlers(t *testing.T) {
	root, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}
	script := `
 globalThis.window = {};
 const requests = [];
 globalThis.fetch = async (url, init = {}) => {
   requests.push({url, method: init.method || "GET", headers: Object.fromEntries(new Headers(init.headers)), body: init.body || ""});
   return new Response(JSON.stringify({run_id: "accepted", points: []}), {status: 200});
 };
 const api = await import("./src/lib/api.ts");
 await api.triggerRun("ci", {message: "hello"}, {repo_url: "https://git.example/acme/app", branch: "main", repo: "app"});
 await api.cancelRun("run");
 await api.retryRun("run", {full: true});
 await api.getNodeMetrics("run", "job");
 await api.getPaused("run");
 console.log(JSON.stringify(requests));`
	cmd := exec.Command("node", "--import", "tsx", "--input-type=module", "-e", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TSX_DISABLE_CACHE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard caller: %v: %s", err, output)
	}
	var requests []struct {
		URL, Method, Body string
		Headers           map[string]string
	}
	if err := json.Unmarshal(output, &requests); err != nil {
		t.Fatalf("requests: %v: %s", err, output)
	}
	if len(requests) != 5 {
		t.Fatalf("requests=%d", len(requests))
	}
	for i, request := range requests {
		t.Run(request.Method+" "+request.URL, func(t *testing.T) {
			b := intakeBoundary()
			b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsWrite, capability.RunsRead, capability.RunsControl}
			tx := &engineTransactions{admitted: store.AdmissionResult{RunID: "accepted"}}
			server := controller.New(nil, nil).WithAuthorization(consoleAuthorization(b)).WithEngineController(fakeEngine(t, tx))
			handler := server.Handler()
			r := httptest.NewRequest(request.Method, request.URL, strings.NewReader(request.Body))
			for name, value := range request.Headers {
				r.Header.Set(name, value)
			}
			r.Header.Set("Authorization", "Bearer "+b.raw)
			if !server.RegisteredRoute(r) {
				t.Fatalf("dashboard called an unregistered route: %s", request.URL)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if i < 3 {
				if w.Code != 200 && w.Code != 201 {
					t.Fatalf("HTTP %d %s", w.Code, w.Body)
				}
				if tx.writes != 1 {
					t.Fatalf("commits=%d", tx.writes)
				}
			}
			if i == 0 && (tx.intake.Request.Pipeline != "ci" || tx.intake.Request.RequestedArgs["message"] != "hello") {
				t.Fatalf("intake=%+v", tx.intake)
			}
			if i == 1 && tx.runChange.Reason != "user_cancelled" {
				t.Fatalf("cancellation=%+v", tx.runChange)
			}
			if i == 2 && tx.intake.IdempotencyKey == "" {
				t.Fatal("retry lost its replay key")
			}
		})
	}
}
