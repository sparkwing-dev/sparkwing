package cluster

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/testhome"
)

func TestWaitForDockerNeedsAUsableSocket(t *testing.T) {
	socket := filepath.Join(testhome.TempDir(t), "docker.sock")
	if err := waitForDocker(t.Context(), "unix://"+socket, 0); err == nil {
		t.Fatal("a missing Docker socket counted as ready")
	}
	ready, rejected := make(chan struct{}), make(chan struct{})
	var rejectedOnce sync.Once
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ready:
			_, _ = w.Write([]byte("OK"))
		default:
			rejectedOnce.Do(func() { close(rejected) })
			http.Error(w, "starting", http.StatusServiceUnavailable)
		}
	})}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	served, finished := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(served); _ = server.Serve(listener) }()
	go func() { defer close(finished); result <- waitForDocker(ctx, "unix://"+socket, 10*time.Second) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); <-served; <-finished })
	select {
	case <-rejected:
	case <-ctx.Done():
		t.Fatal("Docker readiness never reached the unhealthy daemon")
	}
	select {
	case err := <-result:
		t.Fatalf("readiness returned before the daemon became healthy: %v", err)
	default:
	}
	close(ready)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("a daemon that became healthy was refused: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the healthy daemon never became ready")
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
