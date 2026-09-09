package streamhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriterAllowsHTTP2IdleIntervals(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := NewWriter(w, 20*time.Millisecond)
		if err != nil {
			t.Error(err)
			return
		}
		// safety: HTTP/2 aborts an idle stream when its write deadline expires.
		time.Sleep(60 * time.Millisecond)
		for _, line := range []string{": open\n\n", "data: late\n\n"} {
			if _, err := io.WriteString(out, line); err != nil {
				t.Error(err)
				return
			}
			if err := out.Flush(); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(60 * time.Millisecond)
		}
	}))
	srv.Config.WriteTimeout = 20 * time.Millisecond
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 2 * time.Second
	resp, err := client.Get(srv.URL)
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
}
