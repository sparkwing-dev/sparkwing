package acceptance_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
		return w.Code == 303 && f.writer.committed == 1 && f.writer.claim.AccountID == "member" &&
			f.writer.profile.Subject == "123" && f.writer.profile.Email != "member@example.test" &&
			oauthCookie(w, "__Host-sw_session") == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityLinkHTTPCommittedIdentityConflicts(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{store.ErrIdentityLinkedElsewhere, "identity_linked_elsewhere"},
		{store.ErrIdentityAlreadyLinked, "identity_already_linked"},
		{store.ErrProviderAlreadyLinked, "provider_already_linked"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			f := newIdentityLinkHTTP(t)
			cookie := f.ready(t)
			before, expiry := f.state(t)
			f.writer.onConsume = func() { f.writer.failure = tc.err }
			w := f.complete(cookie)
			assertEngineError(t, w, 409, apierr.Conflict)
			var result apierr.Envelope
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			after, expires := f.state(t)
			if result.Error.Details["reason"] != tc.reason || f.writer.consumed != 1 || f.writer.committed != 0 ||
				f.provider.calls != 3 || before != after || expiry != expires || oauthCookie(w, "__Host-sw_session") != nil {
				t.Fatalf("HTTP %d %s writer=%+v calls=%d", w.Code, w.Body, f.writer, f.provider.calls)
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
