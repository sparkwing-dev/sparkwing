# Post-action verification: a `Verify` lifecycle gate (proposal)

**Status:** design settled after field review, with two amendments to
that review (see Decisions). Not yet implemented.

## The gap

A node's command can exit 0 without the thing it was supposed to do
having actually happened. `docker compose up` returns success, but the
service comes up unhealthy. A migration runs clean, but the schema
didn't advance. An ETL step finishes, but the output table is empty.
`terraform apply` succeeds, but a follow-up `plan` still shows drift.

In every one of these, **exit 0 is necessary but not sufficient.** The
tool succeeded mechanically; the real-world postcondition it was meant
to establish did not hold.

Sparkwing has no way to express that. The node lifecycle has a
*precondition* gate but no *postcondition* gate:

| Hook | Position | Gates the outcome? |
|---|---|---|
| `BeforeRun` (`plan.go:908`) | before the action | **yes** -- non-nil error fails the node without running it |
| `Retry` / `Timeout` | around the action | -- |
| `AfterRun` (`plan.go:922`) | after the action | **no** -- "its own failure is logged but does not change the node's outcome" |
| *(missing)* | after a successful action | -- |

`AfterRun` looks like the symmetric partner to `BeforeRun`, but it is an
*observer*: it can watch the result, not veto it. So there is a
precondition gate and no postcondition gate. The symmetry is half
implemented.

The most common place this bites is deploy health. The `OnFailure` hook
fires only when a node's own command exits non-zero (`orchestrator.go`
dispatches the recovery node only when `parentOutcome == Failed`); it
has no concept of "the command succeeded but the deployed service is
unhealthy." Every deploy DAG that wants post-deploy verification
hand-rolls a readiness-probe loop (curl + retry + JSON assert) in a node
body and wires the recovery by hand.

Prior art separates the two cleanly:

| System | Post-action verification | Recovery on failure |
|---|---|---|
| Kubernetes | readiness / liveness probes | `rollout undo` + revision history |
| Helm | `--wait` + chart hooks | `helm rollback <rev>` |
| systemd | `ExecStartPost=` + readiness notify | `Restart=`, `OnFailure=` |
| sparkwing | -- (hand-rolled per DAG) | `OnFailure` (action-failure only) |

## Proposed shape

Three additions to the SDK: the `Verify` gate, a `Failure` value that
recovery callbacks can branch on, and `OnVerifyFailure` sugar over the
common case.

### `Verify`

```go
// Verify registers a postcondition checked after the node's action
// succeeds. The action exited 0, but if fn returns a non-nil error the
// node's outcome is Failed -- eligible for Retry and triggering the
// recovery hook, exactly as an action failure would. Verify runs per
// attempt: a retried action is re-verified.
func (n *JobNode) Verify(fn func(ctx context.Context) error) *JobNode
```

That is the whole gate. A failed `Verify` reuses the machinery that
already exists: `Retry` re-runs action + verification together (the
right shape for "deploy, then wait for healthy, redeploy if it never
comes up"), the recovery hook fires, and downstream `Needs` is correctly
blocked because the node itself is `Failed`. No new outcome type.

### `Failure` and the recovery foundation

Recovery callbacks can see *why* the node failed:

```go
// Failure describes why a node terminated unsuccessfully. It is passed
// to a recovery callback so recovery logic can branch on the stage at
// which the node failed.
type Failure struct {
	Stage FailureStage // which lifecycle stage produced the failure
	Err   error        // the action error (StageAction) or verify error (StageVerify)
}

type FailureStage int

const (
	StageAction FailureStage = iota // action exited non-zero or timed out
	StageVerify                     // action completed; the Verify postcondition failed
)
```

The existing `OnFailure(id, x)` already accepts a `Workable` or a
`func(ctx) error`. It gains one more accepted shape -- a
failure-aware closure -- so a single recovery node can branch on stage:

```go
// OnFailure registers a recovery node that runs when this node's
// outcome is Failed. The recovery inherits no dependencies from the
// parent. The argument may be:
//   - a Workable, or a func(ctx) error
//         -- recovery with no failure context
//   - a func(ctx context.Context, f Failure) error
//         -- recovery that branches on f.Stage / f.Err
func (n *JobNode) OnFailure(id string, x any) *JobNode
```

This is the **foundation**: one hook, one recovery slot, full failure
context. It is deliberately the general shape -- `Stage` is one
discriminator today, and a `Failure` value extends to others (timeout
vs error, future stages) without a new hook per failure kind. (Note this
diverges from `Job`, which does not accept the failure-aware closure:
there is no failure at Job-construction time, only at recovery time.)

### `OnVerifyFailure` (sugar)

The common, safe case gets a dedicated spelling:

```go
// OnVerifyFailure registers recovery that runs only when the node failed
// at the Verify stage. An action-stage failure does not trigger it.
// Exactly equivalent to an OnFailure callback that no-ops for
// StageAction -- but it makes the safe default explicit: wire only
// OnVerifyFailure and an action failure has no recovery path, so it
// fails loudly. Auto-recovery becomes an opt-in you cannot reach by
// accident.
func (n *JobNode) OnVerifyFailure(id string, x any) *JobNode
```

`OnFailure` and `OnVerifyFailure` fill the same single recovery slot and
are mutually exclusive on a node. To take *different* actions per stage,
use the `func(ctx, Failure)` form of `OnFailure` and branch on
`f.Stage`.

### Evaluation order (per attempt)

1. `BeforeRun` (first attempt only) -- non-nil error fails the node
   without running, no retry (unchanged).
2. Run the action. Non-nil / timeout -> attempt failed, `StageAction`.
3. Action succeeded -> run `Verify`. Non-nil -> attempt failed,
   `StageVerify`.
4. Both succeed -> attempt success.
5. `Retry` re-runs steps 2--3.
6. On final failure, build `Failure{Stage, Err}` and dispatch the
   recovery hook (if outcome is `Failed`). The recovery node stays
   independent of the parent's outcome -- it performs side effects
   (rollback, alert); it does not un-fail the parent.

## Why the two-failure fork is load-bearing

A node with a `Verify` gate fails in two distinct ways, and they are
**not** the same situation:

- **Action failed** (`StageAction`). The command itself died, possibly
  partway, leaving an *unknown* state. The deploy action is **not
  transactional**: `docker compose up -d` recreates services
  sequentially, so dying partway leaves a mix -- web on the new image,
  worker on the old, or the old container gone and the new one not
  started. At the instant recovery fires you do not know how far `up`
  got, so the safer recovery is usually "re-run `up`" (converge
  forward), not auto-rollback to a prior tag.
- **Verify failed** (`StageVerify`). The command *ran to completion*;
  the postcondition is what failed. The world is in a known state and
  the prior artifact is a clean rollback target.

A single recovery path that fires on both collapses these. That is why
`OnVerifyFailure` exists and is the recommended spelling: it makes
rollback reachable only for the known-safe case, while keeping the
compatibility decision (is the prior artifact safe to restore?) in the
author's recovery node, where it belongs.

The fork is not deploy-specific. "Did the command crash, or did it run
clean and produce something that fails verification?" is a meaningful
distinction for a data job (re-run vs quarantine the bad output) or a
build (rebuild vs the toolchain is lying about success).

## Where it lives: SDK vs spark library

The probe in the examples is **not** part of this proposal's core
change, and that split is deliberate:

- **`Verify`, `Failure`, `OnVerifyFailure` belong in the sparkwing SDK.**
  They are general lifecycle surface with zero knowledge of HTTP,
  health, or deploys -- they earn a place next to `BeforeRun` and
  `OnFailure` because they touch the node lifecycle and the
  orchestrator's outcome evaluation.
- **The HTTP health probe belongs in a spark library** (e.g.
  `sparks-core/probe`), not the SDK. It is generic `net/http` + retry +
  JSON-assert with nothing sparkwing-specific in it. Putting it in the
  package every author imports would tax everyone for a helper most
  pipelines never call, and lock a generic utility into the SDK's
  supported surface forever. As a spark library it ships, is supported,
  matches sparkwing's own `{"status":"ok","problems":[...]}` health
  shape, and is pulled only by the DAGs that want it.

So `HealthProbe` is not a primitive at all -- it is the composition
`Verify(probe.HTTP(...).Check)`. The probe builder produces a
`func(ctx) error` suitable for handing to `Verify`:

```go
// in a spark library, not the SDK
probe.HTTP(url).
    Method("GET").                              // POST + Body() also supported
    HeaderFunc("X-Service-Token", signToken).   // per-attempt credential provider
    ExpectStatus(200).
    ExpectJSON("status", "ok").
    Retry(30).Interval(2 * time.Second).Timeout(5 * time.Second).
    Check   // a func(ctx context.Context) error
```

Two requirements on the builder, both from the field:

- **Custom method, headers, and body, not just a bare GET.** A health
  check worth gating a rollback on often hits an *authenticated*
  endpoint behind an access proxy that requires a signed service-token
  header and returns real datastore data. A bare 200 from a public
  liveness path is not enough to confirm the deploy is actually serving.
- **Credentials are per-attempt, not captured once.** `Header(name,
  value)` takes a constant; `HeaderFunc(name, func(ctx) (string,
  error))` is evaluated on *each* request. This matters because a
  `Retry(30).Interval(2s)` loop runs ~60s (longer with node-level
  retries), and a signed token captured at builder-construction can
  **expire mid-loop**. A static header would then make the probe fail on
  *auth*, not health -- triggering a rollback for the wrong reason. A
  per-attempt provider re-signs each request and keeps the gate honest.

## Worked example: deploy with verified health and safe rollback

Common case -- rollback only on the known-safe verify failure:

```go
// capture is a typed job exposing the currently-deployed tag.
capture := /* Produces[string]: docker inspect the running image */
prevTag := sw.RefTo[string](capture)

sw.Job(plan, "deploy", func(ctx context.Context) error {
        return sw.Bash(ctx, "docker compose pull && docker compose up -d").Run()
    }).
    Needs(capture).
    Verify(probe.HTTP("http://localhost:8080/healthz").
        ExpectJSON("status", "ok").Retry(30).Interval(2 * time.Second).Check).
    OnVerifyFailure("rollback", func(ctx context.Context) error {
        return sw.Bash(ctx, fmt.Sprintf(
            "docker tag %s regent:current && docker compose up -d",
            prevTag.Get(ctx))).Run()
    })
    // no OnFailure: a failed `up` fails the run; an operator investigates
```

Handling both stages differently -- one recovery node, branch on stage:

```go
deploy.OnFailure("recover", func(ctx context.Context, f sw.Failure) error {
    switch f.Stage {
    case sw.StageVerify:
        return rollbackToPrior(ctx, prevTag.Get(ctx)) // known state, safe
    default: // StageAction
        return convergeForward(ctx)                   // unknown state, re-run up
    }
})
```

The rollback node reads the pre-deploy tag via `Ref.Get`, which already
works from a recovery node: the typed-output store is run-scoped and
readable from any node, and because `capture` is upstream of the failing
`deploy`, ordering is guaranteed (`capture` -> `deploy` -> failure ->
recovery), so no extra dependency edge is needed.

## Non-goals (explicit)

- **No deploy / health / probe logic in the SDK.** `Verify` is generic;
  the probe is a spark library.
- **No safe-rollback compatibility logic.** Whether the prior artifact
  is compatible with the current schema or state (rolling an image back
  across a forward-only migration is data loss) is app-specific and
  stays in the author's recovery node. The primitive supplies the gate
  and the failure-stage signal, not the compatibility decision.
- **No canary / traffic-split.** That needs a traffic-split substrate a
  single-instance topology does not have. Out of scope.
- **No change to `AfterRun`.** It stays an observer. `Verify` is the
  gating partner; `AfterRun` remains for logging and side effects that
  must not change the outcome.

## Decisions

The fork (`Verify` + failure-stage routing), per-attempt timing, and the
SDK-vs-library split are settled. Two points **amend** the field review
and need sign-off, because each refines an earlier call:

1. **Recovery API: failure-context `OnFailure` is the foundation,
   `OnVerifyFailure` is sugar over it** -- *amends the field review*,
   which picked the dedicated hook as the primary (and only) surface.
   Reasoning: the field review separately decided to record
   `Failure.Stage` regardless of routing, which means the structured
   failure value is built either way. Once it exists, a single
   stage-aware `OnFailure` callback is nearly free, and it is the
   *extensible* shape -- a dedicated hook per failure kind
   (`OnVerifyFailure`, then `OnTimeout`, ...) does not scale. Keeping
   `OnVerifyFailure` as sugar preserves the safe-default ergonomic (it
   stays the recommended spelling) without making the primitive a
   per-kind hook. Net surface is the same; the layering is inverted so
   the general path stays open.

2. **`Verify` + `.Cache()` on the same node is a build-time error in
   v1; the *eventual* rule is narrower** -- *refines the field review*,
   which proposed the blunt refusal as the resolution. The blunt refusal
   is correct for v1: allowing `Verify` to run on a cache *hit* means a
   hit no longer means "skip execution entirely" (the orchestrator must
   invoke verification even on a hit), which is exactly the undesigned
   cache-fast-path interaction being deferred. The intended end state is
   to forbid only `.Cache()` + auto-recovery (the actual footgun:
   rolling back a deploy a cache-hit run never performed) and let a
   recovery-free `Verify` run on a hit. That end state is documented as
   direction, not shipped, until the cache-hit-verify semantics are
   designed.

Settled (from the field review, unchanged):

- **Per-attempt timing, two-layer model documented, compounded probe
  time capped.** `Verify` runs inside the `Retry` loop. Two retry layers
  compose: the probe's internal readiness wait
  (`Retry(30).Interval(2s)` is ~60s) and the node's `Retry` (redeploy +
  re-verify). Worst case multiplies -- `Retry(3)` over a probe
  `Retry(30)` at 2s is roughly six minutes of probing on a wedged deploy
  before recovery fires. Document the model; cap the compounded probe
  time so a wedged deploy cannot probe unbounded.
- **`Failure.Stage` is recorded into the structured failure record
  regardless of which recovery spelling is used** -- the run ledger and
  recovery nodes both want action-vs-verify for provenance.

## What "ship it" looks like

- `Verify(fn)` modifier on `JobNode` in `sparkwing/plan.go`, plus the
  per-attempt evaluation in the in-process runner (after the action
  returns nil, map a non-nil verify error to `Failed` with
  `StageVerify`).
- `Failure` / `FailureStage` types; `OnFailure` extended to accept
  `func(ctx, Failure) error`; `OnVerifyFailure(id, x)` sugar. `Stage`
  emitted into the failure record regardless of spelling.
- `Verify` + `.Cache()` on the same node rejected at plan-build time
  with a clear error.
- A documented ceiling on compounded probe time.
- Tests: action succeeds + verify fails -> node Failed with
  `StageVerify`, recovery fires; action fails -> verify never runs and
  `OnVerifyFailure` does not fire; stage-aware `OnFailure` branches
  correctly on both stages; `Retry` re-runs action + verify; verify
  success is a no-op; `Verify` + `.Cache()` rejected at build time; the
  failure record carries the right `Stage`.
- Docs: SDK-reference entries for `Verify` / `OnFailure` (new shape) /
  `OnVerifyFailure`, and a pipelines-guide section on the postcondition
  gate, the two-failure fork, and the two-layer retry model.
- A `probe` package in a spark library (not the SDK): HTTP builder with
  custom method, static and per-attempt (`HeaderFunc`) headers, body,
  and `ExpectStatus` / `ExpectJSON` assertions, mirroring the existing
  internal health-response shape.
- CHANGELOG entry under `### Added`.

The SDK `Verify` gate is a small, focused change; the probe helper is
independent and can land in a spark library on its own timeline.
