//go:build unix && rewrite_integration

package controller_test

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
)

func TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards(t *testing.T) {
	burstNativeSDKAcceptance(t, func(t *testing.T, handler http.Handler) (string, []string) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("native SDK HTTP requires loopback sockets: %v", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
		server.Start()
		t.Cleanup(server.Close)
		return server.URL, nil
	})
}
