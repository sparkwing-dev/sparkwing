package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type expansion struct {
	closure string
	raw     json.RawMessage
	nodes   []nodeproto.JobSpec
	err     error
	// safety: set when err is a recorded batch the generator no longer reproduces; nodes are what it recorded.
	drifted bool
}

type genGroup struct {
	done    bool
	err     error
	members []string
}

// safety: the generators run in the source's own attempt process, after its
// body succeeds, because a generator reads the source's output and the SDK
// holds it in that process; a separate process would read it back through the
// controller, which grants a node only its dependencies' outputs.
func generate(ctx context.Context, sess *Session, n *NodeRun) []expansion {
	if len(n.Plan.Closures.Generate) == 0 {
		return nil
	}
	out := make([]expansion, 0, len(n.Plan.Closures.Generate))
	for _, id := range n.Plan.Closures.Generate {
		raw, err := evalGenerator(ctx, sess, id)
		out = append(out, expansion{closure: id, raw: raw, err: err})
	}
	return out
}

// safety: generated jobs remain declarations; the host derives their stored hashes.
func evalGenerator(ctx context.Context, sess *Session, closure string) (json.RawMessage, error) {
	var res nodeproto.EvalResult
	if err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpEval, Closure: closure}, &res); err != nil {
		return nil, err
	}
	return evalValue(res, "generate")
}

func validateExpansions(run *Run, source string, existing []nodeproto.JobSpec, expansions []expansion) ([]expansion, error) {
	var groups []string
	for _, job := range existing {
		if job.ID == source {
			groups = job.Closures.Generate
		}
	}
	return validateExpansionBatches(run, source, existing, expansions, groups)
}

func validateExpansionBatches(run *Run, source string, existing []nodeproto.JobSpec, expansions []expansion, groups []string) ([]expansion, error) {
	ordered := make([]expansion, 0, len(groups))
	counts := make([]int, 0, len(groups))
	jobs := []json.RawMessage{}
	for _, group := range groups {
		i := slices.IndexFunc(expansions, func(e expansion) bool { return e.closure == group })
		if i < 0 {
			return nil, invalid("generator %s did not run", group)
		}
		e := expansions[i]
		if e.drifted {
			return expansions, nil
		}
		if e.err != nil {
			return nil, e.err
		}
		raw := e.raw
		if raw == nil {
			var err error
			raw, err = json.Marshal(append([]nodeproto.JobSpec{}, e.nodes...))
			if err != nil {
				return nil, err
			}
		}
		var members []json.RawMessage
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || json.Unmarshal(raw, &members) != nil {
			return nil, invalid("generator %s returned no jobs array", group)
		}
		ordered, counts, jobs = append(ordered, e), append(counts, len(members)), append(jobs, members...)
	}
	body, err := json.Marshal(struct {
		Jobs []json.RawMessage `json:"jobs"`
	}{jobs})
	if err != nil {
		return nil, err
	}
	validation := run.validation
	validation.Expansion, validation.SourceJob = true, source
	validation.ExistingJobs, validation.ExistingConcurrency = existing, run.Doc.Concurrency
	plan, err := nodeproto.ValidatePlan(body, validation)
	if err != nil {
		return nil, err
	}
	offset := 0
	for i, count := range counts {
		ordered[i].nodes = plan.Jobs[offset : offset+count]
		for _, job := range ordered[i].nodes {
			if !slices.Contains(job.Groups, ordered[i].closure) {
				return nil, invalid("generated job %s does not belong to its evaluated generator", job.ID)
			}
		}
		offset += count
	}
	return ordered, nil
}

func replayGeneratorJobs(ctx context.Context, n *NodeRun, closure string, raw json.RawMessage) ([]nodeproto.JobSpec, error) {
	jobs := slices.Clone(n.Run.Doc.Jobs)
	lister, ok := baseState(n.Run.State).(nodeLister)
	if !ok {
		return nil, invalid("generator reconstruction requires persisted declarations")
	}
	rows, err := lister.ListNodes(ctx, n.Run.ID)
	if err != nil {
		return nil, err
	}
	expected := map[string]*store.Node{}
	for _, row := range rows {
		if row == nil || row.RunID != n.Run.ID {
			return nil, executorError(apierr.PlanDrift, "generator reconstruction has a foreign row")
		}
		if row.Generated == nil {
			continue
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Generated.Spec, &job); err != nil || job.ID != row.NodeID || row.SpecHash == "" || jobHash(job) != row.SpecHash {
			return nil, invalid("invalid persisted generator declaration")
		}
		if row.Generated.Generator == closure {
			if expected[job.ID] != nil {
				return nil, executorError(apierr.PlanDrift, "generator reconstruction has duplicate accepted members")
			}
			expected[job.ID] = row
			continue
		}
		jobs = append(jobs, job)
	}
	for _, row := range expected {
		if row.Generated.Batch != len(expected) {
			return nil, executorError(apierr.PlanDrift, "generator replay has an incomplete accepted batch")
		}
	}
	for _, job := range jobs {
		if !slices.Contains(job.Closures.Generate, closure) {
			continue
		}
		validated, err := validateExpansionBatches(n.Run, job.ID, jobs, []expansion{{closure: closure, raw: raw}}, []string{closure})
		if err != nil {
			return nil, err
		}
		if len(validated[0].nodes) != len(expected) {
			return nil, executorError(apierr.PlanDrift, "generator replay changed the accepted member set")
		}
		for _, generated := range validated[0].nodes {
			row := expected[generated.ID]
			if row == nil || row.Generated.Source != job.ID || row.SpecHash != jobHash(generated) {
				return nil, executorError(apierr.PlanDrift, "generator replay changed a persisted declaration")
			}
		}
		return validated[0].nodes, nil
	}
	return nil, invalid("recorded generator is absent from its source")
}

// safety: a generated node's code is in no process until its generators run
// there, each source's before the next, so its attempt runs the chain and
// compares the node the last returns with what the run accepted.
func regenerate(ctx context.Context, sess *Session, n *NodeRun) error {
	var nodes []nodeproto.JobSpec
	for _, closure := range n.Generators {
		raw, err := evalGenerator(ctx, sess, closure)
		if err != nil {
			return err
		}
		if nodes, err = replayGeneratorJobs(ctx, n, closure, raw); err != nil {
			return err
		}
	}
	last := n.Generators[len(n.Generators)-1]
	for _, got := range nodes {
		if got.ID != n.Plan.ID {
			continue
		}
		if jobHash(got) != jobHash(n.Plan) {
			return &apierr.Error{Code: apierr.PlanDrift, Message: fmt.Sprintf(
				"generator %s builds node %s differently than when the run added it; a generator must return the same nodes for the same source output", last, n.Plan.ID)}
		}
		return nil
	}
	return &apierr.Error{Code: apierr.PlanDrift, Message: fmt.Sprintf(
		"generator %s no longer returns node %s; a generator must return the same nodes for the same source output", last, n.Plan.ID)}
}

// safety: every generator of a finished source settles here, whatever its
// outcome, so a node that needs its nodes never waits on a group that cannot finish.
func (s *scheduler) expand(ctx context.Context, source string, res Result) {
	i := slices.IndexFunc(s.nodes, func(n nodeproto.JobSpec) bool { return n.ID == source })
	if i < 0 || len(s.nodes[i].Closures.Generate) == 0 {
		return
	}
	var validationErr error
	if res.Outcome.OK() {
		res.expansions, validationErr = validateExpansions(s.run, source, s.nodes, res.expansions)
		if validationErr != nil {
			s.drifts = append(s.drifts, validationErr)
		}
	}
	for _, id := range s.nodes[i].Closures.Generate {
		j := slices.IndexFunc(res.expansions, func(e expansion) bool { return e.closure == id })
		var err error
		switch {
		case !res.Outcome.OK():
			err = fmt.Errorf("expansion source %q did not succeed (outcome=%s)", source, res.Outcome)
		case validationErr != nil:
			err = validationErr
		case j < 0:
			err = fmt.Errorf("generator %s did not run", id)
		case res.expansions[j].drifted:
			err = res.expansions[j].err
			s.endDrifted(ctx, source, res.expansions[j])
		case res.expansions[j].err != nil:
			err = res.expansions[j].err
		default:
			err = s.addGenerated(ctx, source, res.expansions[j])
		}
		g := s.group(id)
		g.done, g.err = true, err
		if err != nil {
			s.run.Logger.Error("nodehost: expansion failed", "run", s.run.ID, "source", source, "generator", id, "err", err)
		}
	}
}

// safety: a recorded batch the generator no longer reproduces fails the run whatever waits on it, and each node it
// recorded keeps a row, ended as cancelled, so none stays pending or disappears from a retry.
func (s *scheduler) endDrifted(ctx context.Context, source string, e expansion) {
	s.drifts = append(s.drifts, e.err)
	if err := s.add(ctx, context.WithoutCancel(ctx), e.nodes, &expansionFrom{source: source, closure: e.closure}); err != nil {
		s.drifts = append(s.drifts, err)
		return
	}
	for _, n := range e.nodes {
		if !s.started[n.ID] {
			s.terminal(ctx, n.ID, sparkwing.Cancelled, "expansion failed: "+e.err.Error())
		}
	}
}

func (s *scheduler) addGenerated(ctx context.Context, source string, e expansion) error {
	if err := s.recordNodes(ctx, context.WithoutCancel(ctx), e.nodes, &expansionFrom{source: source, closure: e.closure}); err != nil {
		return err
	}
	if s.addedBy == nil {
		s.addedBy, s.sourceOf = map[string]string{}, map[string]string{}
	}
	g := s.group(e.closure)
	children := 0
	for _, n := range e.nodes {
		s.addedBy[n.ID], s.sourceOf[n.ID] = e.closure, source
		if n.OnFailureOf == "" {
			g.members = append(g.members, n.ID)
			children++
		}
	}
	s.run.Logger.Info("nodehost: expansion generated", "run", s.run.ID, "source", source, "generator", e.closure, "nodes", len(e.nodes))
	hostEvent(ctx, s.run, source, "expansion_generated", fmt.Appendf(nil, "%d children", children))
	for _, w := range s.nodes {
		if !slices.Contains(w.NeedsGenerated, e.closure) {
			continue
		}
		deps := append(slices.Clone(w.Needs), w.NeedsIfPresent...)
		for _, id := range w.NeedsGenerated {
			if g := s.groups[id]; g != nil {
				deps = append(deps, g.members...)
			}
		}
		if err := s.run.State.UpdateNodeDeps(context.WithoutCancel(ctx), s.run.ID, w.ID, deps); err != nil {
			s.run.Logger.Warn("nodehost: record generated deps", "run", s.run.ID, "node", w.ID, "err", err)
		}
	}
	return nil
}

func (s *scheduler) group(closure string) *genGroup {
	if s.groups == nil {
		s.groups = map[string]*genGroup{}
	}
	g := s.groups[closure]
	if g == nil {
		g = &genGroup{}
		s.groups[closure] = g
	}
	return g
}

// safety: an on_failure node waits on its parent alone, as in waitsOn.
func (s *scheduler) generatedWaits(n nodeproto.JobSpec, waits []string) (_ []string, ready bool, err error) {
	if n.OnFailureOf != "" {
		return waits, true, nil
	}
	ready = true
	for _, id := range n.NeedsGenerated {
		switch g := s.groups[id]; {
		case g == nil || !g.done:
			ready = false
		case g.err != nil:
			return waits, true, g.err
		default:
			waits = append(waits, g.members...)
		}
	}
	return waits, ready, nil
}

func (s *scheduler) generators(id string) []string {
	var chain []string
	for closure := s.addedBy[id]; closure != ""; closure = s.addedBy[id] {
		chain = append([]string{closure}, chain...)
		id = s.sourceOf[id]
	}
	return chain
}

// safety: a memo hit's copy, outcome cached, adds its generators' nodes as its leader did.
func generatesNodes(n nodeproto.JobSpec, outcome sparkwing.Outcome) bool {
	return len(n.Closures.Generate) > 0 && (outcome == sparkwing.Success || outcome == sparkwing.Memoized)
}

type expansionFrom struct{ source, closure string }

func (f *expansionFrom) record(row *store.Node, n nodeproto.JobSpec, batch int) error {
	if f == nil {
		return nil
	}
	spec, err := json.Marshal(n)
	if err != nil {
		return err
	}
	row.Generated = &store.GeneratedNode{Source: f.source, Generator: f.closure, Batch: batch, Spec: spec}
	// safety: a generated recovery node loads its code with a generator that reads the source's output, which the
	// controller grants only to the source's descendants, so its row descends from its parent as the parent does
	// from the source; the scheduler still waits on the parent alone.
	if p := n.OnFailureOf; p != "" && !slices.Contains(row.Deps, p) {
		row.Deps = append(row.Deps, p)
	}
	return nil
}

type nodeLister interface {
	ListNodes(ctx context.Context, runID string) ([]*store.Node, error)
}

// safety: complete only when every node of the batch has its row, so a partial batch is never taken whole.
type recordedBatch struct {
	expansion
	complete bool
}

// safety: the expansion's membership is the rows the run recorded, so a later host adds every node the run
// accepted, finished or not, and never one a generator would return now. A generator with no batch recorded, as an
// empty one records nothing, or only part of one, as a host stopped mid-add leaves it, is incomplete.
func storedExpansions(ctx context.Context, st storage.StateStoreOld, runID string, n nodeproto.JobSpec) ([]recordedBatch, error) {
	out := make([]recordedBatch, 0, len(n.Closures.Generate))
	var rows []*store.Node
	if lister, ok := baseState(st).(nodeLister); ok {
		var err error
		if rows, err = lister.ListNodes(context.WithoutCancel(ctx), runID); err != nil {
			return nil, fmt.Errorf("read the nodes %s generated: %w", n.ID, err)
		}
	}
	for _, closure := range n.Closures.Generate {
		b := recordedBatch{expansion: expansion{closure: closure}}
		size := 0
		for _, row := range rows {
			g := row.Generated
			if g == nil || g.Source != n.ID || g.Generator != closure {
				continue
			}
			var node nodeproto.JobSpec
			if err := json.Unmarshal(g.Spec, &node); err != nil {
				return nil, fmt.Errorf("read generated node %s: %w", row.NodeID, err)
			}
			b.nodes, size = append(b.nodes, node), g.Batch
		}
		b.complete = len(b.nodes) > 0 && len(b.nodes) == size
		out = append(out, b)
	}
	return out, nil
}

func allRecorded(batches []recordedBatch) bool {
	return !slices.ContainsFunc(batches, func(b recordedBatch) bool { return !b.complete })
}

// safety: each generator's batch stands on its own: a complete one is added as recorded, and only an incomplete
// one is generated, once, from the source's recorded output in an eval process, keeping every node it recorded.
func (s *scheduler) recordedExpansions(ctx context.Context, n nodeproto.JobSpec, batches []recordedBatch, output func() (json.RawMessage, error)) []expansion {
	var regenerated []expansion
	var genErr error
	var incomplete []string
	for _, b := range batches {
		if !b.complete {
			incomplete = append(incomplete, b.closure)
		}
	}
	if len(incomplete) > 0 {
		var out json.RawMessage
		switch {
		case s.regenerate == nil:
			genErr = fmt.Errorf("the nodes %s generated are not all recorded, and this host has no eval process to generate them", n.ID)
		default:
			if out, genErr = output(); genErr == nil {
				// perf: a complete sibling's generator is not run again, so it can neither cost an eval nor abort one.
				only := n
				closures := n.Closures
				closures.Generate = incomplete
				only.Closures = closures
				regenerated = s.regenerate(ctx, &NodeRun{Run: s.run, Plan: only, Generators: s.generators(n.ID)}, out)
			}
		}
	}
	if genErr == nil && len(regenerated) > 0 {
		existing := slices.Clone(s.nodes)
		if !slices.ContainsFunc(existing, func(job nodeproto.JobSpec) bool { return job.ID == n.ID }) {
			existing = append(existing, n)
		}
		// safety: complete batches already passed acceptance and need no second evaluation.
		for _, batch := range batches {
			if batch.complete {
				regenerated = append(regenerated, batch.expansion)
			}
		}
		regenerated, genErr = validateExpansions(s.run, n.ID, existing, regenerated)
	}
	result := make([]expansion, 0, len(batches))
	for _, b := range batches {
		if b.complete {
			result = append(result, b.expansion)
			continue
		}
		result = append(result, reconcileBatch(b, regenerated, genErr))
	}
	return result
}

// safety: a recorded node stays a member, so a regenerated batch that drops or changes one is plan drift, never a
// smaller fan-out.
func reconcileBatch(b recordedBatch, regenerated []expansion, genErr error) expansion {
	e := reconcileRecorded(b, regenerated, genErr)
	if e.err != nil && len(b.nodes) > 0 {
		e.nodes, e.drifted = b.nodes, true
	}
	return e
}

func reconcileRecorded(b recordedBatch, regenerated []expansion, genErr error) expansion {
	e := expansion{closure: b.closure, err: genErr}
	j := slices.IndexFunc(regenerated, func(r expansion) bool { return r.closure == b.closure })
	switch {
	case genErr != nil:
		return e
	case j < 0:
		e.err = fmt.Errorf("generator %s did not run", b.closure)
		return e
	case regenerated[j].err != nil:
		e.err = regenerated[j].err
		return e
	}
	for _, rec := range b.nodes {
		k := slices.IndexFunc(regenerated[j].nodes, func(got nodeproto.JobSpec) bool { return got.ID == rec.ID })
		if k < 0 || jobHash(regenerated[j].nodes[k]) != jobHash(rec) {
			e.err = &apierr.Error{Code: apierr.PlanDrift, Message: fmt.Sprintf(
				"generator %s no longer returns node %s as the run recorded it; a generator must return the same nodes for the same source output",
				b.closure, rec.ID)}
			return e
		}
	}
	e.nodes, e.raw = regenerated[j].nodes, regenerated[j].raw
	return e
}
