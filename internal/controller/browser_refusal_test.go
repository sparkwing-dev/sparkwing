package controller_test

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/googleauth/googletest"
)

func TestRefusedSignInReturnsToTheSignInPageWithAReason(t *testing.T) {
	f := newIdentityFixture(t)
	if got := f.browserGet("/auth/google/callback?state=lost&code=lost"); got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/login?refused=expired" {
		t.Fatalf("callback without its flow = %d to %q, want the sign-in page naming expired", got.StatusCode, got.Header.Get("Location"))
	}

	start := f.browserGet("/auth/google/start?next=" + url.QueryEscape("/invitations?id=inv-1"))
	flow := responseCookie(start, "__Host-sw_oauth")
	authorize, err := url.Parse(start.Header.Get("Location"))
	if err != nil || flow == nil {
		t.Fatalf("start = %d %v", start.StatusCode, err)
	}
	declined := f.browserGet("/auth/google/callback?"+url.Values{"state": {authorize.Query().Get("state")}, "error": {"access_denied"}}.Encode(), flow)
	if want := "/login?next=%2Finvitations%3Fid%3Dinv-1&refused=cancelled"; declined.StatusCode != http.StatusSeeOther || declined.Header.Get("Location") != want {
		t.Fatalf("declined sign-in = %d to %q, want %q", declined.StatusCode, declined.Header.Get("Location"), want)
	}

	unverified := googletest.Person{Subject: "u", Email: "una@example.com", Name: "Una Test", GivenName: "Una"}
	status := f.oauthSignIn("google", func(verifier string) string { return f.google.Code(unverified, verifier, dashRedirect) }, nil)
	if status != http.StatusSeeOther || f.lastNext != "/login?refused=email_unverified" {
		t.Fatalf("unverified email = %d to %q, want the sign-in page naming email_unverified", status, f.lastNext)
	}
	page := f.browserGet(f.lastNext).body
	if want := html.EscapeString("Your sign-in provider has not verified this account's email address. Verify it with the provider, then sign in again."); !strings.Contains(page, want) {
		t.Fatalf("sign-in page does not explain the refusal:\n%s", page)
	}
	if page := f.browserGet("/login?refused=" + url.QueryEscape("<b>made up</b>")).body; strings.Contains(page, `class="refused"`) || strings.Contains(page, "made up") {
		t.Fatalf("sign-in page showed text for an unknown reason:\n%s", page)
	}
}

func TestInvitationAcceptanceHasNoMemberCap(t *testing.T) {
	f := newIdentityFixture(t)
	owner, _, _ := teamOf(f)
	for i := range 10 {
		email := fmt.Sprintf("guest%d@example.com", i)
		guest := f.user(fmt.Sprintf("g%d", i), email)
		var invitation map[string]json.RawMessage
		if code := f.call("POST", "/api/v1/team/invitations", owner.auth, map[string]string{"email": email, "role": "reader"}, &invitation); code != http.StatusCreated {
			t.Fatalf("invite member %d = %d", i+4, code)
		}
		if len(invitation) != 3 || invitation["accept_url"] == nil || invitation["email_sent"] == nil {
			t.Fatalf("invitation response = %v", invitation)
		}
		var id string
		if err := json.Unmarshal(invitation["id"], &id); err != nil {
			t.Fatal(err)
		}
		if code := f.call("POST", "/api/v1/invitations/"+id+"/accept", guest.auth, nil, nil); code != http.StatusNoContent {
			t.Fatalf("accept member %d = %d", i+4, code)
		}
	}
}

func TestRefusedLinkReturnsToTheSignInsPageWithAReason(t *testing.T) {
	f := newIdentityFixture(t)
	alice := f.user("a", "alice@example.com")
	session := &http.Cookie{Name: "__Host-sw_session", Value: sessionIDOf(alice.auth)}
	got := f.browserGet("/auth/github/link/complete", session)
	if got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/account/sign-ins?refused=expired" {
		t.Fatalf("link completion without its flow = %d to %q, want the sign-ins page naming expired", got.StatusCode, got.Header.Get("Location"))
	}
}

func TestRefusedInvitationNamesItsReason(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	other := f.user("m", "mallory@example.com")
	id := f.invite(owner, "eddie@example.com", "editor")
	var refusal struct {
		Error struct {
			Details struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	accept := func(id string) (int, string) {
		t.Helper()
		var body json.RawMessage
		code := f.call("POST", "/api/v1/invitations/"+id+"/accept", other.auth, nil, &body)
		return code, string(body)
	}
	foreignCode, foreign := accept(id)
	missingCode, missing := accept("inv-that-never-existed")
	if foreignCode != http.StatusNotFound || foreignCode != missingCode || foreign != missing || !strings.Contains(foreign, `"reason":"invitation_not_found"`) {
		t.Fatalf("another address's invitation = %d %s, a missing one = %d %s; want one identical invitation_not_found answer", foreignCode, foreign, missingCode, missing)
	}
	f.advance(8 * 24 * time.Hour)
	other = f.user("m", "mallory@example.com")
	if code, expired := accept(id); code != missingCode || expired != missing {
		t.Fatalf("another address's expired invitation = %d %s, want the missing answer %s", code, expired, missing)
	}
	eddie := f.user("e", "eddie@example.com")
	if code := f.call("POST", "/api/v1/invitations/"+id+"/accept", eddie.auth, nil, &refusal); code != http.StatusConflict || refusal.Error.Details.Reason != "invitation_closed" {
		t.Fatalf("accept after expiry = %d %+v, want 409 invitation_closed", code, refusal)
	}
}
