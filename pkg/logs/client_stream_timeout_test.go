package logs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDefaultClientStreamOutlivesRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(100 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		if _, err := io.WriteString(w, "data: late\n\n"); err != nil {
			return
		}
	}))
	defer srv.Close()
	for _, token := range []string{"", "secret"} {
		c := NewClientWithToken(srv.URL, nil, token)
		c.http.Timeout = 20 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		body, err := c.Stream(ctx, "run", "node")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		got, err := io.ReadAll(body)
		body.Close()
		cancel()
		if err != nil || string(got) != "data: late\n\n" {
			t.Errorf("stream = %q, %v; want late event", got, err)
		}
		if c.http.Timeout != 20*time.Millisecond {
			t.Fatal("stream changed ordinary request timeout")
		}
	}
}
