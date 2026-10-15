package controller

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCallerStorageStandingUsesFinalTenantInventory(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.DB().ExecContext(t.Context(), "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(t.Context(), "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1),('b',?,999,1)", strings.Repeat("a", 64), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(t.Context(), "INSERT INTO team_storage(team,store,used_bytes,reserved_bytes,updated_at) VALUES ('a','cache',999999,999999,1)"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	pub, priv := licensetest.NewKey(t)
	lic := license.Resolve(licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{license.FeatureMultiTeam}, IssuedTo: "test",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}), pub, now, nil)
	server := New(s, nil).WithLicense(lic)
	request := httptest.NewRequest("GET", "/", nil)
	request = request.WithContext(contextWithPrincipal(request.Context(), &Principal{Team: "a"}))
	got, err := server.teamStorageStanding(request, "a")
	if err != nil || got == nil || got.Team != "a" || got.CacheBytes != 3 || got.LogBytes != 0 || got.EventBytes != 0 {
		t.Fatalf("controller standing=%+v: %v", got, err)
	}
	var slots int
	if err := s.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM free_slots").Scan(&slots); err != nil || slots != 0 {
		t.Fatalf("report took slots: %d: %v", slots, err)
	}
}
