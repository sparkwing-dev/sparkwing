package client

import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/quick"
)

func TestRunnerSessionPresentationCannotInjectHeadersProperty(t *testing.T) {
	property := func(input []byte, character uint8) bool {
		invalid := byte(character % 34)
		if invalid == 33 {
			invalid = 127
		}
		presentation := hex.EncodeToString(input) + string([]byte{invalid}) + "private"
		called := false
		manager, err := NewSessionRunnerTokenManager(t.Context(), EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
			called = true
			return engineResponse(200, `{"csrf_token":"csrf"}`), nil
		})}}, presentation)
		return err != nil && manager == nil && !called
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSessionEnrollmentNeverReplaysLostCredential(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		calls := 0
		manager, err := NewSessionRunnerTokenManager(t.Context(), EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: engineTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Host != "controller.test" || r.Header.Get("Authorization") != "Session private" {
				t.Fatal("session escaped its selected controller")
			}
			if r.URL.Path == "/api/v1/auth/session" {
				return engineResponse(200, `{"csrf_token":"csrf"}`), nil
			}
			if r.Method != "POST" || r.URL.Path != "/api/v1/runners" || r.GetBody != nil {
				t.Fatal("session enrollment can replay")
			}
			if redirect {
				response := engineResponse(307, "")
				response.Header.Set("Location", "https://other.test/api/v1/runners")
				return response, nil
			}
			return nil, io.EOF
		})}}, "private")
		if err != nil {
			t.Fatal(err)
		}
		result, err := manager.EnrollRunner(t.Context(), EnrollRunnerRequest{Name: "desktop", Repos: []string{"github.com/acme/*"}, Personal: true})
		if err == nil || result != nil || calls != 2 || !redirect && !errors.Is(err, io.EOF) {
			t.Fatalf("one-time session enrollment: result=%v calls=%d error=%v", result != nil, calls, err)
		}
	}
}
