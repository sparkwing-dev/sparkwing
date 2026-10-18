package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubDeliverySpoolsRefuseWhenEverySlotIsBusy(t *testing.T) {
	s := New(nil, nil)
	for range cap(s.deliverySpools) {
		s.deliverySpools <- struct{}{}
	}
	w := httptest.NewRecorder()
	_, status, err := s.readSignedGitHubDelivery(w, httptest.NewRequest(http.MethodPost, "/webhooks/github-app", strings.NewReader("{}")))
	if status != http.StatusTooManyRequests || err == nil {
		t.Fatalf("busy spool = %d, %v", status, err)
	}
	if cap(s.deliverySpools) < 1 || cap(s.deliverySpools) > 8 {
		t.Fatalf("spool slots = %d", cap(s.deliverySpools))
	}
}
