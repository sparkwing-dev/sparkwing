package sparkwing

import (
	"context"
	"time"
)

// safety: Main keeps a pipeline executable independent of the controller and store.
func Main() { panic("unimplemented: c3-protocol-sdk") }

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
func MemoKey(parts ...any) MemoValue { panic("unimplemented: c3-protocol-sdk") }

// safety: MemoTTL declares retention subject to the host's ceiling at plan acceptance.
func MemoTTL(d time.Duration) MemoOption { panic("unimplemented: c3-protocol-sdk") }

// safety: Memo declares result reuse; missing or corrupt cached bytes produce a miss.
func (n *JobNode) Memo(key MemoKeyFn, opts ...MemoOption) *JobNode {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: Memo applies one reuse policy to each static or generated group member.
func (g *JobGroup) Memo(key MemoKeyFn, opts ...MemoOption) *JobGroup {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: Backoff declares retry delay without selecting an execution placement.
func Backoff(d time.Duration) RetryOption { panic("unimplemented: c3-protocol-sdk") }

// safety: Cache declares files restored through the host-authorized depcache scope.
func (n *JobNode) Cache(caches ...DirCache) *JobNode { panic("unimplemented: c3-protocol-sdk") }

// safety: Cache applies dependency-file declarations to every group member.
func (g *JobGroup) Cache(caches ...DirCache) *JobGroup { panic("unimplemented: c3-protocol-sdk") }

// safety: Npm requests npm's download cache without introducing a package proxy.
func Npm() DirCache { panic("unimplemented: c3-protocol-sdk") }

// safety: Artifacts distinguishes published files from the job's typed output.
func (n *JobNode) Artifacts(globs ...string) *JobNode { panic("unimplemented: c3-protocol-sdk") }

// safety: Artifacts applies published-file patterns to every group member.
func (g *JobGroup) Artifacts(globs ...string) *JobGroup { panic("unimplemented: c3-protocol-sdk") }

// safety: NeedsIfPresent ignores absent dependencies but preserves failure ordering for present jobs.
func (n *JobNode) NeedsIfPresent(deps ...Dep) *JobNode { panic("unimplemented: c3-protocol-sdk") }

// safety: NeedsIfPresent applies presence-sensitive ordering to every group member.
func (g *JobGroup) NeedsIfPresent(deps ...Dep) *JobGroup { panic("unimplemented: c3-protocol-sdk") }

// safety: GenerateFn reconstructs executable declarations from the source's persisted output.
// Replay must produce the accepted topology hash; a mismatch fails as plan_drift.
type GenerateFn func(context.Context, *Plan) error

// safety: Generator identifies a source expansion whose settlement precedes dependent jobs.
type Generator struct{ id string }

// safety: Generate declares an expansion after source success or memo reuse; the host owns acceptance.
func (n *JobNode) Generate(fn GenerateFn) *Generator { panic("unimplemented: c3-protocol-sdk") }

// safety: ID exposes the stable group identifier used in persisted expansion identity.
func (g *Generator) ID() string { panic("unimplemented: c3-protocol-sdk") }

// safety: NeedsGenerated waits for expansion settlement and all generated prerequisites.
func (n *JobNode) NeedsGenerated(generators ...*Generator) *JobNode {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: NeedsGenerated carries the same expansion barrier to every group member.
func (g *JobGroup) NeedsGenerated(generators ...*Generator) *JobGroup {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: EnvOption distinguishes process startup settings from body-only environment.
type (
	EnvOption  func(*envOptions)
	envOptions struct{ startup bool }
)

// safety: Startup marks interpreter configuration that must never influence plan declarations.
func Startup() EnvOption { panic("unimplemented: c3-protocol-sdk") }

// safety: Env defaults to body scope; startup declarations take effect before attempt process creation.
func (n *JobNode) Env(key, value string, opts ...EnvOption) *JobNode {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: Env applies the same startup/body distinction to all members.
func (g *JobGroup) Env(key, value string, opts ...EnvOption) *JobGroup {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: ApprovalConfig preserves indefinite waiting when Timeout is zero.
type ApprovalConfig struct {
	Message   string
	Timeout   time.Duration
	OnTimeout ApprovalTimeoutPolicy
}

// safety: Approval exposes only declarative gate behavior because gates execute no job hooks.
type Approval struct{ id string }

// safety: Approval declares a manual gate whose owner enforces waiting and timeout policy.
func (p *Plan) Approval(id string, cfg ApprovalConfig) *Approval {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: ID permits gate references without exposing executable-job internals.
func (a *Approval) ID() string    { panic("unimplemented: c3-protocol-sdk") }
func (a *Approval) depID() string { panic("unimplemented: c3-protocol-sdk") }

// safety: Needs waits for prerequisite jobs before requesting a decision.
func (a *Approval) Needs(deps ...Dep) *Approval { panic("unimplemented: c3-protocol-sdk") }

// safety: NeedsIfPresent permits plans whose optional prerequisite is absent.
func (a *Approval) NeedsIfPresent(deps ...Dep) *Approval { panic("unimplemented: c3-protocol-sdk") }

// safety: NeedsGenerated keeps a gate behind the persisted expansion barrier.
func (a *Approval) NeedsGenerated(generators ...*Generator) *Approval {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: Optional excludes a denied or expired gate from required run success.
func (a *Approval) Optional() *Approval { panic("unimplemented: c3-protocol-sdk") }

// safety: ContinueOnError permits dependents after a gate failure.
func (a *Approval) ContinueOnError() *Approval { panic("unimplemented: c3-protocol-sdk") }

// safety: OnFailure declares an executable recovery job separate from the gate.
func (a *Approval) OnFailure(id string, body any) *JobNode { panic("unimplemented: c3-protocol-sdk") }

// safety: FailurePolicy chooses fail_fast or collect_all without changing dependency requirements.
func (w *Work) FailurePolicy(policy ParallelFailurePolicy) *Work {
	panic("unimplemented: c3-protocol-sdk")
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
func Annotate(ctx context.Context, annotation Annotation) { panic("unimplemented: c3-protocol-sdk") }

// safety: Mask returns only after host registration so subsequent output cannot race masking.
func Mask(ctx context.Context, value string) error { panic("unimplemented: c3-protocol-sdk") }

// safety: Variable distinguishes a declared unmasked value from a secret.
func Variable(ctx context.Context, name string) (string, error) {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: MustVariable makes a missing required plain value fail the job.
func MustVariable(ctx context.Context, name string) string { panic("unimplemented: c3-protocol-sdk") }

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
func (p *Plan) Checkout(config Checkout) *Plan { panic("unimplemented: c3-protocol-sdk") }

// safety: SkipIf conditions gate admission without running executable before/after hooks.
func (a *Approval) SkipIf(predicate SkipPredicate, opts ...SkipOption) *Approval {
	panic("unimplemented: c3-protocol-sdk")
}
