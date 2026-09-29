package controller_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type teamDownloadFixture struct {
	*identityFixture
	meter *egress.Meter
}

func newTeamDownloadFixture(t *testing.T, freeCap int64, objects map[string][]byte) teamDownloadFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	meter := egress.New(egress.Config{GlobalDailyAlarmBytes: 1})
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, configure: func(s *controller.Server) {
		s.WithArtifactStore(&fakeArtifactStore{objects: objects}).
			WithTeamDownloadCaps(freeCap, 0).
			WithEgressMeter(meter)
	}})
	return teamDownloadFixture{identityFixture: f, meter: meter}
}

func teamRun(t *testing.T, st *store.Store, team store.Team, runID string) {
	t.Helper()
	ctx := context.Background()
	tenant, err := st.ForTeam(ctx, team)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func (f teamDownloadFixture) download(t *testing.T, auth, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, body
}

// A team's artifact downloads are held to its own daily cap, keyed on the
// team rather than the token's name, so two teams whose tokens share a name
// never spend each other's day; a download already admitted finishes whole;
// and reading a run's logs is never refused past the cap. The process-wide
// alarm, far below what is served here, refuses nothing.
func TestTeamDownloadsAreCappedPerTeamAndLogReadsAreNot(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 100)
	f := newTeamDownloadFixture(t, 150, map[string][]byte{"runs/ra/k": payload, "runs/rb/k": payload})
	a := freeTeamToken(t, f.store, "team-a")
	b := freeTeamToken(t, f.store, "team-b")
	teamRun(t, f.store, "team-a", "ra")
	teamRun(t, f.store, "team-b", "rb")

	for i := range 2 {
		code, body := f.download(t, a, "/api/v1/artifacts/runs%2Fra%2Fk")
		if code != http.StatusOK || !bytes.Equal(body, payload) {
			t.Fatalf("team-a download %d = %d with %d bytes, want 200 and the whole artifact", i, code, len(body))
		}
	}
	if code, _ := f.download(t, a, "/api/v1/artifacts/runs%2Fra%2Fk"); code != http.StatusTooManyRequests {
		t.Fatalf("team-a past its cap = %d, want 429", code)
	}
	if code, body := f.download(t, b, "/api/v1/artifacts/runs%2Frb%2Fk"); code != http.StatusOK || !bytes.Equal(body, payload) {
		t.Fatalf("team-b, whose token has team-a's name = %d with %d bytes, want its own day", code, len(body))
	}
	if code, _ := f.download(t, a, "/api/v1/runs/ra/nodes/n1/logs"); code == http.StatusTooManyRequests {
		t.Fatal("team-a's log read past its download cap was refused")
	}
	if !f.meter.Alarm() {
		t.Fatal("the daily alarm did not rise past its threshold")
	}
}
