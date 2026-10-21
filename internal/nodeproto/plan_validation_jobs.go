package nodeproto

import (
	"fmt"
	"path/filepath"
)

func (v *planValidator) validateJob(job JobSpec, path string) error {
	if err := v.feature(CapabilityJobs, path); err != nil {
		return err
	}
	for _, ids := range []struct {
		values  []string
		path    string
		feature PlanCapability
	}{
		{job.Needs, path + ".deps", CapabilityDependencies},
		{job.NeedsIfPresent, path + ".deps_if_present", CapabilityOptionalDependencies},
		{job.NeedsGenerated, path + ".needs_generated", CapabilityGeneratedDependencies},
		{job.Groups, path + ".groups", CapabilityGroups},
		{job.Requires, path + ".requires", CapabilityRequires},
		{job.Prefers, path + ".prefers", CapabilityPrefers},
		{job.Artifacts, path + ".artifacts", CapabilityArtifacts},
	} {
		if len(ids.values) > 0 {
			if err := v.feature(ids.feature, ids.path); err != nil {
				return err
			}
		}
		if err := v.identifiers(ids.values, ids.path); err != nil {
			return err
		}
	}
	if job.OnFailureOf != "" {
		if err := v.feature(CapabilityRecovery, path+".on_failure_of"); err != nil {
			return err
		}
		if err := v.identifier(job.OnFailureOf, path+".on_failure_of"); err != nil {
			return err
		}
	}
	if job.Retry != nil {
		if err := v.feature(CapabilityRetry, path+".retry"); err != nil {
			return err
		}
		if job.Retry.Attempts < 0 || job.Retry.BackoffMS != nil && *job.Retry.BackoffMS < 0 {
			return planInvalid(path+".retry", "retry count or backoff is negative")
		}
	}
	for _, duration := range []struct {
		value   *int64
		path    string
		feature PlanCapability
	}{
		{job.TimeoutMS, path + ".timeout_ms", CapabilityTimeout},
		{job.NoProgressTimeoutMS, path + ".no_progress_timeout_ms", CapabilityNoProgressTimeout},
		{job.SkipIfBudgetMS, path + ".skip_if_budget_ms", CapabilitySkipBudget},
	} {
		if duration.value != nil {
			if err := v.feature(duration.feature, duration.path); err != nil {
				return err
			}
			if *duration.value < 0 {
				return planInvalid(duration.path, "duration is negative")
			}
		}
	}
	if job.Memo != nil {
		if err := v.feature(CapabilityMemo, path+".memo"); err != nil {
			return err
		}
		if job.Memo.TTLMS != nil && *job.Memo.TTLMS < 0 {
			return planInvalid(path+".memo.ttl_ms", "duration is negative")
		}
	}
	if (job.Closures.MemoKey != "") != (job.Memo != nil) {
		return planInvalid(path+".memo", "memo policy and memo key must be declared together")
	}
	if len(job.Closures.Generate) > 0 {
		if err := v.feature(CapabilityGenerators, path+".closures.generate"); err != nil {
			return err
		}
	}
	if job.Closures.MemoKey != "" || job.Closures.Verify != "" || len(job.Closures.SkipIf)+len(job.Closures.BeforeRun)+len(job.Closures.AfterRun) > 0 {
		if err := v.feature(CapabilityClosures, path+".closures"); err != nil {
			return err
		}
	}
	if len(job.Caches) > 0 {
		if err := v.feature(CapabilityCaches, path+".caches"); err != nil {
			return err
		}
	}
	cacheNames := map[string]bool{}
	for i, cache := range job.Caches {
		at := fmt.Sprintf("%s.caches[%d]", path, i)
		if err := v.identifier(cache.Name, at+".name"); err != nil {
			return err
		}
		if cacheNames[cache.Name] {
			return planInvalid(at+".name", "duplicate cache name")
		}
		if cache.Resolver == "" && cache.Path != "" && !filepath.IsLocal(filepath.FromSlash(cache.Path)) {
			return planInvalid(at+".path", "cache path must be relative and stay inside the workspace")
		}
		cacheNames[cache.Name] = true
	}
	if len(job.Consumes) > 0 {
		if err := v.feature(CapabilityConsumes, path+".consumes"); err != nil {
			return err
		}
	}
	for i, input := range job.Consumes {
		if err := v.identifier(input.Producer, fmt.Sprintf("%s.consumes[%d].producer", path, i)); err != nil {
			return err
		}
	}
	if len(job.References) > 0 {
		if err := v.feature(CapabilityReferences, path+".pipeline_refs"); err != nil {
			return err
		}
	}
	for i, ref := range job.References {
		at := fmt.Sprintf("%s.pipeline_refs[%d]", path, i)
		if err := v.identifier(ref.Pipeline, at+".pipeline"); err != nil {
			return err
		}
		if err := v.identifier(ref.Job, at+".job"); err != nil {
			return err
		}
	}
	for i, child := range job.Children {
		if err := v.identifier(child.Pipeline, fmt.Sprintf("%s.children[%d].pipeline", path, i)); err != nil {
			return err
		}
	}
	if len(job.Concurrency) > 0 {
		if err := v.feature(CapabilityJobConcurrency, path+".concurrency"); err != nil {
			return err
		}
	}
	if err := v.validateConcurrency(job.Concurrency, path+".concurrency"); err != nil {
		return err
	}
	if job.Inline {
		if err := v.feature(CapabilityInline, path+".inline"); err != nil {
			return err
		}
	}
	for name, env := range job.Env {
		if err := v.identifier(name, path+".env"); err != nil {
			return err
		}
		feature := CapabilityBodyEnv
		if env.Startup || planOneOf(name, "NODE_OPTIONS", "GODEBUG", "GOMAXPROCS") {
			feature = CapabilityStartupEnv
		}
		if err := v.feature(feature, path+".env"); err != nil {
			return err
		}
	}
	if job.Approval != nil {
		if err := v.feature(CapabilityApproval, path+".approval"); err != nil {
			return err
		}
		if len(job.Steps) != 0 || job.ResultStep != "" || job.OnFailureOf != "" || job.Retry != nil || job.Memo != nil || len(job.Caches)+len(job.Artifacts)+len(job.Consumes) > 0 || job.TimeoutMS != nil || job.NoProgressTimeoutMS != nil || job.Inline || job.FailurePolicy != "" || len(job.Closures.Generate) > 0 || len(job.Closures.BeforeRun)+len(job.Closures.AfterRun) > 0 || job.Closures.Verify != "" || job.Closures.MemoKey != "" {
			return planInvalid(path+".approval", "approval conflicts with executable or recovery declarations")
		}
		if len(job.Closures.SkipIf) > 0 {
			if err := v.feature(CapabilityApprovalCondition, path+".closures.skip_if"); err != nil {
				return err
			}
		}
		if job.Approval.TimeoutMS != nil && *job.Approval.TimeoutMS < 0 {
			return planInvalid(path+".approval.timeout_ms", "duration is negative")
		}
		if job.Approval.OnTimeout != "" && !planOneOf(job.Approval.OnTimeout, "fail", "deny", "approve") {
			return planInvalid(path+".approval.on_timeout", "unknown timeout policy")
		}
	} else if len(job.Steps) == 0 {
		return planInvalid(path+".steps", "body job requires a step")
	}
	if job.FailurePolicy != "" {
		if err := v.feature(CapabilityFailurePolicy, path+".failure_policy"); err != nil {
			return err
		}
		if !planOneOf(job.FailurePolicy, "fail_fast", "collect_all") {
			return planInvalid(path+".failure_policy", "unknown failure policy")
		}
	}
	return v.validateSteps(job, path)
}

func (v *planValidator) validateSteps(job JobSpec, path string) error {
	if len(job.Steps) > 0 {
		if err := v.feature(CapabilitySteps, path+".steps"); err != nil {
			return err
		}
	}
	steps := map[string]StepSpec{}
	for i, step := range job.Steps {
		at := fmt.Sprintf("%s.steps[%d]", path, i)
		if err := v.identifier(step.ID, at+".id"); err != nil {
			return err
		}
		if _, exists := steps[step.ID]; exists {
			return planInvalid(at+".id", "duplicate step ID")
		}
		steps[step.ID] = step
		if err := v.identifiers(step.Needs, at+".needs"); err != nil {
			return err
		}
		if err := v.identifiers(step.Groups, at+".groups"); err != nil {
			return err
		}
		if len(step.Groups) > 0 {
			if err := v.feature(CapabilityGroups, at+".groups"); err != nil {
				return err
			}
		}
		if len(step.Risks) > 0 {
			if err := v.feature(CapabilityRisk, at+".risks"); err != nil {
				return err
			}
		}
		if step.HasDryRun || step.SafeWithoutDryRun {
			if err := v.feature(CapabilityDryRun, at); err != nil {
				return err
			}
		}
		if len(step.Closures.Generate) > 0 || step.Closures.MemoKey != "" {
			return planInvalid(at+".closures", "step closure has an invalid kind")
		}
		if step.Closures.Verify != "" || len(step.Closures.SkipIf)+len(step.Closures.BeforeRun)+len(step.Closures.AfterRun) > 0 {
			if err := v.feature(CapabilityClosures, at+".closures"); err != nil {
				return err
			}
		}
	}
	if job.ResultStep != "" {
		if err := v.identifier(job.ResultStep, path+".result_step"); err != nil {
			return err
		}
		if _, exists := steps[job.ResultStep]; !exists {
			return planInvalid(path+".result_step", "result step is absent")
		}
	}
	for i, step := range job.Steps {
		for k, dependency := range step.Needs {
			if _, exists := steps[dependency]; !exists || dependency == step.ID {
				return planInvalid(fmt.Sprintf("%s.steps[%d].needs[%d]", path, i, k), "step dependency is absent or self-referential")
			}
		}
	}
	return detectCycle(steps, func(id string) []string { return steps[id].Needs }, path+".steps")
}

func detectCycle[T any](vertices map[string]T, edges func(string) []string, path string) error {
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return planInvalid(path, "dependency cycle")
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, next := range edges(id) {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range vertices {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (v *planValidator) validateGraph() error {
	edges := map[string][]string{}
	members := map[string][]string{}
	for _, job := range v.jobs {
		for _, group := range job.Groups {
			if _, generated := v.generators[group]; generated {
				members[group] = append(members[group], job.ID)
			}
		}
	}
	for _, id := range v.orderedJobs() {
		job := v.jobs[id]
		path := v.paths[id]
		for _, list := range []struct {
			values   []string
			path     string
			optional bool
		}{
			{job.Needs, path + ".deps", false},
			{job.NeedsIfPresent, path + ".deps_if_present", true},
		} {
			for i, dependency := range list.values {
				if _, exists := v.jobs[dependency]; !exists {
					if list.optional {
						continue
					}
					return planInvalid(fmt.Sprintf("%s[%d]", list.path, i), "job dependency is absent")
				}
				if dependency == id {
					return planInvalid(fmt.Sprintf("%s[%d]", list.path, i), "self dependency")
				}
				edges[id] = append(edges[id], dependency)
			}
		}
		for i, generator := range job.NeedsGenerated {
			owner, exists := v.generators[generator]
			if !exists || owner == id {
				return planInvalid(fmt.Sprintf("%s.needs_generated[%d]", path, i), "generator is absent or self-referential")
			}
			edges[id] = append(edges[id], owner)
			edges[id] = append(edges[id], members[generator]...)
		}
		if job.OnFailureOf != "" {
			if _, exists := v.jobs[job.OnFailureOf]; !exists || job.OnFailureOf == id {
				return planInvalid(path+".on_failure_of", "recovery source is absent or self-referential")
			}
			edges[id] = append(edges[id], job.OnFailureOf)
		}
		for i, input := range job.Consumes {
			if _, exists := v.jobs[input.Producer]; !exists || input.Producer == id {
				return planInvalid(fmt.Sprintf("%s.consumes[%d].producer", path, i), "artifact producer is absent or self-referential")
			}
			edges[id] = append(edges[id], input.Producer)
		}
	}
	if v.context.Expansion {
		source := v.jobs[v.context.SourceJob]
		counts := map[string]int{}
		allowed := map[string]bool{}
		for _, generator := range source.Closures.Generate {
			allowed[generator] = true
		}
		for _, id := range v.newJobs {
			job := v.jobs[id]
			matched := ""
			for _, group := range job.Groups {
				if allowed[group] {
					if matched != "" {
						return planInvalid(v.paths[id]+".groups", "generated job names multiple source generators")
					}
					matched = group
				}
			}
			if matched == "" {
				return planInvalid(v.paths[id]+".groups", "generated job must name one source generator")
			}
			counts[matched]++
			if counts[matched] > 256 {
				return planInvalid(v.paths[id]+".groups", "generator fan-out exceeds 256 jobs")
			}
			edges[id] = append(edges[id], v.context.SourceJob)
		}
	}
	return detectCycle(v.jobs, func(id string) []string { return edges[id] }, "plan.jobs")
}
