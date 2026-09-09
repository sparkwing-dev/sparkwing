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

func (w pipeResponse) FlushError() error {
	_, err := w.Conn.Write([]byte("buffered event"))
	return err
}

func TestWriterBoundsStalledReader(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	out, err := NewWriter(pipeResponse{httptest.NewRecorder(), server}, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = out.Write([]byte("unread event"))
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("write error = %v, want timeout", err)
	}
}

func TestFlushBoundsStalledReader(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	out, err := NewWriter(pipeResponse{httptest.NewRecorder(), server}, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	err = out.Flush()
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("flush error = %v, want timeout", err)
	}
}
