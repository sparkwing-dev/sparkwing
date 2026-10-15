//go:build unix

package nodehost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func outputController(t *testing.T, st *store.Store) *Claim {
	t.Helper()
	mux := http.NewServeMux()
	output := func(r *http.Request) []byte {
		out, err := st.GetNodeOutput(r.Context(), r.PathValue("run"), r.PathValue("node"))
		if err != nil {
			t.Errorf("read output of %s: %v", r.PathValue("node"), err)
		}
		return out
	}
	mux.HandleFunc("GET /api/v1/runs/{run}/nodes/{node}", func(w http.ResponseWriter, r *http.Request) {
		row, err := st.GetNode(r.Context(), r.PathValue("run"), r.PathValue("node"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": row.Status, "outcome": row.Outcome})
	})
	mux.HandleFunc("GET /api/v1/runs/{run}/nodes/{node}/output", func(w http.ResponseWriter, r *http.Request) {
		out := output(r)
		sum := sha256.Sum256(out)
		_ = json.NewEncoder(w).Encode(store.OutputReadGrant{
			URL: "/objects/" + r.PathValue("run") + "/" + r.PathValue("node"), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(out)),
		})
	})
	mux.HandleFunc("GET /objects/{run}/{node}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(output(r)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	claim, err := NewClaim(srv.URL, store.ClaimTokenPrefix+"_fanout")
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
