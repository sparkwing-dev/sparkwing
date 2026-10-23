package nodeproto

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type planValidator struct {
	context      ValidationContext
	paths        map[string]string
	jobs         map[string]JobSpec
	closures     map[string]string
	generators   map[string]string
	capabilities map[PlanCapability]bool
	concurrency  map[string]concurrencyDefinition
	newJobs      []string
}

type concurrencyDefinition struct {
	capacity int
	scope    string
	onLimit  string
}

func validatePlan(raw json.RawMessage, context ValidationContext) (PlanDoc, error) {
	if context.Secrets == nil {
		return PlanDoc{}, planInvalid("plan", "secret matcher is required")
	}
	plan, err := strictPlanDecode(raw, context.Secrets)
	if err != nil {
		return PlanDoc{}, err
	}
	checkSecret := func(value, path string) error {
		if context.Secrets.ContainsSecret(value) {
			return &apierr.Error{Code: apierr.SecretInIdentifier, Message: fmt.Sprintf("registered secret in identifier at %s", path), Details: map[string]any{"field": path}}
		}
		return nil
	}
	if err := VisitPlanIdentifiers(plan, checkSecret); err != nil {
		return PlanDoc{}, err
	}
	if err := visitConcurrencyIdentifiers(context.ExistingConcurrency, "existing_concurrency", checkSecret); err != nil {
		return PlanDoc{}, err
	}
	for i, job := range context.ExistingJobs {
		if err := visitRegisteredJobIdentifiers(job, fmt.Sprintf("existing_jobs[%d]", i), checkSecret); err != nil {
			return PlanDoc{}, err
		}
		if err := visitConcurrencyIdentifiers(job.Concurrency, fmt.Sprintf("existing_jobs[%d].concurrency", i), checkSecret); err != nil {
			return PlanDoc{}, err
		}
	}
	if plan.Jobs == nil {
		return PlanDoc{}, planInvalid("plan.jobs", "jobs array is required")
	}
	if context.Expansion {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		for key := range fields {
			if key != "jobs" {
				return PlanDoc{}, planInvalid("plan."+key, "expansion may declare jobs only")
			}
		}
	}
	v := &planValidator{
		context: context, paths: map[string]string{}, jobs: map[string]JobSpec{},
		closures: map[string]string{}, generators: map[string]string{},
		capabilities: map[PlanCapability]bool{},
		concurrency:  map[string]concurrencyDefinition{},
	}
	for _, row := range CapabilityTable() {
		if _, exists := v.capabilities[row.Feature]; exists {
			return PlanDoc{}, planInvalid("plan", "duplicate capability row")
		}
		switch context.Placement {
		case PlacementLocal:
			v.capabilities[row.Feature] = row.Local
		case PlacementLocalTriggered:
			v.capabilities[row.Feature] = row.LocalTriggered
		case PlacementRunnerHosted:
			v.capabilities[row.Feature] = row.RunnerHosted
		case PlacementLauncher:
			v.capabilities[row.Feature] = row.Launcher
		case PlacementHere:
			v.capabilities[row.Feature] = row.Here
		case PlacementGitHubActions:
			v.capabilities[row.Feature] = row.GitHubActions
		default:
			return PlanDoc{}, planInvalid("plan", "unknown placement")
		}
	}
	if err := v.validatePlanConcurrency(context.ExistingConcurrency, "existing_concurrency"); err != nil {
		return PlanDoc{}, err
	}
	for i, job := range context.ExistingJobs {
		path := fmt.Sprintf("existing_jobs[%d]", i)
		if err := v.registerJob(job, path); err != nil {
			return PlanDoc{}, err
		}
		if err := v.validateConcurrency(job.Concurrency, path+".concurrency", "job"); err != nil {
			return PlanDoc{}, err
		}
	}
	for i := range plan.Jobs {
		job := &plan.Jobs[i]
		job.Requires = NormalizeLabels(job.Requires)
		job.Prefers = NormalizeLabels(job.Prefers)
	}
	for i, job := range plan.Jobs {
		if err := v.registerJob(job, fmt.Sprintf("jobs[%d]", i)); err != nil {
			return PlanDoc{}, err
		}
		v.newJobs = append(v.newJobs, job.ID)
	}
	if context.Expansion {
		source, ok := v.jobs[context.SourceJob]
		if !ok || context.SourceJob == "" {
			return PlanDoc{}, planInvalid("source_job", "expansion source is absent")
		}
		if len(source.Closures.Generate) == 0 {
			return PlanDoc{}, planInvalid("source_job", "expansion source has no generator")
		}
	}
	if err := v.validatePlanSettings(plan); err != nil {
		return PlanDoc{}, err
	}
	for i, job := range plan.Jobs {
		if err := v.validateJob(job, fmt.Sprintf("jobs[%d]", i)); err != nil {
			return PlanDoc{}, err
		}
	}
	if err := v.validateGraph(); err != nil {
		return PlanDoc{}, err
	}
	return plan, nil
}

func (v *planValidator) feature(feature PlanCapability, path string) error {
	supported, known := v.capabilities[feature]
	if !known || !supported {
		return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: fmt.Sprintf("plan feature %s is unsupported at %s", feature, path), Details: map[string]any{"feature": feature, "placement": v.context.Placement}}
	}
	return nil
}

func (v *planValidator) identifier(value, path string) error {
	if value == "" {
		return planInvalid(path, "identifier is empty")
	}

	return nil
}

func (v *planValidator) identifiers(values []string, path string) error {
	seen := map[string]bool{}
	for i, value := range values {
		at := fmt.Sprintf("%s[%d]", path, i)
		if err := v.identifier(value, at); err != nil {
			return err
		}
		if seen[value] {
			return planInvalid(at, "duplicate identifier")
		}
		seen[value] = true
	}
	return nil
}

func (v *planValidator) registerJob(job JobSpec, path string) error {
	if err := v.identifier(job.ID, path+".id"); err != nil {
		return err
	}
	if _, exists := v.jobs[job.ID]; exists {
		return planInvalid(path+".id", "duplicate job ID")
	}
	v.jobs[job.ID], v.paths[job.ID] = job, path
	if err := v.registerClosures(job.Closures, path+".closures", job.ID); err != nil {
		return err
	}
	for i, step := range job.Steps {
		if err := v.registerClosures(step.Closures, fmt.Sprintf("%s.steps[%d].closures", path, i), job.ID); err != nil {
			return err
		}
	}
	return nil
}

func (v *planValidator) registerClosures(c ClosureSpec, path, jobID string) error {
	for _, entry := range []struct {
		kind string
		ids  []string
	}{
		{"skip_if", c.SkipIf},
		{"before_run", c.BeforeRun},
		{"verify", []string{c.Verify}},
		{"after_run", c.AfterRun},
		{"memo_key", []string{c.MemoKey}},
		{"generate", c.Generate},
	} {
		kind, ids := entry.kind, entry.ids
		for i, id := range ids {
			if id == "" {
				if kind == "verify" || kind == "memo_key" {
					continue
				}
				return planInvalid(fmt.Sprintf("%s.%s[%d]", path, kind, i), "identifier is empty")
			}
			at := fmt.Sprintf("%s.%s[%d]", path, kind, i)
			if err := v.identifier(id, at); err != nil {
				return err
			}
			if _, exists := v.closures[id]; exists {
				return planInvalid(at, "closure ID is not unique")
			}
			v.closures[id] = kind
			if kind == "generate" {
				v.generators[id] = jobID
			}
		}
	}
	return nil
}

func (v *planValidator) validatePlanSettings(plan PlanDoc) error {
	if err := v.feature(CapabilityJobs, "plan.jobs"); err != nil {
		return err
	}
	if len(plan.Concurrency) > 0 {
		if err := v.feature(CapabilityPlanConcurrency, "plan.concurrency"); err != nil {
			return err
		}
	}
	if err := v.validatePlanConcurrency(plan.Concurrency, "plan.concurrency"); err != nil {
		return err
	}
	for _, item := range []struct {
		used bool
		name PlanCapability
		path string
	}{
		{plan.Priority != nil, CapabilityPriority, "plan.priority"},
		{plan.ClaimWaitMS != nil, CapabilityClaimWait, "plan.claim_wait_ms"},
		{plan.AdmissionClass != nil, CapabilityAdmissionClass, "plan.admission_class"},
		{plan.Source != nil, CapabilitySource, "plan.source"},
	} {
		if item.used {
			if err := v.feature(item.name, item.path); err != nil {
				return err
			}
		}
	}
	if plan.ClaimWaitMS != nil && *plan.ClaimWaitMS < 0 {
		return planInvalid("plan.claim_wait_ms", "duration is negative")
	}
	if plan.AdmissionClass != nil && !planOneOf(*plan.AdmissionClass, "critical", "interactive", "normal", "batch") {
		return planInvalid("plan.admission_class", "unknown admission class")
	}
	if plan.Source != nil && plan.Source.Depth != nil && *plan.Source.Depth < 0 {
		return planInvalid("plan.source.depth", "depth is negative")
	}
	return nil
}

func (v *planValidator) validatePlanConcurrency(specs []ConcurrencySpec, path string) error {
	for i, spec := range specs {
		if spec.Scope != nil && *spec.Scope == "box" {
			return planInvalid(fmt.Sprintf("%s[%d].scope", path, i), fmt.Sprintf("run-level concurrency group %q cannot use box scope; use job-level box scope", spec.Group))
		}
	}
	return v.validateConcurrency(specs, path, "plan")
}

func (v *planValidator) validateConcurrency(specs []ConcurrencySpec, path, level string) error {
	for i, spec := range specs {
		at := fmt.Sprintf("%s[%d]", path, i)
		if err := v.identifier(spec.Group, at+".group"); err != nil {
			return err
		}
		if spec.Capacity != nil && *spec.Capacity <= 0 || spec.Cost != nil && *spec.Cost <= 0 {
			return planInvalid(at, "explicit capacity and cost must be positive")
		}
		if spec.Scope != nil && !planOneOf(*spec.Scope, "global", "run", "box") {
			return planInvalid(at+".scope", "unknown scope")
		}
		if spec.OnLimit != nil && !planOneOf(*spec.OnLimit, "queue", "fail", "skip", "cancel_others") {
			return planInvalid(at+".on_limit", "unknown limit policy")
		}
		if spec.Scope != nil && *spec.Scope == "box" {
			if err := v.feature(CapabilityBoxConcurrency, at+".scope"); err != nil {
				return err
			}
		}
		if spec.QueueTimeoutMS != nil && *spec.QueueTimeoutMS < 0 {
			return planInvalid(at, "timeout is negative")
		}
		if v.context.Expansion {
			definition := concurrencyDefinition{capacity: 1, scope: "global", onLimit: "queue"}
			if spec.Capacity != nil {
				definition.capacity = *spec.Capacity
			}
			if spec.Scope != nil {
				definition.scope = *spec.Scope
			}
			if spec.OnLimit != nil {
				definition.onLimit = *spec.OnLimit
			}
			if existing, ok := v.concurrency[level+":"+spec.Group]; ok {
				field := ""
				switch {
				case existing.capacity != definition.capacity:
					field = "capacity"
				case existing.scope != definition.scope:
					field = "scope"
				case existing.onLimit != definition.onLimit:
					field = "on_limit"
				}
				if field != "" {
					return planInvalid(at+"."+field, "concurrency group definition conflicts with another membership")
				}
			}
			v.concurrency[level+":"+spec.Group] = definition
		}
	}
	return nil
}

func planOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func sanitizePlanError(err error, secrets SecretMatcher) error {
	if err == nil || secrets == nil {
		return err
	}
	failure, ok := err.(*apierr.Error)
	if !ok {
		return err
	}
	leaks := secrets.ContainsSecret(failure.Message) || secrets.ContainsSecret(failure.Hint)
	for _, value := range failure.Details {
		if secrets.ContainsSecret(fmt.Sprint(value)) {
			leaks = true
		}
	}
	if !leaks {
		return err
	}
	for _, message := range []string{"Plan validation failed", "Rejected", "Invalid", "!"} {
		if !secrets.ContainsSecret(message) {
			return &apierr.Error{Code: failure.Code, Message: message}
		}
	}
	return &apierr.Error{Code: failure.Code}
}

func (v *planValidator) orderedJobs() []string {
	ids := make([]string, 0, len(v.jobs))
	for id := range v.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
