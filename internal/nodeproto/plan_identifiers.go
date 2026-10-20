package nodeproto

import (
	"fmt"
	"sort"
)

// VisitPlanIdentifiers visits the validator's identifier fields and stops on the first visitor error.
func VisitPlanIdentifiers(plan PlanDoc, visit func(value, path string) error) error {
	if err := visitConcurrencyIdentifiers(plan.Concurrency, "plan.concurrency", visit); err != nil {
		return err
	}
	for i, job := range plan.Jobs {
		path := fmt.Sprintf("jobs[%d]", i)
		if err := visitRegisteredJobIdentifiers(job, path, visit); err != nil {
			return err
		}
		for _, list := range []struct {
			values []string
			name   string
		}{
			{job.Needs, "deps"},
			{job.NeedsIfPresent, "deps_if_present"},
			{job.NeedsGenerated, "needs_generated"},
			{job.Groups, "groups"},
			{job.Requires, "requires"},
			{job.Prefers, "prefers"},
			{job.Artifacts, "artifacts"},
		} {
			if err := visitIdentifierList(list.values, path+"."+list.name, visit); err != nil {
				return err
			}
		}
		for _, field := range []struct{ value, name string }{{job.OnFailureOf, "on_failure_of"}, {job.ResultStep, "result_step"}} {
			if field.value != "" {
				if err := visit(field.value, path+"."+field.name); err != nil {
					return err
				}
			}
		}
		for k, cache := range job.Caches {
			if err := visit(cache.Name, fmt.Sprintf("%s.caches[%d].name", path, k)); err != nil {
				return err
			}
		}
		for k, input := range job.Consumes {
			if err := visit(input.Producer, fmt.Sprintf("%s.consumes[%d].producer", path, k)); err != nil {
				return err
			}
		}
		for k, child := range job.Children {
			if err := visit(child.Pipeline, fmt.Sprintf("%s.children[%d].pipeline", path, k)); err != nil {
				return err
			}
		}
		for k, ref := range job.References {
			at := fmt.Sprintf("%s.pipeline_refs[%d]", path, k)
			if err := visit(ref.Pipeline, at+".pipeline"); err != nil {
				return err
			}
			if err := visit(ref.Job, at+".job"); err != nil {
				return err
			}
		}
		if err := visitConcurrencyIdentifiers(job.Concurrency, path+".concurrency", visit); err != nil {
			return err
		}
		names := make([]string, 0, len(job.Env))
		for name := range job.Env {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := visit(name, path+".env"); err != nil {
				return err
			}
		}
		for k, step := range job.Steps {
			at := fmt.Sprintf("%s.steps[%d]", path, k)
			if err := visit(step.ID, at+".id"); err != nil {
				return err
			}
			if err := visitIdentifierList(step.Needs, at+".needs", visit); err != nil {
				return err
			}
			if err := visitIdentifierList(step.Groups, at+".groups", visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func visitIdentifierList(values []string, path string, visit func(string, string) error) error {
	for i, value := range values {
		if err := visit(value, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func visitConcurrencyIdentifiers(specs []ConcurrencySpec, path string, visit func(string, string) error) error {
	for i, spec := range specs {
		if err := visit(spec.Group, fmt.Sprintf("%s[%d].group", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func visitRegisteredJobIdentifiers(job JobSpec, path string, visit func(string, string) error) error {
	if err := visit(job.ID, path+".id"); err != nil {
		return err
	}
	if err := visitClosureIdentifiers(job.Closures, path+".closures", visit); err != nil {
		return err
	}
	for i, step := range job.Steps {
		if err := visitClosureIdentifiers(step.Closures, fmt.Sprintf("%s.steps[%d].closures", path, i), visit); err != nil {
			return err
		}
	}
	return nil
}

func visitClosureIdentifiers(c ClosureSpec, path string, visit func(string, string) error) error {
	for _, field := range []struct {
		values []string
		name   string
	}{
		{c.SkipIf, "skip_if"}, {c.BeforeRun, "before_run"}, {c.AfterRun, "after_run"}, {c.Generate, "generate"},
	} {
		if err := visitIdentifierList(field.values, path+"."+field.name, visit); err != nil {
			return err
		}
	}
	for _, field := range []struct{ value, name string }{{c.Verify, "verify"}, {c.MemoKey, "memo_key"}} {
		if field.value != "" {
			if err := visit(field.value, path+"."+field.name+"[0]"); err != nil {
				return err
			}
		}
	}
	return nil
}
