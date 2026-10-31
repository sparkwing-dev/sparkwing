package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestSurfaceErrorsUseSharedEnvelope(t *testing.T) {
	inv, parseErr := clisurface.Parse("sparkwing", []string{"runs", "status", "--run=r1", "--output=json", "--unknown=private-value"})
	var out bytes.Buffer
	calls := 0
	encode := func(w io.Writer, failure *apierr.Error) error {
		calls++
		return json.NewEncoder(w).Encode(apierr.Envelope{Error: failure})
	}
	if err := writeInvocationError(&out, &invocationError{inv: inv, cause: parseErr}, encode); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.Count(out.String(), "\n") != 1 || strings.Contains(out.String(), "private-value") {
		t.Fatalf("diagnostic: %s", &out)
	}
	var envelope apierr.Envelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.Error.Code != apierr.BadRequest {
		t.Fatalf("envelope: %s %v", &out, err)
	}
}

func TestSurfaceErrorsPreserveStructuredHintAndCode(t *testing.T) {
	want := &apierr.Error{Code: apierr.LockHeld, Message: "claim expired", Hint: "claim again", Details: map[string]any{"reason": "lease"}}
	for _, format := range []string{"plain", "pretty", "json"} {
		inv, err := clisurface.Parse("sparkwing", []string{"runs", "status", "--run=r1", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		encode := func(w io.Writer, got *apierr.Error) error {
			if got != want {
				t.Fatalf("structured error was replaced: %#v", got)
			}
			return json.NewEncoder(w).Encode(apierr.Envelope{Error: got})
		}
		if err := writeInvocationError(&out, &invocationError{inv: inv, cause: want}, encode); err != nil {
			t.Fatal(err)
		}
		if format != "json" && out.String() != "sparkwing: runs status: claim expired\nhint: claim again\n" {
			t.Fatalf("diagnostic: %q", out.String())
		}
	}
}

func TestSurfaceErrorExitCodes(t *testing.T) {
	for _, code := range []int{1, 2, 3} {
		err := &invocationError{cause: exitError(code, errors.New("failed"))}
		if exitCodeFor(err) != code {
			t.Fatalf("exit %d became %d", code, exitCodeFor(err))
		}
	}
}

func TestSurfaceConfigErrorIsBadRequest(t *testing.T) {
	inv, err := clisurface.Parse("sparkwing", []string{"profiles", "list", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = writeInvocationError(&out, &invocationError{inv: inv, cause: userconfig.Invalid("profiles", "invalid configuration")}, func(w io.Writer, e *apierr.Error) error { return json.NewEncoder(w).Encode(apierr.Envelope{Error: e}) })
	if err != nil {
		t.Fatal(err)
	}
	var got apierr.Envelope
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Error.Code != apierr.BadRequest {
		t.Fatalf("error: %s", &out)
	}
}

func TestSurfaceConfigErrorsThroughProfiles(t *testing.T) {
	path := profilesFixturePath(t)
	if err := os.WriteFile(path, []byte("profiles: wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := clisurface.Parse("sparkwing", []string{"profiles", "list", "-o=json"})
	if err != nil {
		t.Fatal(err)
	}
	failure := runProfilesArgs([]string{"list", "-o=json"})
	if failure == nil {
		t.Fatal("invalid config accepted")
	}
	var out bytes.Buffer
	if err := writeInvocationError(&out, &invocationError{inv: inv, cause: failure}, func(w io.Writer, e *apierr.Error) error { return json.NewEncoder(w).Encode(apierr.Envelope{Error: e}) }); err != nil {
		t.Fatal(err)
	}
	var got apierr.Envelope
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Error.Code != apierr.BadRequest {
		t.Fatalf("config error: %s", &out)
	}
}

func TestSurfaceOperatorCeilingErrorKeepsCanonicalCode(t *testing.T) {
	inv, err := clisurface.Parse("sparkwing", []string{"profiles", "list", "-o=json"})
	if err != nil {
		t.Fatal(err)
	}
	failure := &settings.Error{Key: "duration", CeilingFrom: &settings.Origin{}, Reason: "above ceiling"}
	var out bytes.Buffer
	if err := writeInvocationError(&out, &invocationError{inv: inv, cause: failure}, func(w io.Writer, e *apierr.Error) error { return json.NewEncoder(w).Encode(apierr.Envelope{Error: e}) }); err != nil {
		t.Fatal(err)
	}
	var got apierr.Envelope
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Error.Code != apierr.LimitExceeded {
		t.Fatalf("ceiling error: %s", &out)
	}
}

func TestRevokedTokenRefusalExplainsSignInAndProfileReplacement(t *testing.T) {
	for _, format := range []string{"plain", "json"} {
		inv, err := clisurface.Parse("sparkwing", []string{"runs", "list", "--profile", "expired", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		failure := &apierr.Error{Code: apierr.Unauthenticated, Message: "authority has ended"}
		var out bytes.Buffer
		if err := writeInvocationError(&out, &invocationError{inv: inv, cause: failure}, apierr.WriteJSON); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Sign in again", "dashboard", "replacement token", "sparkwing profiles set --profile 'expired' --token-stdin"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%s diagnostic = %s; missing %q", format, &out, want)
			}
		}
		if failure.Hint != "" {
			t.Fatal("diagnostic mutated the original failure")
		}
	}
}

func TestDefaultProfileRevokedTokenNamesItsReplacementCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		if err := apierr.WriteJSON(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "authority has ended"}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	setProfileCmdFixture(t, fmt.Sprintf("profiles:\n  expired: {default: true, controller: {url: %s, token: revoked}}\n", server.URL))
	failure := runSparkwing([]string{"runs", "list", "-o", "json"})
	if failure == nil {
		t.Fatal("revoked token accepted")
	}
	var out bytes.Buffer
	writeFailure(&out, failure)
	if !strings.Contains(out.String(), "sparkwing profiles set --profile 'expired' --token-stdin") {
		t.Fatalf("default-profile refusal: %s", &out)
	}
}
