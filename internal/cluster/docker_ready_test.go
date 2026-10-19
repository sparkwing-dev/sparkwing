package cluster

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForDockerNeedsAUsableSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "docker.sock")
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			_, _ = w.Write([]byte("OK"))
		}
	})}
	served := make(chan struct{})
	t.Cleanup(func() { server.Close(); <-served })
	go func() {
		defer close(served)
		time.Sleep(300 * time.Millisecond)
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Error(err)
			return
		}
		_ = server.Serve(listener)
	}()
	if err := waitForDocker(t.Context(), "unix://"+socket, 10*time.Second); err != nil {
		t.Fatalf("a late but usable daemon was refused: %v", err)
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(socket, 0); err != nil {
			t.Fatal(err)
		}
		if err := waitForDocker(t.Context(), "unix://"+socket, 700*time.Millisecond); err == nil {
			t.Fatal("a socket this identity cannot open counted as ready")
		}
	}
	if err := waitForDocker(t.Context(), "tcp://0.0.0.0:2375", time.Second); err == nil {
		t.Fatal("a TCP DOCKER_HOST was accepted")
	}
}
