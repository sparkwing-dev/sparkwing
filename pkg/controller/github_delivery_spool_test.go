package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

func TestGitHubDeliveriesUpToOneMiBNeverWaitForASpool(t *testing.T) {
	s := New(nil, nil).WithGitHubApp(githubapptest.New(t).Config())
	for range cap(s.deliverySpools) {
		s.deliverySpools <- struct{}{}
	}
	deliver := func(body []byte) (*httptest.ResponseRecorder, int, error) {
		r := httptest.NewRequest(http.MethodPost, "/webhooks/github-app", bytes.NewReader(body))
		r.Header.Set("X-Hub-Signature-256", githubapptest.Sign(body))
		w := httptest.NewRecorder()
		got, status, err := s.readSignedGitHubDelivery(w, r)
		if err == nil && !bytes.Equal(got, body) {
			t.Fatalf("delivery body changed: %d bytes", len(got))
		}
		return w, status, err
	}
	small := []byte(`{"zen":"` + strings.Repeat("z", webhookMemoryLimit-10) + `"}`)
	if _, status, err := deliver(small); status != http.StatusOK || err != nil {
		t.Fatalf("1 MiB delivery with every spool busy = %d, %v", status, err)
	}
	large := append(bytes.Clone(small), bytes.Repeat([]byte(" "), 16)...)
	w, status, err := deliver(large)
	if status != http.StatusServiceUnavailable || err == nil || w.Header().Get("Retry-After") == "" {
		t.Fatalf("large delivery with every spool busy = %d, %v, Retry-After %q", status, err, w.Header().Get("Retry-After"))
	}
	for range cap(s.deliverySpools) {
		<-s.deliverySpools
	}
	if _, status, err := deliver(large); status != http.StatusOK || err != nil {
		t.Fatalf("large delivery with a free spool = %d, %v", status, err)
	}
}
