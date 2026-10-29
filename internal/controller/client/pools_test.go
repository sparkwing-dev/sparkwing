package client

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestPoolManagementUsesFinalRoutes(t *testing.T) {
	calls := 0
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if req.Method != http.MethodGet || req.URL.Path != "/api/v1/pools" {
				t.Fatalf("list: %s %s", req.Method, req.URL)
			}
			return engineResponse(200, `{"pools":[{"name":"prod","runners":["runner-1"],"rules":[{"id":"rule-1","repos":["acme/api"],"branches":["main"],"source":"github","route":true}]}]}`), nil
		case 2:
			if req.Method != http.MethodPost || req.URL.Path != "/api/v1/pools/prod/rules" || req.Header.Get("Idempotency-Key") != "stable" {
				t.Fatalf("allow: %s %s %v", req.Method, req.URL, req.Header)
			}
			var body controllerwire.PoolRuleWrite
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body.Repos, []string{"acme/api", "acme/web"}) || !reflect.DeepEqual(body.Branches, []string{"main", "release/*"}) || body.Source != "github" || !body.Route {
				t.Fatalf("rule: %+v", body)
			}
			return engineResponse(200, `{"pool":"prod","rule":{"id":"rule-2","repos":["acme/api","acme/web"],"branches":["main","release/*"],"source":"github","route":true},"replayed":true}`), nil
		case 3:
			if req.Method != http.MethodDelete || req.URL.Path != "/api/v1/pools/prod/rules/rule-2" {
				t.Fatalf("disallow: %s %s", req.Method, req.URL)
			}
			return engineResponse(200, `{"pool":"prod","rule_id":"rule-2","deleted":false}`), nil
		default:
			t.Fatal("unexpected call")
			return nil, nil
		}
	})
	pools, err := c.ListPools(t.Context())
	if err != nil || len(pools.Pools) != 1 || len(pools.Pools[0].Rules) != 1 {
		t.Fatalf("pools: %+v %v", pools, err)
	}
	rule, err := c.AllowPool(t.Context(), "prod", controllerwire.PoolRuleWrite{Repos: []string{"acme/api", "acme/web"}, Branches: []string{"main", "release/*"}, Source: "github", Route: true, IdempotencyKey: "stable"})
	if err != nil || !rule.Replayed || rule.Rule.ID != "rule-2" {
		t.Fatalf("allow result: %+v %v", rule, err)
	}
	deleted, err := c.DisallowPool(t.Context(), "prod", "rule-2")
	if err != nil || deleted.Deleted || calls != 3 {
		t.Fatalf("disallow result: %+v %v", deleted, err)
	}
}

func TestPoolManagementRejectsMissingAndConflictingData(t *testing.T) {
	for _, body := range []string{`{}`, `{"pools":[{}]}`, `{"pools":[{"name":"prod","rules":[{}]}]}`, `{"pools":[{"name":"prod"},{"name":"prod"}]}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		_, err := c.ListPools(t.Context())
		if err == nil {
			t.Fatalf("invalid list accepted: %s %v", body, err)
		}
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid input reached HTTP"); return nil, io.EOF })
	for _, input := range []controllerwire.PoolRuleWrite{{}, {Repos: []string{"acme/api"}}, {Repos: []string{"acme/api"}, IdempotencyKey: "key", Source: "typed"}, {Repos: []string{""}, IdempotencyKey: "key"}} {
		if _, err := c.AllowPool(t.Context(), "prod", input); err == nil {
			t.Fatalf("invalid rule accepted: %+v", input)
		}
	}
}
