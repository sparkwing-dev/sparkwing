package acceptance_test

import (
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestCapabilityManagementHTTPUsesStoreTransactions(t *testing.T) {
	f := newSessionFlow(t)
	var _ controller.CapabilityManager = f.store.AsOperator()
	tenant, err := f.store.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	var _ controller.CapabilityManager = tenant
	h := f.server.Handler()
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request(method, path, body))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return w
	}
	issued := call("POST", "/api/v1/tokens", `{"grants":["runs.read","runs.write"]}`, 201)
	var root struct {
		Capability string `json:"capability"`
		Prefix     string `json:"prefix"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &root); err != nil || root.Capability == "" {
		t.Fatalf("issue: %s %v", issued.Body, err)
	}
	list := call("GET", "/api/v1/tokens?team=foreign", "", 200)
	if !strings.Contains(list.Body.String(), root.Prefix) || strings.Contains(list.Body.String(), root.Capability) {
		t.Fatalf("inventory: %s", list.Body)
	}
	call("GET", "/api/v1/tokens/"+root.Prefix, "", 200)
	rotated := call("POST", "/api/v1/tokens/"+root.Prefix+"/rotate", `{"grace_secs":0}`, 200)
	var replacement struct {
		Capability string              `json:"capability"`
		New        capability.Metadata `json:"new"`
		Old        string              `json:"old_prefix"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &replacement); err != nil || replacement.Capability == "" || replacement.Old != root.Prefix || replacement.New.CreatedBy != "member" {
		t.Fatalf("rotate: %s %v", rotated.Body, err)
	}
	if _, err := f.store.LookupCapability(t.Context(), sha256.Sum256([]byte(root.Capability))); err == nil {
		t.Fatal("old credential survived zero grace")
	}
	if _, err := f.store.LookupCapability(t.Context(), sha256.Sum256([]byte(replacement.Capability))); err != nil {
		t.Fatalf("replacement not committed: %v", err)
	}
	call("POST", "/api/v1/tokens/"+root.Prefix+"/rotate", `{}`, 409)
	call("DELETE", "/api/v1/tokens/"+replacement.New.Prefix, "", 204)
	if _, err := f.store.LookupCapability(t.Context(), sha256.Sum256([]byte(replacement.Capability))); err == nil {
		t.Fatal("owned credential survived HTTP revocation")
	}
}
