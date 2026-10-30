package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestInventoryClientsFollowCursors(t *testing.T) {
	health, err := json.Marshal(controllerwire.HealthView{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, fields string
		read               func(*engineClient) error
	}{
		{"secrets", "/api/v1/secrets", `"secrets":[]`, func(c *engineClient) error { _, err := c.ListSecrets(t.Context()); return err }},
		{"pools", "/api/v1/pools", `"pools":[]`, func(c *engineClient) error { _, err := c.ListPools(t.Context()); return err }},
		{"runners", "/api/v1/runners", `"runners":[]`, func(c *engineClient) error { _, err := c.ListRunners(t.Context()); return err }},
		{"tokens", "/api/v1/tokens", `"tokens":[]`, func(c *engineClient) error {
			_, err := (&tokenManager{client: c, base: "/api/v1/tokens"}).ListTokens(t.Context(), controllerwire.TokenQuery{IncludeRevoked: true})
			return err
		}},
		{"schedules", "/api/v1/schedules", `"schedules":[],"upcoming":[],"health":` + string(health), func(c *engineClient) error {
			_, err := c.ListSchedules(t.Context(), controllerwire.ScheduleQuery{All: true})
			return err
		}},
		{"steps", "/api/v1/runs/run/steps", `"steps":[]`, func(c *engineClient) error { _, err := c.ReadSteps(t.Context(), "run"); return err }},
		{"approvals", "/api/v1/approvals", `"approvals":[]`, func(c *engineClient) error { _, err := c.ListApprovals(t.Context(), ""); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var filters string
			c := testEngine(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer private-capability" {
					t.Fatalf("wrong authenticated list: %s", r.URL)
				}
				query := r.URL.Query()
				cursor := query.Get("cursor")
				query.Del("cursor")
				if calls == 1 {
					filters = query.Encode()
					return engineResponse(200, `{`+tc.fields+`,"next_cursor":"next/a+b","truncated":true}`), nil
				}
				if calls != 2 || cursor != "next/a+b" || query.Encode() != filters {
					t.Fatalf("cursor or filters changed: %s", r.URL)
				}
				return engineResponse(200, `{`+tc.fields+`,"next_cursor":"","truncated":false}`), nil
			})
			if err := tc.read(c); err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestStepsDrainPreservesAllRowsAndRejectsCursorCycles(t *testing.T) {
	calls := 0
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		calls++
		body := map[string]any{
			"steps":       []controllerwire.StepDetail{{JobID: "build", ID: "step-" + strings.Repeat("x", calls), Label: "Build", Attempt: 1, Status: "running"}},
			"next_cursor": "next", "truncated": true,
		}
		if calls == 2 {
			body["next_cursor"], body["truncated"] = "", false
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return engineResponse(200, string(data)), nil
	})
	got, err := c.ReadSteps(t.Context(), "run")
	if err != nil || len(got.Steps) != 2 || got.Steps[0].ID != "step-x" || got.Steps[1].ID != "step-xx" {
		t.Fatalf("steps=%+v err=%v", got.Steps, err)
	}
	calls = 0
	c = testEngine(t, func(*http.Request) (*http.Response, error) {
		calls++
		return engineResponse(200, `{"steps":[],"next_cursor":"loop","truncated":true}`), nil
	})
	_, err = c.ReadSteps(t.Context(), "run")
	var failure *apierr.Error
	if calls != 2 || !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
		t.Fatalf("cycle calls=%d err=%v", calls, err)
	}
}

func TestEmptyInventoryRemainsAnArray(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(200, `{"steps":[],"next_cursor":"","truncated":false}`), nil
	})
	result, err := c.ReadSteps(t.Context(), "run")
	if err != nil || result.Steps == nil {
		t.Fatalf("empty steps changed to null: %+v %v", result, err)
	}
}
