# Post-action verification: a `Verify` lifecycle gate (proposal)

**Status:** queued. Not implemented. Feedback wanted before any code lands.

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

A new node modifier, **`Verify`**: the postcondition gate, peer to
`BeforeRun`.

```go
// Verify registers a postcondition checked after the node's action
// succeeds. The action exited 0, but if fn returns a non-nil error
// the node's outcome is Failed -- making it eligible for Retry and
// triggering OnFailure, exactly as an action failure would. Verify
// runs per attempt: a retried action is re-verified.
func (n *JobNode) Verify(fn func(ctx context.Context) error) *JobNode
```

That is the whole core addition. A failed `Verify` reuses the machinery
that already exists:

- `Retry` re-runs the action *and* the verification together -- the
  right shape for "deploy, then wait for healthy, and redeploy if it
  never comes up."
- `OnFailure` fires the recovery node (rollback, alert, escalate).
- downstream `Needs` is correctly blocked, because the node itself is
  `Failed`.

No new outcome type, no new recovery-routing primitive. `Verify` slots
into the existing lifecycle vocabulary as the missing word.

### The two-failure fork (the important part)

A node with a `Verify` gate can now fail in two distinct ways, and they
are **not** the same situation:

- **Action failed.** The command itself died, possibly partway. The
  world is in an *unknown* state -- half-applied, no-op, or corrupted.
  Whether recovery (e.g. rollback) is even meaningful is unknowable from
  sparkwing's vantage point.
- **Verify failed.** The command *fully completed*; the postcondition is
  what failed. The world state is known precisely. For a deploy this is
  the safe case: the prior artifact is a clean, known-good, fully
  deployed target, so rolling back is the obvious move.

A single `OnFailure` that fires on both collapses these into one
recovery path, which is wrong: you do not want to auto-roll-back a
deploy whose `up` died in an unknown state.

The proposal exposes the distinction and lets the DAG author decide
(the *decision* -- is the prior artifact compatible, is a half-apply
safe to revert -- is necessarily app-specific and stays downstream).
Two ways to surface it, and we want feedback on which:

**Option A -- a verify-specific recovery hook.** The safe default falls
out for free: you opt in to auto-recovery only for the known-safe
verify case; an action failure with no `OnFailure` attached just
surfaces loudly.

```go
sw.Job(plan, "deploy", action).
    Needs(capture).
    Verify(probe.HTTP(healthURL).ExpectJSON("status", "ok").Retry(30).Check).
    OnVerifyFailure("rollback", rollbackFn)   // deployed-but-unhealthy: safe to automate
    // no OnFailure: a failed `up` fails the run; an operator investigates
```

Add `OnFailure` too only when a given deploy genuinely has a safe
action-failure recovery, and now both routes are explicit.

**Option B -- one hook with failure context.** `OnFailure`'s callback
receives which stage failed and branches:

```go
deploy.OnFailure("recover", func(ctx context.Context, f sw.Failure) error {
    if f.Stage == sw.StageVerify { return rollback(ctx) }
    return escalate(ctx, f.Err)   // action failed: don't auto-rollback
})
```

Option A reads better for the common case and makes the safe behavior
the default; Option B is better when one recovery routine wants to
branch. They share the same idea: **the framework's job is to expose
which stage failed; the recovery decision stays in app code.**

This fork is not deploy-specific. "Did the command crash, or did it run
clean and produce something that fails verification?" is a meaningful
distinction for a data job (re-run vs quarantine the bad output) or a
build (rebuild vs the toolchain is lying about success).

## Where it lives: SDK vs spark library

The probe in the examples above is **not** part of this proposal's core
change, and that split is deliberate:

- **`Verify` belongs in the sparkwing SDK** (`sparkwing` package). It is
  a general lifecycle primitive with zero knowledge of HTTP, health, or
  deploys -- it earns a place next to `BeforeRun` and `OnFailure`
  because it touches the node lifecycle and the orchestrator's outcome
  evaluation.
- **The HTTP health probe belongs in a spark library** (e.g.
  `sparks-core/probe`), not the SDK. It is generic `net/http` + retry +
  JSON-assert with nothing sparkwing-specific in it. Putting it in the
  package every pipeline author imports would tax everyone for a helper
  most pipelines never call, and lock a generic utility into the SDK's
  supported surface forever. As a spark library it ships, is supported,
  matches sparkwing's own `{"status":"ok","problems":[...]}` health
  shape, and is pulled only by the DAGs that want it.

So `HealthProbe` is not a primitive at all -- it is the composition
`Verify(probe.HTTP(...).Check)`. The general gate is SDK-worthy; the
probe is a library. The probe builder produces a `func(ctx) error`
suitable for handing to `Verify`:

```go
// in a spark library, not the SDK
probe.HTTP(url).
    ExpectStatus(200).
    ExpectJSON("status", "ok").
    Retry(30).Interval(2 * time.Second).Timeout(5 * time.Second).
    Check   // a func(ctx context.Context) error
```

## Worked example: deploy with verified health and safe rollback

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
```

The rollback node reads the pre-deploy tag via `Ref.Get`, which already
works from a recovery node: the typed-output store is run-scoped and
readable from any node, and because `capture` is upstream of the failing
`deploy`, ordering is guaranteed (`capture` -> `deploy` -> failure ->
rollback), so no extra dependency edge is needed.

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

## Open questions

1. **Option A or Option B for the failure fork?** A dedicated
   `OnVerifyFailure` hook (safe default, declarative) vs an enriched
   `OnFailure` callback that receives the failure stage (one hook,
   branches in app code). Leaning A for the common case.
2. **Interaction with `.Cache()`.** `Verify` is an assertion about the
   *world*, not the *computation*, so the natural rule is "it runs
   regardless of cache hit or miss" -- skip the expensive redeploy on a
   hit, but still confirm health. The wrinkle: if `Verify` runs on a
   cache hit and fails, an `OnVerifyFailure` rollback would fire even
   though this run deployed nothing, which is surprising. Proposed
   resolution: ship `Verify` on the normal (non-cached) path first and
   treat the cache interaction as a separate decision rather than
   guessing now.
3. **Per-attempt timing.** `Verify` should run inside the `Retry` loop
   (each attempt = action + verify). Note this differs from `BeforeRun`,
   which fires once before the first attempt -- so `Verify` mirrors
   `BeforeRun`'s gating semantics but not its timing.
4. **Failure-stage representation.** If Option B, `sw.Failure` needs a
   `Stage` discriminator (`StageAction` / `StageVerify`) and the
   parent's error. This is also the structured signal a recovery node
   would want regardless, so it is worth defining cleanly.

## What "ship it" looks like

- `Verify(fn)` modifier on `JobNode` in `sparkwing/plan.go`, plus the
  per-attempt evaluation in the in-process runner (run after the action
  returns nil, map a non-nil verify error to `Failed`).
- The chosen failure-fork surface (`OnVerifyFailure`, or `sw.Failure`
  with a `Stage` field).
- Tests: action succeeds + verify fails -> node Failed, recovery fires;
  action fails -> verify never runs, action-recovery path; `Retry`
  re-runs action + verify; verify success is a no-op.
- Docs: an SDK-reference entry for `Verify` and a short section in the
  pipelines guide on the postcondition gate and the two-failure fork.
- A `probe` package in a spark library (not the SDK) with the HTTP
  builder, mirroring the existing internal health-response shape.
- CHANGELOG entry under `### Added`.

## Decisions wanted before implementation

- Q1: Option A (`OnVerifyFailure`) or Option B (enriched `OnFailure`)?
- Q2: confirm "Verify runs regardless of cache; cache interaction is a
  later decision."
- Q3: confirm per-attempt timing.
- Q4: if Option B, agree the `sw.Failure` shape.

If those land roughly right, the SDK `Verify` gate is a small, focused
change; the probe helper is independent and can land in a spark library
on its own timeline.
