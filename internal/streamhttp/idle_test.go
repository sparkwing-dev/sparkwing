package streamhttp

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/net/http2"
)

func TestWriterAllowsHTTP2IdleIntervals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		defer client.Close()
		done := make(chan struct{})
		base := &http.Server{WriteTimeout: 20 * time.Millisecond}
		h2 := &http2.Server{}
		if err := http2.ConfigureServer(base, h2); err != nil {
			t.Fatal(err)
		}
		go func() {
			defer close(done)
			defer server.Close()
			h2.ServeConn(server, &http2.ServeConnOpts{
				BaseConfig: base,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					out, err := NewWriter(w, 20*time.Millisecond)
					if err != nil {
						t.Error(err)
						return
					}
					// HTTP/2 aborts an idle stream when its write deadline expires.
					idle := func() bool {
						select {
						case <-time.After(60 * time.Millisecond):
							return true
						case <-r.Context().Done():
							return false
						}
					}
					if !idle() {
						return
					}
					for _, line := range []string{": open\n\n", "data: late\n\n"} {
						if _, err := io.WriteString(out, line); err != nil {
							t.Error(err)
							return
						}
						if err := out.Flush(); err != nil {
							t.Error(err)
							return
						}
						if !idle() {
							return
						}
					}
				}),
			})
		}()
		transport := &http2.Transport{}
		conn, err := transport.NewClientConn(client)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://stream.test/", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := conn.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.ProtoMajor != 2 {
			t.Fatal(resp.Proto)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil || !strings.Contains(string(body), "late") {
			t.Fatalf("idle stream ended: body=%q err=%v", body, err)
		}
		_ = conn.Close()
		<-done
	})
}
