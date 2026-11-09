package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func devicePost(t *testing.T, h http.Handler, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	return engineClaimHTTP(t, h, "", "POST", path, body, status)
}

func startDevice(t *testing.T, s *serverFixture, body string) controllerwire.DeviceLoginStarted {
	t.Helper()
	var started controllerwire.DeviceLoginStarted
	if err := json.Unmarshal(devicePost(t, s.Handler(), "/api/v1/auth/device", body, 201).Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(started.DeviceCode, deviceCodePrefix) || len(started.UserCode) != userCodeLength || started.ExpiresIn != 600 || started.Interval != 5 || started.VerificationPath != "/device" {
		t.Fatalf("started = %+v", started)
	}
	return started
}

// safety: the store rate-limits polls by their recorded time, so tests age that time instead of sleeping.
func pollDevice(t *testing.T, s *serverFixture, device string, status int) controllerwire.DeviceLoginPoll {
	t.Helper()
	engineSQL(t, s.store, `UPDATE device_logins SET polled_at = polled_at - 120000000000 WHERE polled_at IS NOT NULL`)
	var poll controllerwire.DeviceLoginPoll
	w := devicePost(t, s.Handler(), "/api/v1/auth/device/token", `{"device_code":"`+device+`"}`, status)
	if status == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &poll); err != nil {
			t.Fatal(err)
		}
	}
	return poll
}

func deviceRefusalReason(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	reason, _ := body.Error.Details["reason"].(string)
	return reason
}

func TestDeviceLoginIssuesTheApproversCLITokenOnce(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "editor")
	started := startDevice(t, s, `{"kind":"member","machine":"korey-laptop"}`)
	if poll := pollDevice(t, s, started.DeviceCode, 200); poll.Status != controllerwire.DevicePending || poll.Credential != nil {
		t.Fatalf("first poll = %+v", poll)
	}
	var slow controllerwire.DeviceLoginPoll
	if err := json.Unmarshal(devicePost(t, s.Handler(), "/api/v1/auth/device/token", `{"device_code":"`+started.DeviceCode+`"}`, 200).Body.Bytes(), &slow); err != nil {
		t.Fatal(err)
	}
	if slow.Status != controllerwire.DeviceSlowDown || slow.Interval != 10 {
		t.Fatalf("early poll = %+v, want slow_down with a 10s interval", slow)
	}

	typed := strings.ToLower(started.UserCode[:4]) + "-" + started.UserCode[4:]
	var lookup controllerwire.DeviceLoginLookup
	if err := json.Unmarshal(reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+typed+`"}`, 200).Body.Bytes(), &lookup); err != nil {
		t.Fatal(err)
	}
	want := []string{"runs.read", "logs.read", "runs.write", "runs.control", "approvals.write"}
	if lookup.Machine != "korey-laptop" || lookup.Team != "a" || lookup.Kind != "member" || !slices.Equal(lookup.Grants, want) || lookup.Runner != nil {
		t.Fatalf("lookup = %+v", lookup)
	}
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 404)

	poll := pollDevice(t, s, started.DeviceCode, 200)
	if poll.Status != controllerwire.DeviceApproved || poll.Credential == nil || poll.Credential.Team != "a" || !slices.Equal(poll.Credential.Grants, want) || poll.Credential.Principal != "member" {
		t.Fatalf("approved poll = %+v", poll)
	}
	var lifetime int64
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT expires_at - created_at FROM capabilities WHERE prefix=?`, poll.Credential.Prefix).Scan(&lifetime); err != nil || time.Duration(lifetime) != 90*24*time.Hour {
		t.Fatalf("token lifetime %v %v, want the 90-day member default", time.Duration(lifetime), err)
	}
	engineClaimHTTP(t, s.Handler(), poll.Credential.Capability, "GET", "/api/v1/auth/whoami", "", 200)

	reused := devicePost(t, s.Handler(), "/api/v1/auth/device/token", `{"device_code":"`+started.DeviceCode+`"}`, 400)
	if reason := deviceRefusalReason(t, reused); reason != controllerwire.DeviceInvalidGrant {
		t.Fatalf("reused code reason %q", reason)
	}
	var decided int
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='device_login.decided' AND actor='member'`).Scan(&decided); err != nil || decided != 1 {
		t.Fatalf("decision audit rows %d %v", decided, err)
	}
}

func TestDeviceLoginRefusals(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		s, raw, csrf := reviewAccount(t, "editor")
		started := startDevice(t, s, `{"kind":"member","machine":"box"}`)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/deny", `{"user_code":"`+started.UserCode+`"}`, 204)
		w := devicePost(t, s.Handler(), "/api/v1/auth/device/token", `{"device_code":"`+started.DeviceCode+`"}`, 400)
		if reason := deviceRefusalReason(t, w); reason != controllerwire.DeviceDenied {
			t.Fatalf("denied reason %q", reason)
		}
	})
	t.Run("cancelled by the device", func(t *testing.T) {
		s, raw, csrf := reviewAccount(t, "editor")
		started := startDevice(t, s, `{"kind":"member","machine":"box"}`)
		devicePost(t, s.Handler(), "/api/v1/auth/device/cancel", `{"device_code":"`+started.DeviceCode+`"}`, 204)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 404)
	})
	t.Run("expired", func(t *testing.T) {
		s, raw, csrf := reviewAccount(t, "editor")
		started := startDevice(t, s, `{"kind":"member","machine":"box"}`)
		engineSQL(t, s.store, `UPDATE device_logins SET expires_at = ?`, time.Now().Add(-time.Second).UnixNano())
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 404)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 404)
		w := devicePost(t, s.Handler(), "/api/v1/auth/device/token", `{"device_code":"`+started.DeviceCode+`"}`, 400)
		if reason := deviceRefusalReason(t, w); reason != controllerwire.DeviceExpired {
			t.Fatalf("expired reason %q", reason)
		}
	})
	t.Run("another deployment", func(t *testing.T) {
		home, _, _ := reviewAccount(t, "editor")
		other, raw, csrf := reviewAccount(t, "owner")
		started := startDevice(t, home, `{"kind":"member","machine":"box"}`)
		reviewSessionRequest(t, other, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 404)
		w := devicePost(t, other.Handler(), "/api/v1/auth/device/token", `{"device_code":"`+started.DeviceCode+`"}`, 400)
		if reason := deviceRefusalReason(t, w); reason != controllerwire.DeviceInvalidGrant {
			t.Fatalf("foreign deployment reason %q", reason)
		}
	})
	t.Run("never more than the approver holds", func(t *testing.T) {
		s, raw, csrf := reviewAccount(t, "reader")
		asked := startDevice(t, s, `{"kind":"member","machine":"box","grants":["runs.read","runs.write"]}`)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+asked.UserCode+`"}`, 403)
		plain := startDevice(t, s, `{"kind":"member","machine":"box"}`)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+plain.UserCode+`"}`, 204)
		if poll := pollDevice(t, s, plain.DeviceCode, 200); poll.Credential == nil || !slices.Equal(poll.Credential.Grants, []string{"runs.read", "logs.read"}) {
			t.Fatalf("reader default poll = %+v", poll)
		}
		devicePost(t, s.Handler(), "/api/v1/auth/device", `{"kind":"member","machine":"box","grants":["team.admin"]}`, 400)
		runner := startDevice(t, s, `{"kind":"runner","runner":{"name":"box","personal":true}}`)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+runner.UserCode+`"}`, 403)
	})
	t.Run("approving session ended before redemption", func(t *testing.T) {
		s, raw, csrf := reviewAccount(t, "editor")
		started := startDevice(t, s, `{"kind":"member","machine":"box"}`)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
		reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/logout", "", 204)
		engineSQL(t, s.store, `UPDATE device_logins SET polled_at = NULL`)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/device/token", strings.NewReader(`{"device_code":"`+started.DeviceCode+`"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code == 200 || strings.Contains(w.Body.String(), `"capability"`) {
			t.Fatalf("an ended session still minted a credential: %d %s", w.Code, w.Body)
		}
	})
}

func TestDeviceLoginEnrollsARunnerUnderTheApprover(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "editor")
	personal := startDevice(t, s, `{"kind":"runner","runner":{"name":"korey-laptop","personal":true}}`)
	var lookup controllerwire.DeviceLoginLookup
	if err := json.Unmarshal(reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+personal.UserCode+`"}`, 200).Body.Bytes(), &lookup); err != nil {
		t.Fatal(err)
	}
	if lookup.Kind != "runner" || lookup.Machine != "korey-laptop" || lookup.Runner == nil || !lookup.Runner.Personal || !slices.Equal(lookup.Grants, []string{"nodes.claim", "triggers.claim"}) {
		t.Fatalf("runner lookup = %+v", lookup)
	}
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+personal.UserCode+`"}`, 204)
	poll := pollDevice(t, s, personal.DeviceCode, 200)
	if poll.Credential == nil || poll.Credential.Principal != "korey-laptop" || !slices.Equal(poll.Credential.Grants, []string{"nodes.claim", "triggers.claim"}) {
		t.Fatalf("runner poll = %+v", poll)
	}
	var kind string
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT subject_kind FROM capabilities WHERE prefix=?`, poll.Credential.Prefix).Scan(&kind); err != nil || kind != "machine" {
		t.Fatalf("runner credential kind %q %v", kind, err)
	}
}

func TestDeviceUserCodesAreTypedFromTheDisplayAlphabet(t *testing.T) {
	for raw, want := range map[string]string{"bcdf-ghjk": "BCDFGHJK", " BCDF GHJK ": "BCDFGHJK", "BCDFGHJ": "", "BCDFGHJA": "", "BCDF-GHJK-L": ""} {
		got, ok := normalizeUserCode(raw)
		if ok != (want != "") || ok && got != want {
			t.Errorf("normalizeUserCode(%q) = %q %v, want %q", raw, got, ok, want)
		}
	}
	code, err := newUserCode()
	if _, ok := normalizeUserCode(code); err != nil || !ok {
		t.Fatalf("generated code %q does not normalize: %v", code, err)
	}
}

func TestDeviceLoginRefusesWhatABrowserApprovalShouldNotGrant(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	for _, body := range []string{
		`{"kind":"runner","runner":{"name":"b","repos":["github.com/*/*"]}}`,
		`{"kind":"runner","runner":{"name":"b","repos":["github.com/acme/*"],"trust":"any"}}`,
		`{"kind":"runner","runner":{"name":"b","personal":true,"reserved_pools":["deploy"]}}`,
		`{"kind":"runner","runner":{"name":"b","personal":true,"platform":"kubernetes"}}`,
		`{"kind":"runner","runner":{"name":"b","personal":true,"grants":["nodes.claim","claims.launch"]}}`,
		`{"kind":"runner","runner":{"name":"b","personal":true,"repos":["github.com/acme/` + strings.Repeat("x", 300) + `"]}}`,
		`{"kind":"member","machine":"box","team":"Not A Slug"}`,
		`{"kind":"member","machine":"box"` + strings.Repeat(" ", 20<<10) + `}`,
	} {
		devicePost(t, s.Handler(), "/api/v1/auth/device", body, 400)
	}
	var rows int
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM device_logins`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("refused starts stored %d rows %v", rows, err)
	}

	foreign := startDevice(t, s, `{"kind":"member","machine":"box","team":"other"}`)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+foreign.UserCode+`"}`, 403)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+foreign.UserCode+`"}`, 403)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/deny", `{"user_code":"`+foreign.UserCode+`"}`, 204)
	own := startDevice(t, s, `{"kind":"member","machine":"box","team":"a"}`)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+own.UserCode+`"}`, 204)
}

func TestDeviceApprovalRunsTheStoreIssuanceCheck(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "editor")
	engineSQL(t, s.store, `UPDATE teams SET members_can_add_machines=0 WHERE name='a'`)
	started := startDevice(t, s, `{"kind":"runner","runner":{"name":"desk","personal":true}}`)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 403)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 200)

	member := startDevice(t, s, `{"kind":"member","machine":"box"}`)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+member.UserCode+`"}`, 204)
	engineSQL(t, s.store, `UPDATE memberships SET role='reader' WHERE account_id='member'`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/device/token", strings.NewReader(`{"device_code":"`+member.DeviceCode+`"}`))
	r.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"capability"`) {
		t.Fatalf("a downgraded approver still minted the editor grants: %d %s", w.Code, w.Body)
	}
}

func TestRunnerTrustRefusalNamesOnlyCurrentClasses(t *testing.T) {
	_, err := runnerEnrollmentSpec(runnerEnrollmentRequest{Name: "b", Repos: []string{"github.com/acme/*"}, Trust: "verified"}, capability.AuthorityRef{}, "a")
	if err == nil || strings.Contains(err.Error(), "verified,") || !strings.Contains(err.Error(), "verified-default or any") {
		t.Fatalf("trust refusal = %v", err)
	}
}

//sparkwing:critical
func TestDeviceApprovalShowsExactlyTheAllowedRunnerLabels(t *testing.T) {
	boundary := []string{strings.Repeat("a", 128)}
	for i := range 31 {
		boundary = append(boundary, fmt.Sprintf("deploy-pool-%d", i))
	}
	for _, tc := range []struct {
		name      string
		requested []string
		allowed   []string
	}{
		{name: "normalized", requested: []string{"deploy-kikd-prod", "tool:terraform", " os=linux ", "pool=ignored"}, allowed: []string{"deploy-kikd-prod", "tool:terraform", "os=linux"}},
		{name: "none", requested: []string{}, allowed: []string{}},
		{name: "complete boundary", requested: boundary, allowed: boundary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, "owner")
			labelsJSON, err := json.Marshal(tc.requested)
			if err != nil {
				t.Fatal(err)
			}
			started := startDevice(t, s, `{"kind":"runner","runner":{"name":"label-consent","personal":true,"allowed_labels":`+string(labelsJSON)+`}}`)
			response := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 200)
			var lookup controllerwire.DeviceLoginLookup
			if err := json.Unmarshal(response.Body.Bytes(), &lookup); err != nil {
				t.Fatal(err)
			}
			if lookup.Runner == nil || lookup.Runner.AllowedLabels == nil || !slices.Equal(lookup.Runner.AllowedLabels, tc.allowed) {
				t.Fatalf("approval hid allowed labels: %+v, want %v", lookup.Runner, tc.allowed)
			}
			reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
			poll := pollDevice(t, s, started.DeviceCode, 200)
			if poll.Credential == nil {
				t.Fatal("approval issued no runner")
			}
			var body []byte
			if err := teststore.SQL(t, s.store).QueryRow(`SELECT authority_json FROM capabilities WHERE prefix=?`, poll.Credential.Prefix).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var record struct {
				Origin struct {
					AllowedLabels []string `json:"allowed_labels"`
				} `json:"origin"`
			}
			if err := json.Unmarshal(body, &record); err != nil || !slices.Equal(record.Origin.AllowedLabels, lookup.Runner.AllowedLabels) {
				t.Fatalf("issued labels differ from consent: %s %v", body, err)
			}
		})
	}
}
