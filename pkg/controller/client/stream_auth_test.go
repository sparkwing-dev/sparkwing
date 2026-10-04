package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveLogStreamBearerPreservesRedirectBoundary(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-origin", true: "foreign-host"}[foreign], func(t *testing.T) {
			var reads atomic.Int32
			receive := func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				want := "Bearer synthetic-stream-token"
				if foreign {
					want = ""
				}
				if r.Header.Get("Authorization") != want {
					t.Error("stream redirect bearer crossed wrong boundary")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: private-stream\n\n"))
			}
			target := httptest.NewServer(http.HandlerFunc(receive))
			t.Cleanup(target.Close)
			foreignURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1) + "/events"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/events" {
					receive(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-stream-token" {
					http.Error(w, "missing bearer", http.StatusUnauthorized)
					return
				}
				if foreign {
					http.Redirect(w, r, foreignURL, http.StatusFound)
				} else {
					http.Redirect(w, r, "/events", http.StatusFound)
				}
			}))
			t.Cleanup(server.Close)
			hc := server.Client()
			hc.Timeout = time.Nanosecond
			stream, err := NewWithToken(server.URL, hc, "synthetic-stream-token").StreamNodeLiveLog(t.Context(), "run", "node", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			b, err := io.ReadAll(stream)
			if err != nil || string(b) != "data: private-stream\n\n" {
				t.Fatalf("stream bytes=%q err=%v", b, err)
			}
			if reads.Load() != 1 {
				t.Fatalf("stream reads=%d", reads.Load())
			}
			if hc.Timeout != time.Nanosecond {
				t.Fatal("caller client timeout mutated")
			}
		})
	}
}
