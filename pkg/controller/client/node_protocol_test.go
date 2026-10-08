package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

func TestNodeRequestsNameTheNodeProtocolVersion(t *testing.T) {
	got := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(authwire.NodeProtocolHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	for _, c := range []*Client{New(srv.URL, nil), NewWithToken(srv.URL, nil, "tok")} {
		if err := c.StartNode(context.Background(), "run-1", "node-a"); err != nil {
			t.Fatal(err)
		}
		if v := <-got; v != authwire.NodeProtocolVersion {
			t.Fatalf("%s header = %q, want %q", authwire.NodeProtocolHeader, v, authwire.NodeProtocolVersion)
		}
	}
}
