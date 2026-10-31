package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestCallerStorageStandingUsesFinalTenantInventory(t *testing.T) {
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := teststore.SQL(t, s).ExecContext(t.Context(), "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, s).ExecContext(t.Context(), "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1),('b',?,999,1)", strings.Repeat("a", 64), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, s).ExecContext(t.Context(), "INSERT INTO team_storage(team,store,storage_charged_through,updated_at) VALUES ('a','cache',999999,1)"); err != nil {
		t.Fatal(err)
	}
	server := NewDeployment(s, deploymenttest.Team(t), nil)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	got, err := server.teamStorageStanding(request, "a")
	if err != nil || got == nil || got.Team != "a" || got.CacheBytes != 3 || got.LogBytes != 0 || got.EventBytes != 0 {
		t.Fatalf("controller standing=%+v: %v", got, err)
	}
}
