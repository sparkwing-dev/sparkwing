package localws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestVersionHandler_ReportsVersionAndSchema(t *testing.T) {
	srv := httptest.NewServer(versionHandler("v0.16.0"))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.Version != "v0.16.0" {
		t.Errorf("version = %q, want v0.16.0", info.Version)
	}
	if info.Schema != store.ExpectedSchemaVersion() {
		t.Errorf("schema = %d, want %d", info.Schema, store.ExpectedSchemaVersion())
	}
	if info.PID == 0 {
		t.Error("pid = 0, want the running process id")
	}
}
