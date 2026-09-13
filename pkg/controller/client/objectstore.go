package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ObjectStoreClassState is one object-store request class's counters and
// trip state on the controller process.
type ObjectStoreClassState struct {
	Class         string    `json:"class"`
	PerMinute     int       `json:"per_minute"`
	PerDay        int       `json:"per_day"`
	MinuteUsed    int       `json:"minute_used"`
	DayUsed       int       `json:"day_used"`
	Allowed       uint64    `json:"allowed_total"`
	Refused       uint64    `json:"refused_total"`
	Trips         uint64    `json:"trips_total"`
	Tripped       bool      `json:"tripped"`
	TrippedAt     time.Time `json:"tripped_at,omitzero"`
	TrippedWindow string    `json:"tripped_window,omitempty"`
}

// ObjectStoreBreaker is the controller's object-store request budget.
type ObjectStoreBreaker struct {
	Enabled bool                    `json:"enabled"`
	Reset   string                  `json:"reset"`
	Tripped bool                    `json:"tripped"`
	Classes []ObjectStoreClassState `json:"classes"`
	Cleared []string                `json:"cleared,omitempty"`
}

// ObjectStoreBreakerState reads the controller's object-store request
// budget without changing it.
func (c *Client) ObjectStoreBreakerState(ctx context.Context) (*ObjectStoreBreaker, error) {
	return c.objectStoreBreaker(ctx, http.MethodGet, "/api/v1/object-store/breaker")
}

// ResetObjectStoreBreaker clears every tripped class on the controller
// and returns the budget as it stands afterwards, with Cleared naming
// the classes that were refusing requests.
func (c *Client) ResetObjectStoreBreaker(ctx context.Context) (*ObjectStoreBreaker, error) {
	return c.objectStoreBreaker(ctx, http.MethodPost, "/api/v1/object-store/reset-breaker")
}

func (c *Client) objectStoreBreaker(ctx context.Context, method, path string) (*ObjectStoreBreaker, error) {
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("%s%s", c.baseURL, path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out ObjectStoreBreaker
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
