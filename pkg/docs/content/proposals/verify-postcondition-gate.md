# Post-action verification: a `Verify` lifecycle gate (proposal)

**Status:** IMPLEMENTED and shipped — `.Verify(func(ctx) error)` is a
live Job modifier (see the SDK reference). This proposal is retained for
design history only; treat the SDK reference as authoritative.

## What this is, and what it is not

You can already verify a step's result today: hand-roll a check as
another work step, or a downstream node that reads the step's output.
So the selling point is **not** "you can now check health." It is:

> sparkwing can distinguish *the mutation failed* from *the mutation
> completed but its postcondition failed.*

That distinction is the whole feature. A deploy command can exit 0 while
the service comes up unhealthy; a migration can run clean while the
schema didn't advance. "Exit 0" is necessary but not sufficient, and
today sparkwing cannot tell "the command broke" apart from "the command
worked but the result is bad."

### Is `Verify` just another work step?

Almost -- and that "almost" is the justification. **A work step that
fails is indistinguishable from the action failing.** Put a health check
as the last step inside the deploy node and have it error on unhealthy:
the node fails, but sparkwing cannot tell you whether the *deploy* step
or the *check* step failed. Both collapse to "the node failed."

`Verify` is a distinct, labeled lifecycle phase, so that "action failed"
and "action succeeded, postcondition failed" become *forkable*. That
fork is the only thing `Verify` fundamentally buys:

- **If you do not need the fork** (you just want "fail if unhealthy"),
  a work step is genuinely equivalent -- use one. No feature needed.
- **If you need the fork** (the right recovery differs by which failed),
  a work step cannot express it, because it cannot distinguish itself
  from the action.

The output-based alternative (`Result{Healthy bool}` plus a downstream
`SkipIf`) works but costs three things: an unhealthy deploy shows as a
*successful* node, you still need a second mechanism for the crash case,
and you hand-roll the wiring on every DAG. `Verify` is the same fork in
one labeled place with honest node status.

## The gap in the lifecycle

The node lifecycle has a *precondition* gate but no *postcondition* gate:

| Hook | Position | Gates the outcome? |
|---|---|---|
| `BeforeRun` (`plan.go:908`) | before the action | **yes** -- non-nil error fails the node without running it |
| `Retry` / `Timeout` | around the action | -- |
| `AfterRun` (`plan.go:922`) | after the action | **no** -- "its own failure is logged but does not change the node's outcome" |
| `Verify` *(proposed)* | after a successful action | **yes** -- non-nil error fails the node, with stage provenance |

`AfterRun` looks like the partner to `BeforeRun` but is an *observer*:
it can watch the result, not veto it. `Verify` is the missing gating
partner.

Prior art separates verification from recovery cleanly:

| System | Post-action verification | Recovery on failure |
|---|---|---|
| Kubernetes | readiness / liveness probes | `rollout undo` + revision history |
| Helm | `--wait` + chart hooks | `helm rollback <rev>` |
| systemd | `ExecStartPost=` + readiness notify | `Restart=`, `OnFailure=` |
| sparkwing | -- (hand-rolled per DAG) | `OnFailure` (action-failure only) |

## Proposed SDK surface

Core gets phase semantics only. Nothing in this surface knows about
HTTP, auth, probes, or what "unhealthy" means.

```go
// Verify registers a postcondition checked after the node's action
// succeeds. If fn returns a non-nil error the node's outcome is Failed
// (stage StageVerify) -- eligible for Retry and triggering OnFailure,
// exactly as an action failure would. Verify runs per attempt.
func (n *JobNode) Verify(fn func(ctx context.Context) error) *JobNode

// Failure describes why a node terminated unsuccessfully. It is passed
// to an OnFailure recovery callback so recovery can branch on stage.
type Failure struct {
    Stage FailureStage // which lifecycle stage produced the failure
    Err   error        // the action error (StageAction) or verify error (StageVerify)
}

type FailureStage int

const (
    StageAction FailureStage = iota // action exited non-zero or timed out
    StageVerify                     // action completed; the Verify postcondition failed
)

// OnFailure registers a recovery node that runs when this node's outcome
// is Failed. The recovery inherits no dependencies from the parent. The
// argument may be:
//   - a Workable, or a func(ctx) error
//         -- recovery with no failure context
//   - a func(ctx context.Context, f Failure) error
//         -- recovery that branches on f.Stage / f.Err
func (n *JobNode) OnFailure(id string, x any) *JobNode
```

That is the entire core change. There is deliberately **no**
`OnVerifyFailure` sugar (see Decisions): a shortcut spelled
"on verify failure, do X" nudges authors toward "verify failed ->
rollback," which is unsafe -- a verify can fail because the check could
not run, not because the deploy is bad. The single stage-aware
`OnFailure` makes the branch explicit, which is the point.

### Evaluation order (per attempt)

1. `BeforeRun` (first attempt only) -- non-nil error fails the node
   without running, no retry (unchanged).
2. Run the action. Non-nil / timeout -> attempt failed, `StageAction`.
3. Action succeeded -> run `Verify`. Non-nil -> attempt failed,
   `StageVerify`.
4. Both succeed -> attempt success.
5. `Retry` re-runs steps 2--3.
6. On final failure, build `Failure{Stage, Err}` and dispatch the
   recovery node (if outcome is `Failed`). The recovery node performs
   side effects (rollback, alert); it does not un-fail the parent.

## Why the fork is load-bearing: two failures, three outcomes

The two failure stages are not the same situation:

- **Action failed** (`StageAction`). The command died, possibly partway.
  The deploy action is **not transactional**: `docker compose up -d`
  recreates services sequentially, so dying partway leaves a mix -- web
  on the new image, worker on the old, or the old container gone and the
  new one not started. You do not know how far it got, so the safer
  recovery is usually "re-run `up`" (converge forward), not rollback.
- **Verify failed** (`StageVerify`). The command ran to completion; the
  postcondition is what failed.

But "verify failed" itself splits into **two outcomes**, and conflating
them is dangerous:

- **Definitively unhealthy** -- the check ran and the service answered
  "bad." The deploy is the problem. Roll back.
- **Indeterminate** -- the check could not run: auth token expired,
  endpoint unreachable, DNS failed. You *cannot tell* if the deploy is
  healthy. Rolling back here is a self-inflicted outage -- you would
  revert a healthy deploy because your monitoring broke. The rule is:
  do not take destructive action on broken telemetry. Escalate to a
  human instead.

Core does not (and must not) know this distinction -- "auth token
expired" is an HTTP-probe concept. The split lives in the **error
value**: `Verify`'s `func(ctx) error` returns an error the recovery
callback inspects, with classification helpers supplied by the probe
library. The taxonomy divides cleanly across the boundary: core supplies
`Failure.Stage`; the probe library classifies its own `Failure.Err`.

## Where it lives: SDK vs spark library

- **`Verify`, `Failure`, `FailureStage`, `OnFailure` (new shape) belong
  in the sparkwing SDK.** General lifecycle surface, zero deploy/HTTP
  knowledge.
- **The HTTP health probe belongs in `sparks-core/probe`**, a new
  package in the existing optional spark library, sitting next to its
  `deploy`, `kube`, and `gitops` neighbors. The base SDK stays lean;
  teams that do not deploy over HTTP never pull it.

The probe builder produces a `func(ctx) error` for `Verify` and carries
the two field requirements:

```go
// in sparks-core/probe, not the SDK
probe.HTTP(url).
    Method("GET").                              // POST + Body() also supported
    HeaderFunc("X-Service-Token", signToken).   // per-attempt credential provider
    ExpectStatus(200).
    ExpectJSON("status", "ok").
    Retry(30).Interval(2 * time.Second).Timeout(5 * time.Second).
    Check   // a func(ctx context.Context) error
```

- **Custom method, headers, and body, not just a bare GET.** A
  rollback-worthy check often hits an *authenticated* endpoint behind an
  access proxy and returns real datastore data; a public-liveness 200 is
  not enough.
- **Per-attempt credentials.** `HeaderFunc(name, func(ctx) (string,
  error))` is evaluated on each request. A `Retry(30).Interval(2s)` loop
  runs ~60s; a signed token captured once can expire mid-loop, making the
  probe fail on *auth* and triggering a rollback for the wrong reason.
- **Error classification.** The probe returns an error the recovery code
  can classify -- a definitive "unhealthy" response versus an
  indeterminate transport/auth failure (e.g. `probe.Indeterminate(err)
  bool`). This is what lets recovery roll back on the former and
  escalate on the latter.

## Worked example

Minimal -- fork on stage; rollback on a completed-but-unhealthy deploy,
converge forward on a crash:

```go
capture := /* Produces[string]: the currently-deployed tag */
prevTag := sw.RefTo[string](capture)

deploy := sw.Job(plan, "deploy", func(ctx context.Context) error {
        return sw.Bash(ctx, "docker compose pull && docker compose up -d").Run()
    }).
    Needs(capture).
    Verify(probe.HTTP("http://localhost:8080/healthz").
        ExpectJSON("status", "ok").Retry(30).Interval(2 * time.Second).Check)

deploy.OnFailure("recover", func(ctx context.Context, f sw.Failure) error {
    if f.Stage != sw.StageVerify {
        return convergeForward(ctx)              // crash: unknown state, re-run up
    }
    return rollback(ctx, prevTag.Get(ctx))       // completed but unhealthy: safe
})
```

Robust -- also distinguish "unhealthy" from "could not verify":

```go
deploy.OnFailure("recover", func(ctx context.Context, f sw.Failure) error {
    if f.Stage != sw.StageVerify        { return convergeForward(ctx) }   // crash
    if probe.Indeterminate(f.Err)       { return escalate(ctx, f.Err) }   // couldn't tell -> human
    return rollback(ctx, prevTag.Get(ctx))                                // definitively unhealthy
})
```

The rollback reads the pre-deploy tag via `Ref.Get`, which works from a
recovery node: the typed-output store is run-scoped, and because
`capture` is upstream of the failing `deploy`, ordering is guaranteed
(`capture` -> `deploy` -> failure -> recovery), so no extra edge is
needed.

## Caching: cache means cache, always

A cached node skips its action **and** its `Verify` -- there are no side
effects on a cache hit, by definition. Want a fresh check on every run?
Model it as its own, uncached node. This needs no special-case rule and
no build-time refusal: it falls out of "cache means cache," and it kills
the footgun by construction (a cache hit cannot fire a rollback, because
nothing -- including the verify -- runs).

The tradeoff is honest and correct: a cached deploy carries the *past*
verify's blessing, not a fresh one. If you want a live recheck, the rule
forces you to say so by splitting it out -- which is the right way to
model a world-state assertion that must run regardless of caching.

## Rejected alternative: verify-as-node + a conditional-execution engine

An alternative models verification as a plain downstream node and the
recovery branches as separate nodes, gated by a new conditional-edge
family -- `RunIf`, `Failed(node)`, `Succeeded(node)`, `FailureOf(node)`,
`Output(node)`:

```go
verify   := sw.Job(plan, p+"verify", c.Verify).Needs(deploy)
rollback := sw.Job(plan, p+"rollback", c.Rollback).
    Needs(verify).
    RunIf(sw.Failed(verify).And(probe.NotIndeterminate(verify)))
```

It is rejected for this release, for four reasons:

1. **It grows core surface, not shrinks it.** It swaps a small lifecycle
   addition (`Verify` + a `Stage` field on the existing recovery hook)
   for a general conditional-execution engine -- and most of that engine
   duplicates primitives sparkwing already has: `RunIf(Failed(x))` is
   `OnFailure`, `RunIf(!pred)` is `SkipIf`, `Output(x)` is `Ref`. Two
   parallel ways to express the same conditionals.
2. **It fights the established model.** `Retry`, `Timeout`, `OnFailure`,
   `SkipIf`, `BeforeRun`, `AfterRun` are all node-attached lifecycle
   modifiers, not separate nodes. `Verify` joins that family. A
   `RunIf`-everything model is a different (trigger-rule) execution
   philosophy that would sit awkwardly beside -- and partly obsolete --
   the existing modifiers.
3. **It reintroduces dishonest node status.** Verify-as-a-separate-node
   leaves the deploy node showing `Success` while the service is
   unhealthy; downstream `Needs(deploy)` proceeds past a broken deploy
   and the dashboard shows green. Keeping `Verify` on the node means the
   deploy node itself goes `Failed(StageVerify)` -- the whole point.
4. **It loses unit retry.** `deploy.Verify(...).Retry(3)` re-runs the
   action and verify together ("redeploy if it comes up unhealthy");
   re-running a separate verify node cannot re-run the deploy.

The valid kernel in the alternative is *recovery-branch visibility* --
seeing which of rollback / escalate / converge fired. But the recovery
is already a dispatched node with its own logs and status; only the
branch decision is in a closure. Making each branch its own node is a
possible later enhancement that does not require the conditional engine
and is weighed separately. Unifying the dependency model
(`Needs`/`OnFailure`/`SkipIf` into one `RunIf` family) may be worth its
own proposal someday; it is out of scope here.

## Non-goals (explicit)

- **No deploy / health / HTTP / auth knowledge in the SDK.** `Verify` is
  generic phase provenance; everything HTTP is `sparks-core/probe`.
- **No `OnVerifyFailure` (or any per-failure-kind hook).** One
  stage-aware `OnFailure`. Sugar can be added later if the explicit
  branch proves painful; it is far easier to add API than remove it.
- **No core classification of verify errors.** Unhealthy-vs-indeterminate
  lives in the probe library's error values, not in `Failure`.
- **No safe-rollback compatibility logic.** Whether the prior artifact
  is compatible with current schema/state stays in the author's recovery
  node.
- **No canary / traffic-split.** Needs a substrate a single-instance
  topology lacks. Out of scope.
- **No change to `AfterRun`.** It stays an observer.

## Decisions

Settled across all three reviews:

- **`Verify` is justified by phase provenance only** -- distinguishing
  action-failed from postcondition-failed. Not "you can check health."
- **One recovery shape: `OnFailure(ctx, Failure)`.** No
  `OnVerifyFailure`. *(This overrides the earlier field-review call that
  made the dedicated hook primary.* Reason: a verify failure is not
  reliably "deploy is bad" -- the indeterminate case proves it -- and the
  unhealthy-vs-indeterminate line is not universal, so no shortcut can
  encode a safe default. The reversal is the safe direction: add sugar
  later if warranted.)
- **Core knows only `Stage`.** HTTP, auth, retry policy, and
  unhealthy-vs-indeterminate never enter the SDK.
- **Cache means cache, always.** Cached node skips action + verify; live
  checks are separate nodes. *(Simplifies the earlier "build-time
  refusal" call to a rule with no special case.)*
- **Probe + error classification live in `sparks-core/probe`**, with
  per-attempt credentials and custom method/headers/body.
- **Per-attempt timing, two-layer model documented, compounded probe
  time capped.** `Verify` runs inside the `Retry` loop. The probe's
  internal readiness wait (`Retry(30).Interval(2s)` is ~60s) and the
  node's `Retry` compose; worst case multiplies (`Retry(3)` over a probe
  `Retry(30)` at 2s is roughly six minutes), so cap the compounded probe
  time.
- **`Failure.Stage` recorded into the failure record** regardless, for
  the run ledger and recovery nodes.

## What "ship it" looks like

- `Verify(fn)` on `JobNode` in `sparkwing/plan.go`, plus per-attempt
  evaluation in the in-process runner (after the action returns nil, map
  a non-nil verify error to `Failed` with `StageVerify`).
- `Failure` / `FailureStage` types; `OnFailure` extended to accept
  `func(ctx, Failure) error`; `Stage` emitted into the failure record.
- A cached node skips action + verify (no new refusal logic; verify is
  inside the cached unit).
- A documented ceiling on compounded probe time.
- Tests: action succeeds + verify fails -> Failed with `StageVerify`,
  recovery fires; action fails -> verify never runs; stage-aware
  `OnFailure` branches on both stages; `Retry` re-runs action + verify;
  verify success is a no-op; a cache hit runs neither action nor verify;
  the failure record carries the right `Stage`.
- Docs: SDK-reference entries for `Verify` and the new `OnFailure` shape,
  and a pipelines-guide section on the postcondition gate, the
  action-vs-verify fork, the unhealthy-vs-indeterminate distinction, and
  the two-layer retry model.
- `sparks-core/probe`: HTTP builder with custom method, static and
  per-attempt headers, body, `ExpectStatus` / `ExpectJSON`, and error
  classification (`Indeterminate`).
- CHANGELOG entry under `### Added`.

The SDK `Verify` gate is a small, focused change; the probe helper is
independent and lands in `sparks-core` on its own timeline.
