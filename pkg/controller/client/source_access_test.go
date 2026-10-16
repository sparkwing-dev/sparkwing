package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type sourceAccessTransport struct {
	call func(*http.Request) (*http.Response, error)
}

func (s sourceAccessTransport) RoundTrip(r *http.Request) (*http.Response, error) { return s.call(r) }

func FuzzSourceAccessBindsSealedRepository(f *testing.F) {
	f.Add("repo", "https://github.com/team/repo")
	f.Add("other", "https://github.com/other/repo")
	f.Fuzz(func(t *testing.T, repo, remote string) {
		called := false
		access, err := NewSourceAccess(EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "award", nil }, HTTP: &http.Client{Transport: sourceAccessTransport{call: func(r *http.Request) (*http.Response, error) {
			called = true
			if r.Header.Get("Authorization") != "Bearer award" || r.URL.Path != "/api/v1/runs/run/source-credential" {
				t.Fatal("source request lost award binding")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"kind":"ssh","host":"github.com","secret":"private","known_hosts":"confirmed"}`))}, nil
		}}}}, "run", source.Spec{RepoKey: "repo", RemoteURL: "https://github.com/team/repo"})
		if err != nil {
			t.Fatal(err)
		}
		credential, err := access.GitCredential(t.Context(), source.Spec{RepoKey: repo, RemoteURL: remote})
		matches := repo == "repo" && remote == "https://github.com/team/repo"
		if called != matches || (err == nil) != matches {
			t.Fatalf("sealed source relation: called=%v error=%v", called, err)
		}
		if matches && credential.KnownHosts != "confirmed" {
			t.Fatal("source host verification material was lost")
		}
	})
}
