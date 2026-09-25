package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota/storagequotatest"
)

func newTeamEgressServer(t *testing.T) (*httptest.Server, *storagequotatest.Controller, string) {
	t.Helper()
	const token = "operator-token"
	ctl, url := fakeController(t, token, 1<<20, 120)
	ctl.Tiers["paying"] = storagequota.TierFunded
	srv, _, _ := newBlobServerWith(t, token, func(c *Config, _ *s3.Client) { c.ControllerURL = url })
	return srv, ctl, token
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

// A download by a grant is charged to its team in the controller before the
// first byte. A free team past its cap is refused with 429 and the
// controller's Retry-After, while a funded team, another free team, the
// operator's own team and the operator token keep downloading.
func TestAFreeTeamPastItsDailyDownloadCapIsRefused(t *testing.T) {
	srv, ctl, token := newTeamEgressServer(t)
	free := grantFor(t, token, "free")
	putBin(t, srv, free)
	for i := range 2 {
		if got := downloadBin(t, srv, free); got.status != http.StatusOK || len(got.body) != 60 {
			t.Fatalf("download %d under the cap = %d with %d bytes, want 200 and 60", i, got.status, len(got.body))
		}
	}
	refused := downloadBin(t, srv, free)
	if refused.status != http.StatusTooManyRequests || len(refused.body) == 60 {
		t.Fatalf("a download past the free team's daily cap = %d, want 429 and no binary", refused.status)
	}
	if refused.header.Get("Retry-After") != "3600" || !strings.Contains(string(refused.body), "daily download cap") {
		t.Fatalf("refusal = Retry-After %q, body %q; want the controller's", refused.header.Get("Retry-After"), refused.body)
	}
	if got := ctl.Downloaded("free"); got != 120 {
		t.Fatalf("the controller charged the free team %d bytes, want the 120 it was sent", got)
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
	if got := ctl.Downloaded(authwire.OperatorTeam); got != 0 {
		t.Fatalf("the operator's team was charged %d bytes, want none", got)
	}
	putBin(t, srv, token)
	for i := range 3 {
		if got := downloadBin(t, srv, token); got.status != http.StatusOK {
			t.Fatalf("operator token download %d = %d, want 200", i, got.status)
		}
	}
}

// A response of unknown length is checked for room when it starts and
// charged what it sent when it ends.
func TestAStreamedDownloadIsChargedWhatItSent(t *testing.T) {
	srv, ctl, token := newTeamEgressServer(t)
	free := grantFor(t, token, "free")
	for _, name := range []string{"a.txt", "b.txt"} {
		if code, body := send(t, srv, http.MethodPost, "/artifacts/job-1?path="+name, free, strings.Repeat("a", 20)); code/100 != 2 {
			t.Fatalf("upload = %d %s", code, body)
		}
	}
	got := get(t, srv, "/artifacts/job-1?glob=*", free)
	if got.status != http.StatusOK || got.header.Get("Content-Length") != "" {
		t.Fatalf("artifact tar = %d, Content-Length %q; want a 200 stream", got.status, got.header.Get("Content-Length"))
	}
	if charged := ctl.Downloaded("free"); charged != int64(len(got.body)) {
		t.Fatalf("the controller charged %d bytes for a %d-byte stream", charged, len(got.body))
	}
}

// While the controller cannot count, a free team's download is refused with
// 503 before any byte; the operator and a team answered funded moments
// before download on.
func TestDownloadsFailClosedWhileTheControllerCannotCount(t *testing.T) {
	srv, ctl, token := newTeamEgressServer(t)
	free, paying := grantFor(t, token, "free"), grantFor(t, token, "paying")
	putBin(t, srv, free)
	putBin(t, srv, paying)
	putBin(t, srv, token)
	if got := downloadBin(t, srv, paying); got.status != http.StatusOK {
		t.Fatalf("a funded download with the controller up = %d", got.status)
	}
	ctl.SetDown(true)
	if got := downloadBin(t, srv, free); got.status != http.StatusServiceUnavailable || len(got.body) == 60 {
		t.Fatalf("a free team's download with the controller down = %d, want 503 and no binary", got.status)
	}
	if got := downloadBin(t, srv, token); got.status != http.StatusOK {
		t.Fatalf("the operator's download with the controller down = %d, want 200", got.status)
	}
	if got := downloadBin(t, srv, paying); got.status != http.StatusOK {
		t.Fatalf("a recently funded team's download with the controller down = %d, want 200", got.status)
	}
	ctl.SetDown(false)
	if got := downloadBin(t, srv, free); got.status != http.StatusOK {
		t.Fatalf("a free team's download with the controller back = %d, want 200", got.status)
	}
}
