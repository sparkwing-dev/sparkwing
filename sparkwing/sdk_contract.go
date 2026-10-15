package sparkwing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

// safety: Main keeps a pipeline executable independent of the controller and store.
func Main() {
	os.Exit(sdkserve.Main(sdkserve.Config{SDKVersion: sdkVersion(), Operations: sdkOperations{}}))
}

// safety: MemoValue separates job-result reuse from dependency file caches.
type MemoValue string

// safety: NoMemo bypasses reuse without treating an empty key as valid.
const NoMemo MemoValue = "memo:none"

// safety: MemoKeyFn evaluates author-owned inputs before the host scopes their key.
type MemoKeyFn func(context.Context) (MemoValue, error)

// safety: MemoOption keeps unset TTL distinct from an explicit duration.
type (
	MemoOption  func(*memoOptions)
	memoOptions struct{ ttl *time.Duration }
)

// safety: MemoKey hashes ordered inputs; callers resolve references before hashing.
func MemoKey(parts ...any) MemoValue {
	for _, p := range parts {
		if v, ok := p.(MemoValue); ok && v == NoMemo {
			return NoMemo
		}
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		panic(fmt.Errorf("sparkwing: MemoKey: %w", err))
	}
	return MemoValue(fmt.Sprintf("%x", sha256.Sum256(raw)))
}

// safety: MemoTTL declares retention subject to the host's ceiling at plan acceptance.
func MemoTTL(d time.Duration) MemoOption {
	return func(o *memoOptions) { o.ttl = &d }
}

// safety: Memo declares result reuse; missing or corrupt cached bytes produce a miss.
func (n *JobNode) Memo(key MemoKeyFn, opts ...MemoOption) *JobNode {
	n.memoKey = key
	n.memoOptions = memoOptions{}
	for _, o := range opts {
		if o != nil {
			o(&n.memoOptions)
		}
	}
	return n
}

// safety: Memo applies one reuse policy to each static or generated group member.
func (g *JobGroup) Memo(key MemoKeyFn, opts ...MemoOption) *JobGroup {
	for _, n := range g.Members() {
		n.Memo(key, opts...)
	}
	return g
}

// safety: Backoff declares retry delay without selecting an execution placement.
func Backoff(d time.Duration) RetryOption {
	return func(c *RetryConfig) { c.Backoff = d; c.backoffSet = true }
}

// safety: Cache declares files restored through the host-authorized depcache scope.
func (n *JobNode) Cache(caches ...DirCache) *JobNode {
	for _, c := range caches {
		if c.spec.Path == "" && c.spec.Resolver == "" {
			panic("sparkwing: Cache: path or resolver required")
		}
		if len(c.spec.KeyFiles) == 0 {
			panic("sparkwing: Cache: key files required")
		}
		n.dirCaches = append(n.dirCaches, c)
	}
	return n
}

// safety: Cache applies dependency-file declarations to every group member.
func (g *JobGroup) Cache(caches ...DirCache) *JobGroup {
	for _, n := range g.Members() {
		n.Cache(caches...)
	}
	return g
}

// safety: Npm requests npm's download cache without introducing a package proxy.
func Npm() DirCache {
	return DirCache{spec: nodeproto.CacheSpec{Name: "npm", Resolver: "npm", KeyFiles: []string{"package-lock.json"}}}
}

// safety: Artifacts distinguishes published files from the job's typed output.
func (n *JobNode) Artifacts(globs ...string) *JobNode {
	n.outputGlobs = append(n.outputGlobs, globs...)
	return n
}

// safety: Artifacts applies published-file patterns to every group member.
func (g *JobGroup) Artifacts(globs ...string) *JobGroup {
	for _, n := range g.Members() {
		n.Artifacts(globs...)
	}
	return g
}

// safety: NeedsIfPresent ignores absent dependencies but preserves failure ordering for present jobs.
func (n *JobNode) NeedsIfPresent(deps ...Dep) *JobNode {
	for _, d := range deps {
		if d == nil {
			panic("sparkwing: NeedsIfPresent: nil dependency")
		}
		if g, ok := d.(*JobGroup); ok {
			for _, m := range g.Members() {
				n.needsOptional = append(n.needsOptional, m.id)
			}
		} else {
			n.needsOptional = append(n.needsOptional, d.depID())
		}
	}
	return n
}

// safety: NeedsIfPresent applies presence-sensitive ordering to every group member.
func (g *JobGroup) NeedsIfPresent(deps ...Dep) *JobGroup {
	for _, n := range g.Members() {
		n.NeedsIfPresent(deps...)
	}
	return g
}

// safety: GenerateFn reconstructs executable declarations from the source's persisted output.
// Replay must produce the accepted topology hash; a mismatch fails as plan_drift.
type GenerateFn func(context.Context, *Plan) error

// safety: Generator identifies a source expansion whose settlement precedes dependent jobs.
type Generator struct {
	id string
	fn GenerateFn
}

// safety: Generate declares an expansion after source success or memo reuse; the host owns acceptance.
func (n *JobNode) Generate(fn GenerateFn) *Generator {
	if fn == nil {
		panic("sparkwing: Generate: callback required")
	}
	g := &Generator{id: closureID(n.id, "generate", len(n.generators)), fn: fn}
	n.generators = append(n.generators, g)
	return g
}

// safety: ID exposes the stable group identifier used in persisted expansion identity.
func (g *Generator) ID() string {
	return g.id
}

// safety: NeedsGenerated waits for expansion settlement and all generated prerequisites.
func (n *JobNode) NeedsGenerated(generators ...*Generator) *JobNode {
	for _, g := range generators {
		if g == nil {
			panic("sparkwing: NeedsGenerated: nil generator")
		}
		n.needsGenerated = append(n.needsGenerated, g.id)
	}
	return n
}

// safety: NeedsGenerated carries the same expansion barrier to every group member.
func (g *JobGroup) NeedsGenerated(generators ...*Generator) *JobGroup {
	for _, n := range g.Members() {
		n.NeedsGenerated(generators...)
	}
	return g
}

// safety: EnvOption distinguishes process startup settings from body-only environment.
type (
	EnvOption  func(*envOptions)
	envOptions struct{ startup bool }
)

// safety: Startup marks interpreter configuration that must never influence plan declarations.
func Startup() EnvOption {
	return func(o *envOptions) { o.startup = true }
}

// safety: Env defaults to body scope; startup declarations take effect before attempt process creation.
func (n *JobNode) Env(key, value string, opts ...EnvOption) *JobNode {
	o := envOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if n.declaredEnv == nil {
		n.declaredEnv = map[string]nodeproto.EnvValue{}
	}
	n.declaredEnv[key] = nodeproto.EnvValue{Value: value, Startup: o.startup}
	return n
}

// safety: Env applies the same startup/body distinction to all members.
func (g *JobGroup) Env(key, value string, opts ...EnvOption) *JobGroup {
	for _, n := range g.Members() {
		n.Env(key, value, opts...)
	}
	return g
}

// safety: ApprovalConfig preserves indefinite waiting when Timeout is zero.
type ApprovalConfig struct {
	Message   string
	Timeout   time.Duration
	OnTimeout ApprovalTimeoutPolicy
}

// safety: Approval exposes only declarative gate behavior because gates execute no job hooks.
type Approval struct {
	id   string
	node *JobNode
}

// safety: Approval declares a manual gate whose owner enforces waiting and timeout policy.
func (p *Plan) Approval(id string, cfg ApprovalConfig) *Approval {
	if err := validateNodeID(id); err != nil {
		panic(err)
	}
	n := &JobNode{id: id, approvalSpec: &cfg}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byID[id] != nil {
		panic("sparkwing: duplicate approval ID")
	}
	p.nodes = append(p.nodes, n)
	p.byID[id] = n
	return &Approval{id: id, node: n}
}

// safety: ID permits gate references without exposing executable-job internals.
func (a *Approval) ID() string {
	return a.id
}

func (a *Approval) depID() string {
	return a.id
}

// safety: Needs waits for prerequisite jobs before requesting a decision.
func (a *Approval) Needs(deps ...Dep) *Approval {
	a.node.Needs(deps...)
	return a
}

// safety: NeedsIfPresent permits plans whose optional prerequisite is absent.
func (a *Approval) NeedsIfPresent(deps ...Dep) *Approval {
	a.node.NeedsIfPresent(deps...)
	return a
}

// safety: NeedsGenerated keeps a gate behind the persisted expansion barrier.
func (a *Approval) NeedsGenerated(generators ...*Generator) *Approval {
	a.node.NeedsGenerated(generators...)
	return a
}

// safety: Optional excludes a denied or expired gate from required run success.
func (a *Approval) Optional() *Approval {
	a.node.optional = true
	return a
}

// safety: ContinueOnError permits dependents after a gate failure.
func (a *Approval) ContinueOnError() *Approval {
	a.node.continueOnError = true
	return a
}

// safety: OnFailure declares an executable recovery job separate from the gate.
func (a *Approval) OnFailure(id string, body any) *JobNode {
	a.node.OnFailure(id, body)
	return a.node.onFailure
}

// safety: FailurePolicy chooses fail_fast or collect_all without changing dependency requirements.
func (w *Work) FailurePolicy(policy ParallelFailurePolicy) *Work {
	switch policy {
	case FailFast, CollectAll:
		w.failures = policy
	default:
		panic("sparkwing: unknown failure policy")
	}
	return w
}

// safety: Annotation carries source location and the same severity vocabulary as log records.
type Annotation struct {
	Level   string
	Message string
	File    string
	Line    int
	Column  int
	EndLine int
}

// safety: Annotate preserves structured location while the host masks and enforces the 128 KiB limit.
func Annotate(ctx context.Context, annotation Annotation) {
	if annotation.Level == "" {
		annotation.Level = "info"
	}
	switch annotation.Level {
	case "debug", "info", "warn", "error":
	default:
		panic("sparkwing: invalid annotation level")
	}
	if annotation.Line < 0 || annotation.Column < 0 || annotation.EndLine < 0 || (annotation.EndLine > 0 && annotation.EndLine < annotation.Line) {
		panic("sparkwing: invalid annotation location")
	}
	attrs := map[string]any{}
	if annotation.File != "" {
		attrs["file"] = annotation.File
	}
	if annotation.Line != 0 {
		attrs["line"] = annotation.Line
	}
	if annotation.Column != 0 {
		attrs["column"] = annotation.Column
	}
	if annotation.EndLine != 0 {
		attrs["end_line"] = annotation.EndLine
	}
	loggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{Level: annotation.Level, Event: "annotation", Msg: annotation.Message, Attrs: attrs}))
}

// safety: Mask returns only after host registration so subsequent output cannot race masking.
func Mask(ctx context.Context, value string) error {
	return sdkserve.ServicesFrom(ctx).Mask(ctx, value)
}

// safety: Variable distinguishes a declared unmasked value from a secret.
func Variable(ctx context.Context, name string) (string, error) {
	v, masked, err := resolveValue(ctx, "Variable", name)
	if err != nil {
		return "", err
	}
	if masked {
		return "", fmt.Errorf("sparkwing: Variable(%q) is a secret", name)
	}
	return v, nil
}

// safety: MustVariable makes a missing required plain value fail the job.
func MustVariable(ctx context.Context, name string) string {
	v, err := Variable(ctx, name)
	if err != nil {
		panic(err)
	}
	return v
}

// safety: Memoized records successful reuse without pretending the body executed.
const Memoized Outcome = "memoized"

// safety: Checkout distinguishes absent source settings from explicit zero and false values.
type Checkout struct {
	Depth       *int
	FullHistory *bool
	Tags        *bool
	Submodules  *bool
	LFS         *bool
}

// safety: Checkout declares source preferences for host resolution without reading YAML in the SDK.
func (p *Plan) Checkout(config Checkout) *Plan {
	p.source = &config
	return p
}

// safety: SkipIf conditions gate admission without running executable before/after hooks.
func (a *Approval) SkipIf(predicate SkipPredicate, opts ...SkipOption) *Approval {
	a.node.SkipIf(predicate, opts...)
	return a
}
