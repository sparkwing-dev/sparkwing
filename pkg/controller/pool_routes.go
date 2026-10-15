package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) poolEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/pools", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePools},
		{Method: "POST", Pattern: "/api/v1/pools/{pool}/rules", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePoolRule},
		{Method: "DELETE", Pattern: "/api/v1/pools/{pool}/rules/{rule}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeletePoolRule},
	}
}

func (s *Server) handlePools(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	rules, runners, err := tenant.ReadPoolAccess(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	pools := map[string]*controllerwire.PoolDetail{}
	for pool, names := range runners {
		pools[pool] = &controllerwire.PoolDetail{Name: pool, Runners: names}
	}
	for _, row := range rules {
		if pools[row.Pool] == nil {
			pools[row.Pool] = &controllerwire.PoolDetail{Name: row.Pool}
		}
		pool := pools[row.Pool]
		id, _, _ := strings.Cut(row.ID, ".")
		i := slices.IndexFunc(pool.Rules, func(rule controllerwire.PoolRule) bool { return rule.ID == id })
		if i < 0 {
			pool.Rules = append(pool.Rules, controllerwire.PoolRule{ID: id, Source: poolRuleSource(row.Source), Route: row.Route != ""})
			i = len(pool.Rules) - 1
		}
		rule := &pool.Rules[i]
		if !slices.Contains(rule.Repos, row.RepositoryKey) {
			rule.Repos = append(rule.Repos, row.RepositoryKey)
		}
		if row.Branch != "" && !slices.Contains(rule.Branches, row.Branch) {
			rule.Branches = append(rule.Branches, row.Branch)
		}
	}
	result := controllerwire.Pools{Pools: []controllerwire.PoolDetail{}}
	for _, pool := range pools {
		result.Pools = append(result.Pools, *pool)
	}
	sort.Slice(result.Pools, func(i, j int) bool { return result.Pools[i].Name < result.Pools[j].Name })
	writeJSON(w, http.StatusOK, result)
	return nil
}

func poolRuleSource(source string) string {
	if source == "github_app" {
		return "github"
	}
	return source
}

func (s *Server) handlePoolRule(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	pool := r.PathValue("pool")
	var body controllerwire.PoolRuleWrite
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if err := controllerwire.ValidateQueueGroupName(pool); err != nil {
		return invalidEngineBody("invalid pool")
	}
	if key == "" || len(body.Repos) == 0 || !slices.Contains([]string{"", "github", "local"}, body.Source) {
		return invalidEngineBody("pool rule requires repositories, valid source and idempotency key")
	}
	for _, branch := range body.Branches {
		if branch == "" {
			return invalidEngineBody("empty branch selector")
		}
		if _, err := path.Match(branch, ""); err != nil {
			return invalidEngineBody("invalid branch selector")
		}
	}
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	source, route := body.Source, ""
	if source == "github" {
		source = "github_app"
	}
	if body.Route {
		route = pool
	}
	branches := body.Branches
	if len(branches) == 0 {
		branches = []string{""}
	}
	var rows []store.PoolAccessRule
	repos := []string{}
	for _, repo := range body.Repos {
		if s.githubApp == nil || s.githubApp.client == nil {
			return missingEndpointPolicy()
		}
		installation, verified, err := s.resolveGitHubAppRepository(r.Context(), tenant, repo)
		if err != nil {
			return engineLookupError(err)
		}
		repoKey := "gh:" + strconv.FormatInt(verified.ID, 10)
		if slices.Contains(repos, repoKey) {
			return invalidEngineBody("repeated repository")
		}
		repos = append(repos, repoKey)
		for _, branch := range branches {
			rows = append(rows, store.PoolAccessRule{Pool: pool, RepositoryKey: repoKey, InstallationID: installation.InstallationID, Branch: branch, Source: source, Route: route})
		}
	}
	encoded, _ := json.Marshal([]string{a.Target.Team, pool, key})
	digest := sha256.Sum256(encoded)
	id := hex.EncodeToString(digest[:])
	replayed, err := tenant.WritePoolAccess(r.Context(), engineActor(a), id, rows)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.PoolRuleResult{Pool: pool, Rule: controllerwire.PoolRule{ID: id, Repos: repos, Branches: body.Branches, Source: body.Source, Route: body.Route}, Replayed: replayed})
	return nil
}

func (s *Server) handleDeletePoolRule(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	pool, id := r.PathValue("pool"), r.PathValue("rule")
	deleted, err := tenant.DeletePoolAccess(r.Context(), engineActor(a), pool, id)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.PoolRuleDeletion{Pool: pool, RuleID: id, Deleted: deleted})
	return nil
}
