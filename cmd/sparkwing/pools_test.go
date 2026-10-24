package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type poolManager struct {
	input      controllerwire.PoolRuleWrite
	pool, rule string
	err        error
}

func (p *poolManager) ListPools(context.Context) (controllerwire.Pools, error) {
	return controllerwire.Pools{Pools: []controllerwire.PoolDetail{{Name: "prod", Runners: []string{"runner-1"}, Rules: []controllerwire.PoolRule{{ID: "rule-1", Repos: []string{"acme/api"}, Branches: []string{"main"}, Source: "github", Route: true}}}}}, p.err
}

func (p *poolManager) AllowPool(_ context.Context, pool string, input controllerwire.PoolRuleWrite) (controllerwire.PoolRuleResult, error) {
	p.pool, p.input = pool, input
	return controllerwire.PoolRuleResult{Pool: pool, Rule: controllerwire.PoolRule{ID: "rule-2", Repos: input.Repos, Branches: input.Branches, Source: input.Source, Route: input.Route}, Replayed: true}, p.err
}

func (p *poolManager) DisallowPool(_ context.Context, pool, rule string) (controllerwire.PoolRuleDeletion, error) {
	p.pool, p.rule = pool, rule
	return controllerwire.PoolRuleDeletion{Pool: pool, RuleID: rule, Deleted: false}, p.err
}

func TestPoolsCommandsPreserveRulesAndResults(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		inv, err := clisurface.Parse("sparkwing", []string{"pools", "list", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := managePools(t.Context(), inv, &poolManager{}, &out); err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var pool controllerwire.PoolDetail
			if err := json.Unmarshal(out.Bytes(), &pool); err != nil || len(pool.Rules) != 1 || pool.Rules[0].Source != "github" || !pool.Rules[0].Route {
				t.Fatalf("pool JSON: %s %v", out.String(), err)
			}
		} else if format == "plain" && out.String() != "prod\n" {
			t.Fatalf("plain: %q", out.String())
		}
	}
	p := &poolManager{}
	inv, err := clisurface.Parse("sparkwing", []string{"pools", "allow", "--pool", "prod", "--repo", "acme/api,acme/web", "--branch", "main,release/*", "--source", "github", "--route", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := managePools(t.Context(), inv, p, &out); err != nil {
		t.Fatal(err)
	}
	if p.pool != "prod" || p.input.IdempotencyKey == "" || !reflect.DeepEqual(p.input.Repos, []string{"acme/api", "acme/web"}) || !reflect.DeepEqual(p.input.Branches, []string{"main", "release/*"}) || !p.input.Route || !strings.Contains(out.String(), `"replayed":true`) {
		t.Fatalf("allow: %+v %s", p, out.String())
	}
	inv, _ = clisurface.Parse("sparkwing", []string{"pools", "disallow", "--pool", "prod", "--rule", "rule-1", "-o", "json"})
	out.Reset()
	if err := managePools(t.Context(), inv, p, &out); err != nil || p.rule != "rule-1" || !strings.Contains(out.String(), `"deleted":false`) {
		t.Fatalf("disallow: %s %v", out.String(), err)
	}
}

func TestPoolsAuthorizationRefusalPreservesError(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Forbidden, Message: "repository authority required", Details: map[string]any{"reason": "unverified_repo"}}
	inv, _ := clisurface.Parse("sparkwing", []string{"pools", "allow", "--pool", "prod", "--repo", "acme/api"})
	var out bytes.Buffer
	err := managePools(t.Context(), inv, &poolManager{err: failure}, &out)
	if !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("refusal: %s %v", out.String(), err)
	}
}
