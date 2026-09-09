package streamhttp

import (
	"errors"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

type pipeResponse struct {
	*httptest.ResponseRecorder
	net.Conn
}

func (w pipeResponse) Write(p []byte) (int, error) { return w.Conn.Write(p) }

func TestWriterBoundsStalledReader(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	out := NewWriter(pipeResponse{httptest.NewRecorder(), server}, 20*time.Millisecond)
	_, err := out.Write([]byte("unread event"))
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("write error = %v, want timeout", err)
	}
}
