package controller_test

import (
	"context"
	"errors"
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

func newTeamDownloadFixture(t *testing.T, freeCap int64) teamDownloadFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	meter := egress.New(egress.Config{})
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, configure: func(s *controller.Server) {
		s.WithTeamDownloadCaps(freeCap, 0).WithEgressMeter(meter)
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

// Reading a run's logs is never refused once its team has spent its daily
// download cap, so a team can still read why its run failed.
func TestTeamLogReadsAreNotRefusedPastTheDownloadCap(t *testing.T) {
	f := newTeamDownloadFixture(t, 150)
	a := freeTeamToken(t, f.store, "team-a")
	teamRun(t, f.store, "team-a", "ra")
	if _, err := f.store.ChargeDownload(context.Background(), store.DownloadCharge{
		Team: "team-a", Bytes: 150, Record: true, Now: time.Now(), FreeCapBytes: 150,
	}); err != nil {
		t.Fatalf("spend team-a's day: %v", err)
	}
	if _, err := f.store.ChargeDownload(context.Background(), store.DownloadCharge{
		Team: "team-a", Now: time.Now(), FreeCapBytes: 150,
	}); !errors.Is(err, store.ErrDownloadCap) {
		t.Fatalf("team-a after spending its day = %v, want the cap", err)
	}
	if code, _ := f.download(t, a, "/api/v1/runs/ra/nodes/n1/logs"); code == http.StatusTooManyRequests {
		t.Fatal("team-a's log read past its download cap was refused")
	}
}
