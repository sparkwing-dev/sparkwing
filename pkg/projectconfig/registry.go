package projectconfig

import (
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// Registry projects validated checkout declarations into pipeline dispatch metadata.
// It resolves project defaults while preserving explicit empty overrides.
func (c *Config) Registry() *pipelines.Config {
	out := &pipelines.Config{}
	if c == nil {
		return out
	}
	for _, p := range c.Pipelines {
		entry := pipelines.Pipeline{Name: p.Name, Entrypoint: p.Entrypoint, Description: value(p.Description), Source: value(p.Source), Profile: value(p.Profile), Hidden: value(p.Hidden), Args: maps.Clone(p.Args), Requires: slices.Clone(p.Requires)}
		guards := p.Guards
		if c.Defaults != nil {
			entry.Args = maps.Clone(c.Defaults.Args)
			if entry.Args == nil && p.Args != nil {
				entry.Args = map[string]string{}
			}
			maps.Copy(entry.Args, p.Args)
			if p.Requires == nil {
				entry.Requires = slices.Clone(c.Defaults.Requires)
			}
			if guards == nil {
				guards = c.Defaults.Guards
			}
		}
		if guards != nil {
			entry.Guards = pipelines.Guards{Require: slices.Clone(guards.When), Reject: slices.Clone(guards.Unless)}
		}
		if p.On != nil {
			entry.On = registryEvents(*p.On)
		}
		out.Pipelines = append(out.Pipelines, entry)
	}
	return out
}

func registryEvents(e Events) pipelines.Triggers {
	out := pipelines.Triggers{}
	if e.Push != nil {
		out.Push = &pipelines.PushTrigger{Branches: slices.Clone(e.Push.Branches), Paths: slices.Clone(e.Push.Paths)}
	}
	if e.PullRequest != nil {
		out.PullRequest = &pipelines.PullRequestTrigger{Branches: slices.Clone(e.PullRequest.Branches), Actions: slices.Clone(e.PullRequest.Actions)}
	}
	if e.PreCommit != nil {
		out.PreHook = &pipelines.PreHookTrigger{}
	}
	if e.PrePush != nil {
		out.PostHook = &pipelines.PostHookTrigger{}
	}
	if e.PostCommit != nil {
		out.PostCommitHook = &pipelines.PostCommitHookTrigger{}
	}
	for _, s := range e.Schedule {
		out.Schedule = append(out.Schedule, pipelines.ScheduleTrigger{Name: value(s.Name), Cron: s.Cron, Where: s.Where, TZ: value(s.TZ), Overlap: value(s.Overlap), CatchUp: value(s.CatchUp), Args: maps.Clone(s.Args)})
	}
	return out
}

func value[T any](p *T) (v T) {
	if p != nil {
		return *p
	}
	return v
}
