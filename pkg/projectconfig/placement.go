package projectconfig

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// RepositoryPlacement resolves a pipeline's placement and job overrides into
// the sealed policy at revision. It returns nil when the pipeline declares
// neither, so the pipeline keeps the routing it has without repository
// placement.
func (c *Config) RepositoryPlacement(pipeline, revision string) (*runspec.RepositoryPlacement, error) {
	if c == nil {
		return nil, nil
	}
	for i, p := range c.Pipelines {
		if p.Name != pipeline {
			continue
		}
		if p.Placement == nil && len(p.Jobs) == 0 {
			return nil, nil
		}
		key := fmt.Sprintf("pipelines[%d]", i)
		out := &runspec.RepositoryPlacement{Revision: revision, Prefer: []runspec.RepositoryTier{}}
		var err error
		if p.Placement != nil {
			if out.Prefer, err = placementTiers(key+".placement", p.Placement); err != nil {
				return nil, err
			}
		}
		for _, job := range slices.Sorted(maps.Keys(p.Jobs)) {
			override := p.Jobs[job].Placement
			if override == nil {
				continue
			}
			tiers, err := placementTiers(key+".jobs."+job+".placement", override)
			if err != nil {
				return nil, err
			}
			out.Jobs = append(out.Jobs, runspec.RepositoryJobPlacement{Job: job, Prefer: tiers})
		}
		return out, nil
	}
	return nil, nil
}

func validatePlacement(key string, p Pipeline, defaults *Defaults) error {
	lists := map[string]*Placement{}
	if p.Placement != nil {
		lists[key+".placement"] = p.Placement
	}
	for job, override := range p.Jobs {
		if strings.TrimSpace(job) == "" || strings.ContainsAny(job, " \t\r\n") {
			return userconfig.Invalid(key+".jobs", "job IDs must be nonempty and contain no whitespace")
		}
		if override.Placement != nil {
			lists[key+".jobs."+job+".placement"] = override.Placement
		}
	}
	route, local := key+".run.route", p.Run != nil && p.Run.Route != nil
	if !local && defaults != nil && defaults.Run != nil && defaults.Run.Route != nil {
		route, local = "defaults.run.route", true
	}
	for _, listKey := range slices.Sorted(maps.Keys(lists)) {
		tiers, err := placementTiers(listKey, lists[listKey])
		if err != nil {
			return err
		}
		for i, tier := range tiers {
			if local && (tier.Target == runspec.PlacementCloud || tier.Target == runspec.PlacementGitHubActions) {
				return userconfig.Invalid(fmt.Sprintf("%s.prefer[%d].target", listKey, i), tier.Target+" conflicts with "+route+": local; remove one of them")
			}
		}
	}
	return nil
}

func placementTiers(key string, p *Placement) ([]runspec.RepositoryTier, error) {
	tiers := make([]runspec.RepositoryTier, 0, len(p.Prefer))
	for i, entry := range p.Prefer {
		at := fmt.Sprintf("%s.prefer[%d]", key, i)
		tier := runspec.RepositoryTier{Target: entry.Target, Labels: entry.Labels}
		if entry.Hold != nil {
			hold, err := time.ParseDuration(*entry.Hold)
			if err != nil || hold < 0 {
				return nil, userconfig.Invalid(at+".hold", "expected a nonnegative duration such as 5s")
			}
			tier.Hold = hold
		}
		if entry.Workflow != nil {
			tier.Workflow = *entry.Workflow
			if !validWorkflowPath(tier.Workflow) {
				return nil, userconfig.Invalid(at+".workflow", "expected a workflow file under .github/workflows/")
			}
		}
		if entry.Runner != nil {
			tier.Runner = *entry.Runner
		}
		if err := runspec.ValidateRepositoryTiers(append(slices.Clone(tiers), tier)); err != nil {
			return nil, userconfig.Invalid(at, err.Error())
		}
		tiers = append(tiers, tier)
	}
	if err := runspec.ValidateRepositoryTiers(tiers); err != nil {
		return nil, userconfig.Invalid(key+".prefer", err.Error())
	}
	return tiers, nil
}

func validWorkflowPath(name string) bool {
	dir, file := path.Split(name)
	return dir == ".github/workflows/" && (strings.HasSuffix(file, ".yaml") || strings.HasSuffix(file, ".yml")) && path.Clean(name) == name
}
