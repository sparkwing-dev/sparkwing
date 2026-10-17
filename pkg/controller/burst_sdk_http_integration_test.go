//go:build unix && rewrite_integration

package controller_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards(t *testing.T) {
	burstNativeSDKAcceptance(t, func(t *testing.T, handler http.Handler) (string, []string) {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server.URL, nil
	})
}
