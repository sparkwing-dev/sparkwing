package orchestrator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A controller and a logs service are two binaries on two ports
// (sparkwing-controller :4344, sparkwing-logs :4345), and only the
// second routes /api/v1/logs. RemoteBackends defaulted log appends to
// the controller's own base URL, so a profile that named a controller
// and no logs surface posted every line into a 404.
//
// This pins the discriminator the fix relies on: a bare controller
// handler does not accept a log append.
func TestControllerHandlerServesNoLogAppends(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/controller.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/logs/run-x/node-x", "application/json", strings.NewReader("{}\n"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("controller answered %d for a log append; if it serves logs now, the "+
			"logs-URL resolution this test guards is no longer needed", resp.StatusCode)
	}
}

// The laptop dashboard mounts the controller and the logs service on
// one mux, so there the controller's base URL is the right place to
// send appends. The fix must not break that case, which is why the
// base URL stays the last resort rather than being removed.
func TestColocatedControllerAcceptsLogAppends(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/controller.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	logsSrv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/logs/", logsSrv.Handler())
	mux.Handle("/", controller.New(st, nil).Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/logs/run-x/node-x", "application/json", strings.NewReader("{}\n"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("a co-located deployment must accept log appends, got 404")
	}
}

// When the controller announces a logs URL, appends go there rather
// than to the controller.
func TestRemoteBackends_PrefersAnnouncedLogsURL(t *testing.T) {
	var logsHits, ctrlLogHits atomic.Int64

	logsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logsHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer logsSrv.Close()

	ctrlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/logs/") {
			ctrlLogHits.Add(1)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/v1/services" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"logs":"` + logsSrv.URL + `"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ctrlSrv.Close()

	c := client.NewWithToken(ctrlSrv.URL, nil, "")
	backends := orchestrator.RemoteBackends(c, nil, nil, nil, 0)

	nlog, err := backends.Logs.OpenNodeLog("run-x", "node-x", nil)
	if err != nil {
		t.Fatalf("OpenNodeLog: %v", err)
	}
	nlog.Log("info", "hello")
	_ = nlog.Close()

	if logsHits.Load() == 0 {
		t.Error("the announced logs service received nothing")
	}
	if ctrlLogHits.Load() != 0 {
		t.Errorf("the controller received %d log append(s); it serves none", ctrlLogHits.Load())
	}
}
