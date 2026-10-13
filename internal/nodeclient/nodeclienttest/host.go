// Package nodeclienttest runs a real node route server with a fake
// controller behind it, for tests of the SDK's route clients.
package nodeclienttest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeclient"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ClaimToken is the controller credential the host forwards with; no reply
// or log the node can see may carry it.
const ClaimToken = "swc_claim-credential-never-shown"

// RunID is the run every admitted node belongs to.
const RunID = "run-1"

// Secret is one value the node's secret source holds.
type Secret struct {
	Value  string
	Masked bool
}

// Host is a node route server with one admitted node.
type Host struct {
	Server *nodehost.Server
	Logs   *Buffer
	Bearer string
	Masker *secrets.Masker
	Client *nodeclient.Client
	// Revoke ends the bearer, as the host does when the process exits.
	Revoke func()

	controller *httptest.Server
	mux        *http.ServeMux
}

// Start serves the routes for node of [RunID]. The node's pipeline declares
// every secret in values, plus the names in declared, which the source does
// not hold.
func Start(t testing.TB, node string, values map[string]Secret, declared ...string) *Host {
	t.Helper()
	logs := &Buffer{}
	s, err := nodehost.Start(slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	h := &Host{Server: s, Logs: logs, Masker: secrets.NewMasker(), mux: http.NewServeMux()}
	h.controller = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && r.Header.Get("Authorization") != "Bearer "+ClaimToken {
			t.Errorf("%s %s: forwarded without the claim token", r.Method, r.URL.Path)
		}
		h.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(h.controller.Close)
	claim, err := nodehost.NewClaim(h.controller.URL, ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	for name := range values {
		declared = append(declared, name)
	}
	h.Bearer, h.Revoke, err = s.Admit(nodehost.Node{
		RunID: RunID, NodeID: node, Secrets: declared, Resolver: resolver(values), Masker: h.Masker, Claim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Client, err = nodeclient.New(s.URL(), h.Bearer); err != nil {
		t.Fatal(err)
	}
	return h
}

// Handle serves pattern on the fake controller.
func (h *Host) Handle(pattern string, handler http.HandlerFunc) { h.mux.HandleFunc(pattern, handler) }

// Object serves data at a signed object URL and returns the grant to it.
func (h *Host) Object(key string, data []byte) store.OutputReadGrant {
	h.Handle("GET /objects/"+key, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
	sum := sha256.Sum256(data)
	return store.OutputReadGrant{URL: "/objects/" + key, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

// Answer replies status with body as JSON.
func Answer(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // a reply the host fails to read fails the test that asked
	}
}

type resolver map[string]Secret

func (r resolver) Resolve(_ context.Context, name string) (string, bool, error) {
	v, ok := r[name]
	if !ok {
		return "", false, secretsource.ErrMissing
	}
	return v.Value, v.Masked, nil
}

// Buffer collects the host's log lines across goroutines.
type Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
