package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

type profileTransport func(*http.Request) (*http.Response, error)

func (f profileTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func profileHTTPFixture(t *testing.T, handler func(*http.Request) (int, string)) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = profileTransport(func(r *http.Request) (*http.Response, error) {
		status, body := handler(r)
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func TestProfilesAddStoresExistingCredentialAfterProbe(t *testing.T) {
	path := profilesFixturePath(t)
	token := "swu_existing_credential"
	withStdin(t, token+"\n")
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		if r.Method != "GET" {
			t.Fatalf("probe mutated controller: %s %s", r.Method, r.URL)
		}
		if r.URL.Path == "/api/v1/health" {
			return 200, `{"status":"ok","auth":"enabled"}`
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("credential did not reach controller")
		}
		switch r.URL.Path {
		case "/api/v1/runs":
			return 200, `{"runs":[]}`
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return 500, ""
		}
	})
	out := captureStdout(t, func() {
		if err := runProfiles([]string{"add", "--controller", "https://api.example", "--profile", "prod", "--token-stdin"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, token) {
		t.Fatal("raw credential in result")
	}
	if got := loadSavedProfile(t, path, "prod").ControllerToken(); got != token {
		t.Fatalf("stored %q", got)
	}
}

func TestProfilesAddDerivesNameAndRefusesReplacement(t *testing.T) {
	path := profilesFixturePath(t)
	args := []string{"add", "--controller", "https://api.example", "--no-probe"}
	captureStdout(t, func() {
		if err := runProfiles(args); err != nil {
			t.Fatal(err)
		}
	})
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loadSavedProfile(t, path, "api-example").ControllerURL(); got != "https://api.example" {
		t.Fatal(got)
	}
	if err := runProfiles(args); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("replacement refusal: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("refusal changed config: %v", err)
	}
	withStdin(t, "replacement-credential\n")
	captureStdout(t, func() {
		if err := runProfiles(append(args, "--force", "--token-stdin")); err != nil {
			t.Fatal(err)
		}
	})
	if got := loadSavedProfile(t, path, "api-example").ControllerToken(); got != "replacement-credential" {
		t.Fatal(got)
	}
}

func TestProfilesAddProbeFailureDoesNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name         string
		health, auth int
		token        bool
	}{
		{"offline", 503, 200, true}, {"rejected", 200, 401, true}, {"missing", 200, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := profilesFixturePath(t)
			profileHTTPFixture(t, func(r *http.Request) (int, string) {
				if r.URL.Path == "/api/v1/health" {
					return tc.health, `{"status":"ok","auth":"enabled"}`
				}
				return tc.auth, `{"error":"refused"}`
			})
			args := []string{"add", "--controller", "https://api.example"}
			if tc.token {
				withStdin(t, "swu_not_valid\n")
				args = append(args, "--token-stdin")
			}
			if err := runProfiles(args); err == nil {
				t.Fatal("failed probe accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("failed probe wrote config: %v", err)
			}
		})
	}
}

func TestProfilesRemoveRevokesOnlyMemberCredential(t *testing.T) {
	for _, tc := range []struct {
		name, kind, principal string
		revoked               bool
		wantRemoved           bool
	}{
		{"member", "member", "person", true, true},
		{"principal is not authority", "member", "agent:box", true, true},
		{"machine", "runner", "box", true, false},
		{"revocation unconfirmed", "member", "person", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := profilesFixturePath(t)
			token := "swk_12345678_long_private"
			prefix, err := tokenPrefix(token)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &profile.Config{Profiles: map[string]*profile.Profile{"prod": {Name: "prod", Controller: &profile.ControllerSpec{URL: "https://api.example", Token: token}}}}
			if err := profile.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			deletes := 0
			profileHTTPFixture(t, func(r *http.Request) (int, string) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("missing bearer")
				}
				if r.Method == "GET" {
					if r.URL.Path != "/api/v1/tokens/"+prefix {
						t.Fatalf("metadata route: %s", r.URL)
					}
					return 200, `{"prefix":"` + prefix + `","kind":"` + tc.kind + `","principal":"` + tc.principal + `","scopes":[]}`
				}
				if r.Method == "DELETE" {
					deletes++
					if tc.revoked {
						return 200, `{"prefix":"` + prefix + `","revoked":true}`
					}
					return 200, `{"prefix":"` + prefix + `","revoked":false}`
				}
				t.Fatalf("unexpected request: %s", r.Method)
				return 500, ""
			})
			var result error
			out := captureStdout(t, func() { result = runProfiles([]string{"remove", "--profile", "prod"}) })
			if (result == nil) != tc.wantRemoved {
				t.Fatalf("removal = %v", result)
			}
			if strings.Contains(out, token) {
				t.Fatal("raw credential in result")
			}
			after, err := profile.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := after.Profiles["prod"]; exists == tc.wantRemoved {
				t.Fatalf("remaining profiles: %#v", after.Profiles)
			}
			if tc.name == "machine" && deletes != 0 {
				t.Fatal("revoked machine credential")
			}
		})
	}
}

func TestProfilesRemoveKeepTokenMakesNoRequest(t *testing.T) {
	path := profilesFixturePath(t)
	withStdin(t, "swu_private\n")
	captureStdout(t, func() {
		if err := runProfiles([]string{"add", "--controller", "https://api.example", "--profile", "prod", "--no-probe", "--token-stdin"}); err != nil {
			t.Fatal(err)
		}
	})
	profileHTTPFixture(t, func(*http.Request) (int, string) { t.Fatal("keep-token contacted controller"); return 500, "" })
	captureStdout(t, func() {
		if err := runProfiles([]string{"remove", "--profile", "prod", "--keep-token"}); err != nil {
			t.Fatal(err)
		}
	})
	cfg, err := profile.Load(path)
	if err != nil || len(cfg.Profiles) != 0 {
		t.Fatalf("remove result: %#v %v", cfg, err)
	}
}

func TestProfilesAddRejectsInvalidController(t *testing.T) {
	for _, endpoint := range []string{"api.example", "file:///private", "https://user:private@api.example", "https://api.example?token=private", "https://api.example#private"} {
		path := profilesFixturePath(t)
		err := runProfiles([]string{"add", "--controller", endpoint, "--no-probe"})
		if err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("credential-bearing URL in error")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid controller wrote config: %v", err)
		}
	}
}
