package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestControllerDownloadChargesUseTenantAndReplica(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := s.AsOperator().CreateTeam(t.Context(), "team-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().CreateTeam(t.Context(), "team-b"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	pub, priv := licensetest.NewKey(t)
	lic := license.Resolve(licensetest.Sign(t, priv, licensetest.Terms{Features: []string{license.FeatureMultiTeam}, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}), pub, now, nil)
	servers := []*Server{New(s, nil).WithLicense(lic).WithTeamDownloadCaps(10, 100), New(s, nil).WithLicense(lic).WithTeamDownloadCaps(10, 100)}
	if servers[0].leaseHolder == servers[1].leaseHolder {
		t.Fatal("controllers share replica identity")
	}
	body := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "12345"); err != nil {
			t.Logf("response refused before delivery: %v", err)
		}
	})
	read := func(server *Server, team store.Team, class egress.Class) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(contextWithPrincipal(r.Context(), &Principal{Team: team, Name: "reader"}))
		response := httptest.NewRecorder()
		server.meterOn(class, egress.SlotDownload, body).ServeHTTP(response, r)
		return response
	}
	for _, server := range servers {
		if got := read(server, "team-a", egress.ClassArtifact); got.Code != http.StatusOK {
			t.Fatalf("download=%d: %s", got.Code, got.Body)
		}
	}
	if got := read(servers[1], "team-a", egress.ClassArtifact); got.Code != http.StatusTooManyRequests || got.Header().Get("Retry-After") == "" {
		t.Fatalf("spent download=%d: %s", got.Code, got.Body)
	}
	if got := read(servers[1], "team-b", egress.ClassArtifact); got.Code != http.StatusOK {
		t.Fatalf("foreign team inherited cap=%d: %s", got.Code, got.Body)
	}
	for _, class := range []egress.Class{egress.ClassLog, egress.ClassLogStream} {
		if got := read(servers[1], "team-a", class); got.Code != http.StatusOK || got.Body.String() != "12345" {
			t.Fatalf("log read=%d: %s", got.Code, got.Body)
		}
	}
	for i, server := range servers {
		var bytes int64
		if err := s.DB().QueryRowContext(t.Context(), "SELECT SUM(bytes) FROM team_download_day WHERE team='team-a' AND replica_id=?", server.leaseHolder).Scan(&bytes); err != nil || bytes != 5 {
			t.Fatalf("controller %d counted %d bytes: %v", i, bytes, err)
		}
	}
}
