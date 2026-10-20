package acceptance_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestIdentityLinkHTTPAddressDoesNotSelectAccountProperty(t *testing.T) {
	property := func(email string) bool {
		f := newIdentityLinkHTTP(t)
		body, err := json.Marshal([]map[string]any{{"email": "verified-" + email + "@elsewhere.test", "primary": true, "verified": true}})
		if err != nil {
			t.Fatal(err)
		}
		f.provider.emailsBody = string(body)
		w := f.complete(f.ready(t))
		var linked, account string
		if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT i.email,a.email FROM identities i JOIN accounts a ON a.id=i.account_id WHERE i.provider='github' AND i.subject='123' AND i.account_id='member'`).Scan(&linked, &account); err != nil {
			t.Fatal(err)
		}
		return w.Code == 303 && linked != "member@example.test" && account == "member@example.test" && oauthCookie(w, "__Host-sw_session") == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityLinkHTTPCommittedIdentityConflicts(t *testing.T) {
	for _, tc := range []struct {
		conflict string
		reason   string
	}{
		{`INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','123','stranger','stranger@example.test',1,1)`, "identity_linked_elsewhere"},
		{`INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','123','member','member@example.test',1,1)`, "identity_already_linked"},
		{`INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','999','member','member@example.test',1,1)`, "provider_already_linked"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			f := newIdentityLinkHTTP(t)
			f.exec(t, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('stranger','stranger@example.test',1,1)`)
			cookie := f.ready(t)
			before, expiry := f.state(t)
			f.provider.onToken = func() { f.exec(t, tc.conflict) }
			w := f.complete(cookie)
			assertEngineError(t, w, 409, apierr.Conflict)
			var result apierr.Envelope
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			after, expires := f.state(t)
			consumed, committed := f.links(t)
			if result.Error.Details["reason"] != tc.reason || consumed != 1 || committed != 0 ||
				f.provider.calls != 3 || before != after || expiry != expires || oauthCookie(w, "__Host-sw_session") != nil {
				t.Fatalf("HTTP %d %s links=%d/%d calls=%d", w.Code, w.Body, consumed, committed, f.provider.calls)
			}
		})
	}
}

func TestAccountUnlinkHTTPPreservesLastMethodAndReauthenticationRefusals(t *testing.T) {
	f := newSessionFlow(t)
	writes := &accountWrites{}
	h := f.server.WithAccountWriter(writes).Handler()
	for _, tc := range []struct {
		err    error
		code   apierr.Code
		reason string
	}{
		{store.ErrLastSignInMethod, apierr.Conflict, "last_sign_in_method"},
		{&apierr.Error{Code: apierr.Forbidden, Message: "sign in again", Details: map[string]any{"reason": apierr.ReasonReauthRequired}}, apierr.Forbidden, string(apierr.ReasonReauthRequired)},
	} {
		writes.err = tc.err
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request("DELETE", "/api/v1/me/identities/github", ""))
		assertEngineError(t, w, apierr.Status(tc.code), tc.code)
		var result apierr.Envelope
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		after, expires := f.state(t)
		if result.Error.Details["reason"] != tc.reason || before != after || expiry != expires || strings.Contains(w.Body.String(), f.raw) {
			t.Fatalf("HTTP %d %s", w.Code, w.Body)
		}
	}
}
