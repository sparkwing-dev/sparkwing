package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestControllerBearerUsesStandardRedirectBoundary(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-origin", true: "foreign-host"}[foreign], func(t *testing.T) {
			var visits atomic.Int32
			receive := func(w http.ResponseWriter, r *http.Request) {
				visits.Add(1)
				want := "Bearer synthetic-private-token"
				if foreign {
					want = ""
				}
				if got := r.Header.Get("Authorization"); got != want {
					t.Errorf("redirected authorization=%q want=%q", got, want)
				}
				_ = json.NewEncoder(w).Encode(Secret{Value: "private-value"})
			}
			target := httptest.NewServer(http.HandlerFunc(receive))
			t.Cleanup(target.Close)
			redirectURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1) + "/value"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/value" {
					receive(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-private-token" {
					t.Error("initial controller request missing bearer")
				}
				if foreign {
					http.Redirect(w, r, redirectURL, http.StatusFound)
				} else {
					http.Redirect(w, r, "/value", http.StatusFound)
				}
			}))
			t.Cleanup(server.Close)
			c := NewWithToken(server.URL, http.DefaultClient, "synthetic-private-token")
			sec, err := c.GetSecretForRun(t.Context(), "TOKEN", "private-run")
			if err != nil || sec.Value != "private-value" {
				t.Fatalf("redirect lookup=%v err=%v", sec, err)
			}
			if visits.Load() != 1 {
				t.Fatalf("redirect visits=%d", visits.Load())
			}
		})
	}
}

func TestControllerUnauthorizedResponseIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "private refusal", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	_, err := NewWithToken(server.URL, nil, "synthetic-invalid-token").GetSecretForRun(context.Background(), "TOKEN", "private-run")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("unauthorized error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unauthorized requests=%d", calls.Load())
	}
}

func TestControllerAuthPreservesCustomClientAndRange(t *testing.T) {
	var redirects atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-private-token" {
			t.Error("API request missing bearer")
		}
		switch r.URL.Path {
		case "/api/v1/secrets/TOKEN":
			http.SetCookie(w, &http.Cookie{Name: "private-fixture", Value: "present", Path: "/"})
			http.Redirect(w, r, "/value", http.StatusFound)
		case "/value":
			if c, err := r.Cookie("private-fixture"); err != nil || c.Value != "present" {
				t.Error("custom cookie jar lost")
			}
			_ = json.NewEncoder(w).Encode(Secret{Value: "private-value"})
		case "/range":
			if r.Header.Get("Range") != "bytes=1-3" {
				t.Error("range header lost")
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("slice"))
		}
	}))
	t.Cleanup(server.Close)
	hc := server.Client()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	hc.Jar = jar
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { redirects.Add(1); return nil }
	c := NewWithToken(server.URL, hc, "synthetic-private-token")
	if _, err := c.GetSecretForRun(t.Context(), "TOKEN", "private-run"); err != nil {
		t.Fatal(err)
	}
	if redirects.Load() != 1 {
		t.Fatalf("custom redirect calls=%d", redirects.Load())
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/range", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=1-3")
	resp, err := c.do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusPartialContent || string(b) != "slice" {
		t.Fatalf("range response status=%d bytes=%q err=%v", resp.StatusCode, b, err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("caller request mutated")
	}
}
