package sparkwing

import (
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func milliseconds(d time.Duration) *int64 {
	v := d.Milliseconds()
	if d < 0 && v == 0 {
		v = -1
	}
	return &v
}
func closureID(job, kind string, index int) string { return fmt.Sprintf("%s/%s/%d", job, kind, index) }

func planDocument(p *Plan) nodeproto.PlanDoc {
	p.mu.Lock()
	defer p.mu.Unlock()
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}, Resources: resourceDocument(p.resources)}
	if p.prioritySet {
		v := p.priority
		doc.Priority = &v
	}
	if p.claimWaitSet {
		doc.ClaimWaitMS = milliseconds(p.claimWait)
	}
	if p.admissionClass != "" {
		v := string(p.admissionClass)
		doc.AdmissionClass = &v
	}
	if s := p.source; s != nil {
		doc.Source = &nodeproto.SourceSpec{Depth: s.Depth, FullHistory: s.FullHistory, Tags: s.Tags, Submodules: s.Submodules, LFS: s.LFS}
		if s.FullHistory != nil && *s.FullHistory {
			doc.Source.Depth = nil
		}
	}
	for _, c := range p.planConcurrency {
		doc.Concurrency = append(doc.Concurrency, concurrencyDocument(c.Group, c.Cost))
	}
	for _, n := range p.nodes {
		doc.Jobs = append(doc.Jobs, jobDocument(p, n))
		if n.onFailure != nil {
			recovery := jobDocument(p, n.onFailure)
			recovery.OnFailureOf = n.id
			doc.Jobs = append(doc.Jobs, recovery)
		}
	}
	return doc
}
func resourceDocument(r *ResourceHints) *nodeproto.ResourceSpec {
	if r == nil {
		return nil
	}
	s := &nodeproto.ResourceSpec{}
	if r.coresSet {
		s.Cores = &r.Cores
	}
	if r.memorySet {
		s.MemoryBytes = &r.MemoryBytes
	}
	return s
}
func concurrencyDocument(g *ConcurrencyGroup, cost int) nodeproto.ConcurrencySpec {
	l := g.limit
	scope := l.Scope
	if scope == "" {
		scope = ScopeGlobal
	}
	policy := l.OnLimit
	if policy == "" {
		policy = Queue
	}
	return nodeproto.ConcurrencySpec{Group: g.name, Scope: string(scope), Capacity: l.Capacity, Cost: cost, OnLimit: string(policy), QueueTimeoutMS: milliseconds(l.QueueTimeout), CancelTimeoutMS: milliseconds(l.CancelTimeout)}
}
func jobDocument(p *Plan, n *JobNode) nodeproto.JobSpec {
	s := nodeproto.JobSpec{ID: n.id, Needs: n.needs, NeedsIfPresent: n.needsOptional, NeedsGenerated: n.needsGenerated, Groups: jobGroups(p, n), Env: n.declaredEnv, Artifacts: n.outputGlobs, Requires: append(append([]string(nil), n.requires...), n.tools...), Prefers: n.prefers, Inline: n.inline, Optional: n.optional, ContinueOnError: n.continueOnError, Resources: resourceDocument(n.resources)}
	for _, e := range n.consumes {
		s.Consumes = append(s.Consumes, nodeproto.ArtifactInput{Producer: e.producer, Into: e.into})
	}
	for _, r := range n.pipelineRefs() {
		s.References = append(s.References, nodeproto.PipelineReference{Pipeline: r.Pipeline, Job: r.NodeID})
	}
	for _, c := range n.dirCaches {
		s.Caches = append(s.Caches, c.spec)
	}
	if n.retrySet {
		s.Retry = &nodeproto.RetrySpec{Attempts: n.retryAttempts}
		if n.retryBackoffSet {
			s.Retry.BackoffMS = milliseconds(n.retryBackoff)
		}
	}
	if n.timeoutSet {
		s.TimeoutMS = milliseconds(n.timeout)
	}
	if n.noProgressSet {
		s.NoProgressTimeoutMS = milliseconds(n.noProgressTimeout)
	}
	if n.memoKey != nil {
		s.Memo = &nodeproto.MemoSpec{}
		if n.memoOptions.ttl != nil {
			s.Memo.TTLMS = milliseconds(*n.memoOptions.ttl)
		}
		s.Closures.MemoKey = closureID(n.id, "memo", 0)
	}
	if n.concurrency != nil {
		s.Concurrency = []nodeproto.ConcurrencySpec{concurrencyDocument(n.concurrency.group, n.concurrency.cost)}
	}
	for i := range n.skipIf {
		s.Closures.SkipIf = append(s.Closures.SkipIf, closureID(n.id, "skip", i))
	}
	if n.skipBudgetSet {
		s.SkipIfBudgetMS = milliseconds(n.skipIfTimeout)
	}
	for i := range n.beforeRun {
		s.Closures.BeforeRun = append(s.Closures.BeforeRun, closureID(n.id, "before", i))
	}
	for i := range n.afterRun {
		s.Closures.AfterRun = append(s.Closures.AfterRun, closureID(n.id, "after", i))
	}
	if n.verify != nil {
		s.Closures.Verify = closureID(n.id, "verify", 0)
	}
	for _, g := range n.generators {
		s.Closures.Generate = append(s.Closures.Generate, g.id)
	}
	if a := n.approvalSpec; a != nil {
		s.Approval = &nodeproto.ApprovalSpec{Message: a.Message, TimeoutMS: milliseconds(a.Timeout), OnTimeout: string(a.OnTimeout)}
		return s
	}
	if n.work == nil {
		return s
	}
	s.FailurePolicy = string(failurePolicy(n.work))
	for _, step := range n.work.steps {
		d := nodeproto.StepSpec{ID: step.id, Needs: step.needs, Finally: step.finally, Risks: step.risks, Optional: step.optional, ContinueOnError: step.continueOnError, HasDryRun: step.dryRunFn != nil, SafeWithoutDryRun: step.safeWithoutDryRun}
		for _, g := range n.work.groups {
			for _, member := range g.members {
				if member == step {
					d.Groups = append(d.Groups, g.name)
				}
			}
		}
		for i := range step.skipIf {
			d.Closures.SkipIf = append(d.Closures.SkipIf, closureID(n.id, "step/"+step.id+"/skip", i))
		}
		s.Steps = append(s.Steps, d)
	}
	if n.resultStep != nil {
		s.ResultStep = n.resultStep.id
	}
	return s
}

func jobGroups(p *Plan, n *JobNode) []string {
	var groups []string
	for _, g := range p.groups {
		for _, member := range g.members {
			if member == n && g.name != "" {
				groups = append(groups, g.name)
			}
		}
	}
	return groups
}
func failurePolicy(w *Work) ParallelFailurePolicy {
	if w.failures == "" {
		return FailFast
	}
	return w.failures
}
func planJob(p *Plan, id string) *JobNode {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := p.byID[id]; n != nil {
		return n
	}
	for _, n := range p.nodes {
		if n.onFailure != nil && n.onFailure.id == id {
			return n.onFailure
		}
	}
	return nil
}
