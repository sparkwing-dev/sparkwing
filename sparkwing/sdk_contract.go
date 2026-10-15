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

// Main serves registered pipelines over the node protocol.
func Main() {
	os.Exit(sdkserve.Main(sdkserve.Config{SDKVersion: sdkVersion(), Operations: sdkOperations{}}))
}

// MemoValue separates job-result reuse from dependency file caches.
type MemoValue string

// NoMemo bypasses reuse without treating an empty key as valid.
const NoMemo MemoValue = "memo:none"

// MemoKeyFn evaluates author-owned inputs before the host scopes their key.
type MemoKeyFn func(context.Context) (MemoValue, error)

// MemoOption keeps unset TTL distinct from an explicit duration.
type (
	MemoOption  func(*memoOptions)
	memoOptions struct{ ttl *time.Duration }
)

// MemoKey hashes ordered inputs; callers resolve references before hashing.
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

// MemoTTL declares retention subject to the host's ceiling at plan acceptance.
func MemoTTL(d time.Duration) MemoOption {
	return func(o *memoOptions) { o.ttl = &d }
}

// Memo declares result reuse; missing or corrupt cached bytes produce a miss.
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

// Memo applies one reuse policy to each static or generated group member.
func (g *JobGroup) Memo(key MemoKeyFn, opts ...MemoOption) *JobGroup {
	for _, n := range g.Members() {
		n.Memo(key, opts...)
	}
	return g
}

// Backoff declares retry delay without selecting an execution placement.
func Backoff(d time.Duration) RetryOption {
	return func(c *RetryConfig) { c.Backoff = d; c.backoffSet = true }
}

// Cache declares files restored through the host-authorized depcache scope.
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

// Cache applies dependency-file declarations to every group member.
func (g *JobGroup) Cache(caches ...DirCache) *JobGroup {
	for _, n := range g.Members() {
		n.Cache(caches...)
	}
	return g
}

// Npm requests npm's download cache without introducing a package proxy.
func Npm() DirCache {
	return DirCache{spec: nodeproto.CacheSpec{Name: "npm", Resolver: "npm", KeyFiles: []string{"package-lock.json"}}}
}

// Artifacts distinguishes published files from the job's typed output.
func (n *JobNode) Artifacts(globs ...string) *JobNode {
	n.outputGlobs = append(n.outputGlobs, globs...)
	return n
}

// Artifacts applies published-file patterns to every group member.
func (g *JobGroup) Artifacts(globs ...string) *JobGroup {
	for _, n := range g.Members() {
		n.Artifacts(globs...)
	}
	return g
}

// NeedsIfPresent ignores absent dependencies but preserves failure ordering for present jobs.
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

// NeedsIfPresent applies presence-sensitive ordering to every group member.
func (g *JobGroup) NeedsIfPresent(deps ...Dep) *JobGroup {
	for _, n := range g.Members() {
		n.NeedsIfPresent(deps...)
	}
	return g
}

// GenerateFn reconstructs declarations from persisted source output without the source job's body environment.
type GenerateFn func(context.Context, *Plan) error

// Generator identifies a source expansion whose settlement precedes dependent jobs.
type Generator struct {
	id string
	fn GenerateFn
}

// Generate declares an expansion after source success or memo reuse; the host owns acceptance.
func (n *JobNode) Generate(fn GenerateFn) *Generator {
	if fn == nil {
		panic("sparkwing: Generate: callback required")
	}
	g := &Generator{id: closureID(n.id, "generate", len(n.generators)), fn: fn}
	n.generators = append(n.generators, g)
	return g
}

// ID exposes the stable group identifier used in persisted expansion identity.
func (g *Generator) ID() string {
	return g.id
}

// NeedsGenerated waits for expansion settlement and all generated prerequisites.
func (n *JobNode) NeedsGenerated(generators ...*Generator) *JobNode {
	for _, g := range generators {
		if g == nil {
			panic("sparkwing: NeedsGenerated: nil generator")
		}
		n.needsGenerated = append(n.needsGenerated, g.id)
	}
	return n
}

// NeedsGenerated carries the same expansion barrier to every group member.
func (g *JobGroup) NeedsGenerated(generators ...*Generator) *JobGroup {
	for _, n := range g.Members() {
		n.NeedsGenerated(generators...)
	}
	return g
}

// EnvOption distinguishes process startup settings from body-only environment.
type (
	EnvOption  func(*envOptions)
	envOptions struct{ startup bool }
)

// Startup marks interpreter configuration that must never influence plan declarations.
func Startup() EnvOption {
	return func(o *envOptions) { o.startup = true }
}

// Env defaults to body scope; startup declarations take effect before attempt process creation.
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

// Env applies the same startup/body distinction to all members.
func (g *JobGroup) Env(key, value string, opts ...EnvOption) *JobGroup {
	for _, n := range g.Members() {
		n.Env(key, value, opts...)
	}
	return g
}

// ApprovalConfig preserves indefinite waiting when Timeout is zero.
type ApprovalConfig struct {
	Message   string
	Timeout   time.Duration
	OnTimeout ApprovalTimeoutPolicy
}

// Approval exposes only declarative gate behavior because gates execute no job hooks.
type Approval struct {
	id   string
	node *JobNode
}

// Approval declares a manual gate whose owner enforces waiting and timeout policy.
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

// ID permits gate references without exposing executable-job internals.
func (a *Approval) ID() string {
	return a.id
}

func (a *Approval) depID() string {
	return a.id
}

// Needs waits for prerequisite jobs before requesting a decision.
func (a *Approval) Needs(deps ...Dep) *Approval {
	a.node.Needs(deps...)
	return a
}

// NeedsIfPresent permits plans whose optional prerequisite is absent.
func (a *Approval) NeedsIfPresent(deps ...Dep) *Approval {
	a.node.NeedsIfPresent(deps...)
	return a
}

// NeedsGenerated keeps a gate behind the persisted expansion barrier.
func (a *Approval) NeedsGenerated(generators ...*Generator) *Approval {
	a.node.NeedsGenerated(generators...)
	return a
}

// Optional excludes a denied or expired gate from required run success.
func (a *Approval) Optional() *Approval {
	a.node.optional = true
	return a
}

// ContinueOnError permits dependents after a gate failure.
func (a *Approval) ContinueOnError() *Approval {
	a.node.continueOnError = true
	return a
}

// OnFailure declares and returns a recovery job for a failed gate.
func (a *Approval) OnFailure(id string, body any) *JobNode {
	return a.node.OnFailure(id, body)
}

// FailurePolicy chooses fail_fast or collect_all without changing dependency requirements.
func (w *Work) FailurePolicy(policy ParallelFailurePolicy) *Work {
	switch policy {
	case FailFast, CollectAll:
		w.failures = policy
	default:
		panic("sparkwing: unknown failure policy")
	}
	return w
}

// Annotation carries source location and the same severity vocabulary as log records.
type Annotation struct {
	Level   string
	Message string
	File    string
	Line    int
	Column  int
	EndLine int
}

// Annotate emits a located annotation during execution and panics on invalid input or host write failure.
func Annotate(ctx context.Context, annotation Annotation) {
	sdkserve.RequireExecution(ctx, "sparkwing.Annotate")
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

// Mask returns only after host registration so subsequent output cannot race masking.
func Mask(ctx context.Context, value string) error {
	return sdkserve.ServicesFrom(ctx).Mask(ctx, value)
}

// Variable distinguishes a declared unmasked value from a secret.
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

// MustVariable makes a missing required plain value fail the job.
func MustVariable(ctx context.Context, name string) string {
	v, err := Variable(ctx, name)
	if err != nil {
		panic(err)
	}
	return v
}

// Memoized records successful reuse without pretending the body executed.
const Memoized Outcome = "memoized"

// Checkout distinguishes absent source settings from explicit zero and false values.
type Checkout struct {
	Depth       *int
	FullHistory *bool
	Tags        *bool
	Submodules  *bool
	LFS         *bool
}

// Checkout declares source preferences for host resolution without reading YAML in the SDK.
func (p *Plan) Checkout(config Checkout) *Plan {
	p.source = &config
	return p
}

// SkipIf conditions gate admission without running executable before/after hooks.
func (a *Approval) SkipIf(predicate SkipPredicate, opts ...SkipOption) *Approval {
	a.node.SkipIf(predicate, opts...)
	return a
}
