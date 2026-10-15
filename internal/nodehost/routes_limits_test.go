package nodehost_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func openRequest(ctx context.Context, s *server, method, path, bearer, body string, done chan<- int) {
	req, err := http.NewRequestWithContext(ctx, method, s.URL()+path, strings.NewReader(body))
	if err != nil {
		done <- 0
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := s.client.Do(req)
	if err != nil {
		done <- 0
		return
	}
	_ = resp.Body.Close()
	done <- resp.StatusCode
}

func TestChildBodyOverTheLimitIsRefused(t *testing.T) {
	s := start(t)
	service := newService(t)
	service.StartChild = func(context.Context, capability.Authorized, nodehost.ChildRequest) (string, error) {
		t.Fatal("oversized child reached admission")
		return "", nil
	}
	body := `{"pipeline":"build","args":{"a":"` + strings.Repeat("x", 64<<10) + `"}}`
	wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", admitService(t, s, service), body), http.StatusBadRequest, "bad_request")
}
