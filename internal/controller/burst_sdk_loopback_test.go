//go:build unix

package controller_test

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
)

// hack: the Go SDK dials its node host itself, so the fixture host must listen on real loopback.
func burstSDKLoopbackService(t *testing.T, handler http.Handler) (string, []string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("native SDK execution requires loopback sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	return server.URL, nil
}
