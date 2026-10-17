package client

import (
	"context"
	"net/url"
	"reflect"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.PoolManager = (*engineClient)(nil)

// NewPoolManager shares authenticated transport and retry policy with engines.
func NewPoolManager(options EngineOptions) (controllerwire.PoolManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ListPools(ctx context.Context) (controllerwire.Pools, error) {
	var result controllerwire.Pools
	err := c.request(ctx, "GET", "/api/v1/pools", "/api/v1/pools", "", nil, &result)
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, pool := range result.Pools {
		if !validSelector(pool.Name) || seen[pool.Name] {
			return result, poolProtocolError("controller returned missing or repeated pools")
		}
		seen[pool.Name] = true
		rules := map[string]bool{}
		for _, rule := range pool.Rules {
			if !validSelector(rule.ID) || rules[rule.ID] || !validPoolRule(rule.Repos, rule.Branches, rule.Source) {
				return result, poolProtocolError("controller returned an invalid pool rule")
			}
			rules[rule.ID] = true
		}
	}
	return result, nil
}

func (c *engineClient) AllowPool(ctx context.Context, pool string, input controllerwire.PoolRuleWrite) (controllerwire.PoolRuleResult, error) {
	var result controllerwire.PoolRuleResult
	if !validSelector(pool) || !validPoolRule(input.Repos, input.Branches, input.Source) || !validSelector(input.IdempotencyKey) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "pool rule requires a pool, repositories, an optional github source and an idempotency key"}
	}
	err := c.request(ctx, "POST", "/api/v1/pools/{pool}/rules", "/api/v1/pools/"+url.PathEscape(pool)+"/rules", input.IdempotencyKey, input, &result)
	if err == nil && (result.Pool != pool || !validSelector(result.Rule.ID) || !reflect.DeepEqual(result.Rule.Repos, input.Repos) || !sameOptionalStrings(result.Rule.Branches, input.Branches) || result.Rule.Source != input.Source || result.Rule.Route != input.Route) {
		err = poolProtocolError("controller returned a conflicting pool rule")
	}
	return result, err
}

func (c *engineClient) DisallowPool(ctx context.Context, pool, rule string) (controllerwire.PoolRuleDeletion, error) {
	var result controllerwire.PoolRuleDeletion
	if !validSelector(pool) || !validSelector(rule) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "pool deletion requires pool and rule selectors"}
	}
	err := c.request(ctx, "DELETE", "/api/v1/pools/{pool}/rules/{rule}", "/api/v1/pools/"+url.PathEscape(pool)+"/rules/"+url.PathEscape(rule), "", nil, &result)
	if err == nil && (result.Pool != pool || result.RuleID != rule) {
		err = poolProtocolError("controller returned a conflicting pool rule deletion")
	}
	return result, err
}

func validPoolRule(repos, branches []string, source string) bool {
	if len(repos) == 0 || source != "" && source != "github" {
		return false
	}
	for _, values := range [][]string{repos, branches} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
				return false
			}
		}
	}
	return true
}

func sameOptionalStrings(a, b []string) bool {
	return len(a) == 0 && len(b) == 0 || reflect.DeepEqual(a, b)
}
func poolProtocolError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}
