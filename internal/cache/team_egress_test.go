package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// newTeamEgressServer caps a free team at two 60-byte downloads a day and a
// funded team at twenty, with every team's tier answered by a controller.
func newTeamEgressServer(t *testing.T) (*httptest.Server, Config, string) {
	t.Helper()
	const token = "operator-token"
	controller := tierController(t, token, map[string]storagequota.Standing{
		"free":   {Tier: storagequota.TierFree, AllowanceBytes: 1 << 20},
		"other":  {Tier: storagequota.TierFree, AllowanceBytes: 1 << 20},
		"paying": {Tier: storagequota.TierFunded, AllowanceBytes: 1 << 20},
	})
	var cfg Config
	srv, _, _ := newBlobServerWith(t, token, func(c *Config, _ *s3.Client) {
		c.ControllerURL = controller
		c.TeamDailyDownloadFreeBytes = 120
		c.TeamDailyDownloadFundedBytes = 1200
		cfg = *c
	})
	return srv, cfg, token
}

func putBin(t *testing.T, srv *httptest.Server, bearer string) {
	t.Helper()
	if code, body := send(t, srv, http.MethodPut, "/bin/deadbeef", bearer, strings.Repeat("x", 60)); code/100 != 2 {
		t.Fatalf("upload = %d %s", code, body)
	}
}

func downloadBin(t *testing.T, srv *httptest.Server, bearer string) fetched {
	t.Helper()
	return get(t, srv, "/bin/deadbeef", bearer)
}

// Every byte the cache serves a grant is charged to the grant's team for the
// UTC day. A free team past its cap is refused with 429 until midnight UTC,
// while a funded team, another free team, the operator's own team and the
// operator token keep downloading.
func TestAFreeTeamPastItsDailyDownloadCapIsRefusedUntilMidnight(t *testing.T) {
	srv, _, token := newTeamEgressServer(t)
	free := grantFor(t, token, "free")
	putBin(t, srv, free)
	for i := range 2 {
		if got := downloadBin(t, srv, free); got.status != http.StatusOK || len(got.body) != 60 {
			t.Fatalf("download %d under the cap = %d with %d bytes, want 200 and 60", i, got.status, len(got.body))
		}
	}
	refused := downloadBin(t, srv, free)
	if refused.status != http.StatusTooManyRequests {
		t.Fatalf("a download past the free team's daily cap = %d, want 429", refused.status)
	}
	retry, err := strconv.Atoi(refused.header.Get("Retry-After"))
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	if want := int(midnight.Sub(now).Seconds()); err != nil || retry < want-5 || retry > want+5 {
		t.Fatalf("Retry-After = %q, want the %d seconds until midnight UTC", refused.header.Get("Retry-After"), want)
	}
	for _, want := range []string{"free", "daily download cap", "credits"} {
		if !strings.Contains(string(refused.body), want) {
			t.Errorf("refusal %q does not name %q", refused.body, want)
		}
	}

	for team, downloads := range map[string]int{"other": 2, "paying": 3, authwire.OperatorTeam: 3} {
		bearer := grantFor(t, token, team)
		putBin(t, srv, bearer)
		for i := range downloads {
			if got := downloadBin(t, srv, bearer); got.status != http.StatusOK {
				t.Fatalf("team %s download %d = %d %s, want 200: the cap is the free team's alone", team, i, got.status, got.body)
			}
		}
	}
	putBin(t, srv, token)
	for i := range 3 {
		if got := downloadBin(t, srv, token); got.status != http.StatusOK {
			t.Fatalf("operator token download %d = %d, want 200", i, got.status)
		}
	}
}

// A team's day is saved with the process's, so a restart does not hand a
// capped team a fresh day.
func TestATeamsDailyDownloadsSurviveARestart(t *testing.T) {
	srv, cfg, token := newTeamEgressServer(t)
	free := grantFor(t, token, "free")
	putBin(t, srv, free)
	for range 2 {
		if got := downloadBin(t, srv, free); got.status != http.StatusOK {
			t.Fatalf("download under the cap = %d", got.status)
		}
	}
	if err := flushEgressDay(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	egressMeter = nil
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	restarted := httptest.NewServer(s.handler)
	defer restarted.Close()
	if got := downloadBin(t, restarted, free); got.status != http.StatusTooManyRequests {
		t.Fatalf("a capped team's download after a restart = %d, want 429", got.status)
	}
	other := grantFor(t, token, "other")
	putBin(t, restarted, other)
	if got := downloadBin(t, restarted, other); got.status != http.StatusOK {
		t.Fatalf("another team after the restart = %d, want 200", got.status)
	}
}

// A zero cap turns the per-team cap off, leaving the process-wide cap as the
// only refusal.
func TestAZeroTeamCapRefusesNothing(t *testing.T) {
	const token = "operator-token"
	srv, _, _ := newBlobServerWith(t, token, func(c *Config, _ *s3.Client) {
		c.TeamDailyDownloadFreeBytes = 0
	})
	free := grantFor(t, token, "free")
	putBin(t, srv, free)
	for i := range 4 {
		if got := downloadBin(t, srv, free); got.status != http.StatusOK {
			t.Fatalf("download %d with the cap off = %d, want 200", i, got.status)
		}
	}
}

// The per-team metric names the ten teams that downloaded most today and
// folds the rest into one series, so a thousand teams cost eleven series.
func TestTeamDownloadSeriesAreBounded(t *testing.T) {
	day := map[string]int64{BearerPrincipal: 1 << 30, "anonymous": 1 << 30}
	for i := range 12 {
		day[teamPrincipal(fmt.Sprintf("team-%02d", i))] = int64(100 + i)
	}
	series := teamDownloadSeries(day)
	if len(series) != 11 {
		t.Fatalf("series = %v, want ten teams and one fold", series)
	}
	if series["team-11"] != 111 || series["team-02"] != 102 {
		t.Fatalf("series = %v, want the ten largest teams by name", series)
	}
	if _, ok := series["team-01"]; ok {
		t.Fatalf("series = %v, named a team outside the ten largest", series)
	}
	if series[teamDownloadOther] != 100+101 {
		t.Fatalf("fold = %d, want the 201 bytes of the two smallest teams", series[teamDownloadOther])
	}
}
