package client

import (
	"context"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"net/http"
	"testing"
	"time"
)

func TestClaimRetainsIdlePollAdvice(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		response := engineResponse(http.StatusOK, `{}`)
		response.Header.Set(store.ClaimPollAfterHeader, "5")
		return response, nil
	})
	result, err := c.Claim(context.Background(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil || result.PollAfter != 5*time.Second {
		t.Fatalf("poll advice = %+v, %v", result, err)
	}
}
