package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func cliMemberToken(t *testing.T, s *serverFixture, raw, csrf string) string {
	t.Helper()
	started := startDevice(t, s, `{"kind":"member","machine":"laptop"}`)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
	poll := pollDevice(t, s, started.DeviceCode, 200)
	if poll.Credential == nil || slices.Contains(poll.Credential.Grants, "team.admin") {
		t.Fatalf("member login = %+v", poll)
	}
	return poll.Credential.Capability
}

func startStepUp(t *testing.T, s *serverFixture, bearer string, status int) controllerwire.DeviceLoginStarted {
	t.Helper()
	w := engineClaimHTTP(t, s.Handler(), bearer, "POST", "/api/v1/auth/device/step-up", `{"kind":"admin","machine":"laptop"}`, status)
	var started controllerwire.DeviceLoginStarted
	if status == 201 {
		if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
			t.Fatal(err)
		}
	}
	return started
}

func secondAccount(t *testing.T, s *serverFixture, id, role string) (string, string) {
	t.Helper()
	engineSQL(t, s.store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1)`, id, id+"@example.test")
	engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a',?,?,1)`, id, role)
	raw, csrf, _, err := s.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: id}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return raw, csrf
}

func TestDeviceStepUpIssuesShortLivedAdminToTheRequestingOwner(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	member := cliMemberToken(t, s, raw, csrf)
	if refused := engineClaimHTTP(t, s.Handler(), member, "GET", "/api/v1/subscriptions", "", 403); !strings.Contains(refused.Body.String(), "sparkwing login --admin") {
		t.Fatalf("member refusal does not name the step-up: %s", refused.Body)
	}

	started := startStepUp(t, s, member, 201)
	var lookup controllerwire.DeviceLoginLookup
	if err := json.Unmarshal(reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 200).Body.Bytes(), &lookup); err != nil {
		t.Fatal(err)
	}
	want := []string{"runs.read", "logs.read", "runs.write", "runs.control", "approvals.write", "team.admin"}
	if lookup.Kind != controllerwire.DeviceLoginAdmin || lookup.Team != "a" || lookup.Machine != "laptop" || !slices.Equal(lookup.Grants, want) || lookup.SignInRequired {
		t.Fatalf("admin lookup = %+v", lookup)
	}
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
	poll := pollDevice(t, s, started.DeviceCode, 200)
	if poll.Credential == nil || poll.Credential.Team != "a" || poll.Credential.Principal != "member" || !slices.Equal(poll.Credential.Grants, want) || poll.Credential.ExpiresAt == nil {
		t.Fatalf("admin poll = %+v", poll)
	}
	var lifetime int64
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT expires_at - created_at FROM capabilities WHERE prefix=?`, poll.Credential.Prefix).Scan(&lifetime); err != nil || (time.Duration(lifetime)-time.Hour).Abs() > time.Minute {
		t.Fatalf("admin token lifetime %v %v, want one hour", time.Duration(lifetime), err)
	}
	if poll.Member == nil || !slices.Equal(poll.Member.Grants, want[:5]) || poll.Member.Prefix == poll.Credential.Prefix {
		t.Fatalf("re-issued member token = %+v", poll.Member)
	}
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT expires_at - created_at FROM capabilities WHERE prefix=?`, poll.Member.Prefix).Scan(&lifetime); err != nil || time.Duration(lifetime) != 90*24*time.Hour {
		t.Fatalf("re-issued member lifetime %v %v, want the 90-day member default", time.Duration(lifetime), err)
	}
	admin := poll.Credential.Capability
	engineClaimHTTP(t, s.Handler(), admin, "GET", "/api/v1/subscriptions", "", 200)
	engineClaimHTTP(t, s.Handler(), poll.Member.Capability, "GET", "/api/v1/subscriptions", "", 403)

	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/logout", "", 204)
	engineClaimHTTP(t, s.Handler(), admin, "GET", "/api/v1/subscriptions", "", 401)
}

func TestDeviceStepUpRefusesNonOwners(t *testing.T) {
	for _, role := range []string{"editor", "reader"} {
		t.Run(role, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, role)
			member := cliMemberToken(t, s, raw, csrf)
			started := startStepUp(t, s, member, 201)
			w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 403)
			if !strings.Contains(w.Body.String(), "only team owners") {
				t.Fatalf("non-owner refusal = %s", w.Body)
			}
			reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 403)
			reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/deny", `{"user_code":"`+started.UserCode+`"}`, 204)
			if reason := deviceRefusalReason(t, devicePost(t, s.Handler(), "/api/v1/auth/device/token", `{"device_code":"`+started.DeviceCode+`"}`, 400)); reason != controllerwire.DeviceDenied {
				t.Fatalf("denied reason %q", reason)
			}
			var admins int
			if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM capabilities WHERE grants LIKE '%team.admin%'`).Scan(&admins); err != nil || admins != 0 {
				t.Fatalf("%d team.admin credentials exist after a %s step-up: %v", admins, role, err)
			}
		})
	}
}

func TestDeviceStepUpBindsTheRequestingAccount(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	member := cliMemberToken(t, s, raw, csrf)
	started := startStepUp(t, s, member, 201)
	otherRaw, otherCSRF := secondAccount(t, s, "other", "owner")
	w := reviewSessionRequest(t, s, otherRaw, otherCSRF, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 403)
	if !strings.Contains(w.Body.String(), "another account") {
		t.Fatalf("foreign approver refusal = %s", w.Body)
	}
	reviewSessionRequest(t, s, otherRaw, otherCSRF, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 403)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
}

func TestDeviceStepUpNeedsARecentSignIn(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	member := cliMemberToken(t, s, raw, csrf)
	started := startStepUp(t, s, member, 201)
	engineSQL(t, s.store, `UPDATE sessions SET created_at = created_at - ?`, int64(11*time.Minute))
	var lookup controllerwire.DeviceLoginLookup
	if err := json.Unmarshal(reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/lookup", `{"user_code":"`+started.UserCode+`"}`, 200).Body.Bytes(), &lookup); err != nil {
		t.Fatal(err)
	}
	if !lookup.SignInRequired {
		t.Fatalf("stale lookup = %+v, want sign_in_required", lookup)
	}
	w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 403)
	if reason := deviceRefusalReasonOf(t, w.Body.Bytes()); reason != "reauth_required" {
		t.Fatalf("stale approval reason %q: %s", reason, w.Body)
	}
	fresh, freshCSRF, _, err := s.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reviewSessionRequest(t, s, fresh, freshCSRF, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
}

func TestDeviceStepUpStartRefusals(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	for _, body := range []string{
		`{"kind":"admin","machine":"box"}`,
		`{"kind":"member","machine":"box","account":"member"}`,
		`{"kind":"admin","machine":"box","account":"member","team":"a"}`,
	} {
		devicePost(t, s.Handler(), "/api/v1/auth/device", body, 400)
	}
	engineClaimHTTP(t, s.Handler(), "", "POST", "/api/v1/auth/device/step-up", `{"kind":"admin","machine":"box"}`, 401)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/step-up", `{"kind":"admin","machine":"box"}`, 403)
	member := cliMemberToken(t, s, raw, csrf)
	for _, body := range []string{
		`{"kind":"member","machine":"box"}`,
		`{"kind":"admin","machine":"box","grants":["runs.read"]}`,
		`{"kind":"admin","machine":"box","team":"other"}`,
		`{"kind":"admin","machine":"not a name"}`,
	} {
		engineClaimHTTP(t, s.Handler(), member, "POST", "/api/v1/auth/device/step-up", body, 400)
	}
	var rows int
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM device_logins WHERE request LIKE '%"admin"%'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("refused step-ups stored %d rows %v", rows, err)
	}
}

func TestDeviceStepUpEndsWhenTheOwnerIsDemoted(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	member := cliMemberToken(t, s, raw, csrf)
	started := startStepUp(t, s, member, 201)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/auth/device/approve", `{"user_code":"`+started.UserCode+`"}`, 204)
	admin := pollDevice(t, s, started.DeviceCode, 200).Credential.Capability
	engineClaimHTTP(t, s.Handler(), admin, "GET", "/api/v1/subscriptions", "", 200)

	otherRaw, otherCSRF := secondAccount(t, s, "other", "owner")
	reviewSessionRequest(t, s, otherRaw, otherCSRF, "PATCH", "/api/v1/team/members/member", `{"role":"editor"}`, 204)
	engineClaimHTTP(t, s.Handler(), admin, "GET", "/api/v1/subscriptions", "", 401)
}

func deviceRefusalReasonOf(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	reason, _ := envelope.Error.Details["reason"].(string)
	return reason
}
