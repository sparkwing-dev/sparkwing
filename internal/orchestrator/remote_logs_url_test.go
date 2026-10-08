package orchestrator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestControllerHandlerServesNoLogAppends(t *testing.T) {
	st, err := teststore.Open(t.TempDir() + "/controller.db")
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

func TestColocatedControllerAcceptsLogAppends(t *testing.T) {
	st, err := teststore.Open(t.TempDir() + "/controller.db")
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
