# Post-action verification: a `Verify` lifecycle gate (proposal)

**Status:** design settled after field review. Decisions resolved
below. Not yet implemented.

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

**Decided: a verify-specific recovery hook, `OnVerifyFailure`.** The
safe default falls out for free: wire only `OnVerifyFailure` and an
action failure has no recovery path, so it fails loudly. Auto-recovery
requires a separate, explicit opt-in.

```go
sw.Job(plan, "deploy", action).
    Needs(capture).
    Verify(probe.HTTP(healthURL).ExpectJSON("status", "ok").Retry(30).Check).
    OnVerifyFailure("rollback", rollbackFn)   // deployed-but-unhealthy: safe to automate
    // no OnFailure: a failed `up` fails the run; an operator investigates
```

Add `OnFailure` too only when a given deploy genuinely has a safe
action-failure recovery, and now both routes are explicit.

The rejected alternative was one `OnFailure` callback receiving
`f.Stage` and branching internally. It collapses both failures into a
single path where the unsafe one is a forgotten `if f.Stage == ` away.
For a primitive whose headline use is deploy rollback, the surface that
makes auto-rollback require explicit effort is the right one.

**Why the distinction is load-bearing: the deploy action is not
transactional.** `docker compose up -d` recreates services
sequentially; die partway and you get a mix -- web on the new image,
worker on the old, or the old container gone and the new one not
started. At the instant a recovery hook fires you do not know how far
`up` got, so the safer recovery is usually "re-run `up`" (converge
forward), not auto-rollback to a prior tag. When `Verify` fails, by
contrast, `up` ran to completion: the world is in a known state and the
prior artifact is a clean rollback target. The "did the action
complete?" line is exactly what the fork captures, and keeping the
compatibility decision in the author's recovery node is the right
boundary.

This fork is not deploy-specific. "Did the command crash, or did it run
clean and produce something that fails verification?" is a meaningful
distinction for a data job (re-run vs quarantine the bad output) or a
build (rebuild vs the toolchain is lying about success).

**Emit the failure stage regardless of routing.** Routing through
`OnVerifyFailure` and recording the stage are not mutually exclusive:
the run ledger and the recovery node both want action-vs-verify for
provenance, so `sw.Failure` carries a `Stage` discriminator
(`StageAction` / `StageVerify`) and the underlying error even under the
`OnVerifyFailure` routing.

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
    Method("GET").                       // POST + Body() also supported
    Header("X-Service-Token", token).    // arbitrary request headers
    ExpectStatus(200).
    ExpectJSON("status", "ok").
    Retry(30).Interval(2 * time.Second).Timeout(5 * time.Second).
    Check   // a func(ctx context.Context) error
```

The builder must support custom request headers (and method + body),
not just a bare GET. A health check worth gating a rollback on often
hits an *authenticated* endpoint behind an access proxy that requires a
signed service-token header, returning real data from the backing
datastore. A bare 200 from a public liveness path is not enough to
confirm the deploy is actually serving. The builder's assertion surface
(`ExpectStatus`, `ExpectJSON`) is only useful if it can first construct
the real request.

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

## Resolved decisions

1. **Failure fork: `OnVerifyFailure`, and emit `Stage` regardless.**
   Routing makes auto-rollback an explicit opt-in (the safe default by
   construction); the structured failure record still carries
   `StageAction` / `StageVerify` for provenance and for recovery nodes
   that want it. The two are not mutually exclusive.
2. **`Verify` + `.Cache()` on the same node is a loud refusal, for
   now.** `Verify` is an assertion about the *world*, not the
   *computation*, so the eventual rule is "it runs regardless of cache
   hit or miss." But until that interaction is designed, a `Verify` on a
   `.Cache()` node would arm an `OnVerifyFailure` rollback on a cache hit
   that deployed nothing this run. Rather than ship that footgun
   silently, combining the two on one node is a documented error until
   the cache interaction is designed deliberately.
3. **Per-attempt timing, with a documented two-layer model and a
   ceiling.** `Verify` runs inside the `Retry` loop (each attempt =
   action + verify); this differs from `BeforeRun`, which fires once
   before the first attempt, so `Verify` mirrors `BeforeRun`'s gating
   semantics but not its timing. Two retry layers then compose: the
   probe's internal readiness wait (`Retry(30).Interval(2s)` is about
   60s) and the node's `Retry` (redeploy + re-verify). Worst case
   multiplies: `Retry(3)` over a probe `Retry(30)` at 2s is roughly six
   minutes of probing on a wedged deploy before recovery fires. The
   two-layer model is documented and the compounded probe time gets a
   ceiling so a wedged deploy cannot probe unbounded.
4. **`sw.Failure` shape.** `Stage` (`StageAction` / `StageVerify`) plus
   the underlying error, defined as part of this work (see decision 1).

## What "ship it" looks like

- `Verify(fn)` modifier on `JobNode` in `sparkwing/plan.go`, plus the
  per-attempt evaluation in the in-process runner (run after the action
  returns nil, map a non-nil verify error to `Failed`).
- `OnVerifyFailure(id, x)` recovery hook, and `sw.Failure` carrying a
  `Stage` (`StageAction` / `StageVerify`) emitted into the failure
  record regardless of routing.
- `Verify` + `.Cache()` on the same node rejected at plan-build time
  with a clear error.
- A documented ceiling on compounded probe time so a wedged deploy
  cannot probe unbounded.
- Tests: action succeeds + verify fails -> node Failed, recovery fires;
  action fails -> verify never runs and `OnVerifyFailure` does not fire;
  `Retry` re-runs action + verify; verify success is a no-op; `Verify` +
  `.Cache()` rejected at build time; the failure record carries the
  right `Stage`.
- Docs: an SDK-reference entry for `Verify` / `OnVerifyFailure`, and a
  short pipelines-guide section on the postcondition gate, the
  two-failure fork, and the two-layer retry model.
- A `probe` package in a spark library (not the SDK) with the HTTP
  builder -- custom method, headers, and body, not just a bare GET --
  mirroring the existing internal health-response shape.
- CHANGELOG entry under `### Added`.

## Decisions (resolved after field review)

- Q1: `OnVerifyFailure`. Auto-rollback is an explicit opt-in; an
  unhandled action failure fails loudly.
- Q2: `Verify` + `.Cache()` on one node is a documented error until the
  cache interaction is designed; ship the non-cached path first.
- Q3: per-attempt timing confirmed; document the two-layer retry model
  and cap the compounded probe time.
- Q4: define `sw.Failure.Stage` (`StageAction` / `StageVerify`)
  regardless of routing.

The SDK `Verify` gate is a small, focused change; the probe helper is
independent and can land in a spark library on its own timeline.
