package client

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestClaimRetainsIdlePollAdvice(t *testing.T) {
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get(controllerwire.RunnerMaxConcurrentHeader); got != "2" {
			t.Errorf("configured capacity header = %q", got)
		}
		response := engineResponse(http.StatusOK, `{}`)
		response.Header.Set(controllerwire.ClaimPollAfterHeader, "5")
		return response, nil
	})
	result, err := c.Claim(context.Background(), controllerwire.ClaimRequest{MaxConcurrent: 2, Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil || result.PollAfter != 5*time.Second {
		t.Fatalf("poll advice = %+v, %v", result, err)
	}
}
