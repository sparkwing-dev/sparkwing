package client

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestProbeCompletesWithoutQueueWork(t *testing.T) {
	home := shortHome(t)
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != controllerPath+"probe" {
			t.Error("unexpected queue work")
			http.NotFound(w, r)
			return
		}
		writeHTTPFixture(w, wingwire.HelloAck{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test"})
	}))
	socket, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Probe(t.Context(), socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.ProtocolMajor != wingd.ProtocolMajor || info.BinaryVersion != "test" {
		t.Fatalf("probe lost status: %+v", info)
	}
}
