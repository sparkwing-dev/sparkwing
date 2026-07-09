# Local Admission Owner

**Status:** implemented.

## Problem

Local `sparkwing run` used to have two independent host-resource gates:

- The host-local `box-slots` semaphore admitted whole `sparkwing run`
  processes before the run opened and planned the pipeline.
- The `.Concurrency(...)` store gate admitted plan or node work after the
  run had already entered the orchestrator.

That split let one run hold the coarse host semaphore while it was only
waiting on the store-backed pipeline gate. Under contention, capacity was
owned by the wrong layer: the host said "a run is active" while the
pipeline said "this run is not admitted yet." The visible result was queue
churn, idle capacity, misleading wait diagnostics, and manual maintenance
as a recovery path.

Sparkwing now has an explicit host-admission contract: a plan-level
`ScopeBox` concurrency group can declare that it owns local host execution
budget. Waiting on that group does not also consume the default
`box-slots` budget.

## Principles

1. **One resource, one owner.** If host execution capacity is scarce,
   exactly one admission system owns that budget for a run. A run should
   not wait on plan admission while also holding `box-slots`.
2. **Waiting is not execution.** A queued run is not consuming the
   resource it is queued for. If it has not been admitted, it should not
   hold process slots, worker slots, or budget from a different layer.
3. **Locality is not meaning.** `ScopeBox` means "this key is scoped to
   one machine." It does not automatically mean "this is the host
   CPU/process admission gate." Host admission needs an explicit contract.
4. **Inherited ownership is ownership.** If a parent run already holds a
   host-admission budget and passes it to a child or middle run, the child
   is already covered. It must not reacquire `box-slots` just because it
   does not declare its own plan concurrency.
5. **State converges without humans.** Finished holders, stale holders,
   and cancelled waiters are cleared by normal admission, promotion, and
   release paths. Manual maintenance is an operator tool, not the
   correctness path.
6. **The queue tells the truth.** If something is queued, evicted, timed
   out, or waiting on admission, Sparkwing reports that exact condition
   with the key and inspection command. It must not look like test failure
   or generic execution failure.

## Invariants

1. **No double ownership by default:** A local run that declares or
   inherits an explicit host-admission plan gate does not also hold a host
   `box-slots` marker while waiting for that plan gate, unless the operator
   explicitly passed `--sw-box-slots` or `SPARKWING_BOX_SLOTS_PIN`.
2. **Waiters do not hold unrelated capacity:** A run blocked in declared
   or inherited host plan admission holds no worker slot and no host box
   slot unless the operator explicitly pinned `box-slots`.
3. **Host ownership is keyed:** An inherited admission set may carry both
   host and non-host plan holders. Only the holder named by
   `host_admission_key` owns host execution admission.
4. **Finished holders are stale:** A holder whose run row is terminal is
   not live for budget accounting, even if its lease has not yet expired.
5. **Inherited cost stays live:** Releasing or reaping a charged parent
   holder transfers its cost to one live inherited holder before deleting
   the parent. Sibling inherited holders keep the budget charged until the
   last covered run releases.
6. **Promotion is eager:** Release and stale-holder cleanup paths that
   delete a holder also promote waiters for that key before returning.
7. **Maintenance is bounded:** Inline maintenance uses a 15-second context
   timeout and reports `warn: concurrency maintenance:` on failure.
   Admission paths still perform targeted cleanup for the key they are
   about to evaluate.
8. **Events are truthful:** Plan admission wait, wait update, promotion,
   timeout, and cancellation events carry the key, queue position when
   applicable, queue length, and current holders.

## Runtime Contract

- A plan-level `Concurrency` group replaces `box-slots` only when it is
  declared with `ScopeBox` and `HostAdmission`.
- Multiple plan-level `Concurrency` calls compose as independent
  whole-run gates. Use separate groups for separate resources, such as one
  deploy mutex plus per-host CPU and memory budgets.
- Exactly one plan-level group may declare `HostAdmission`; host execution
  has one owner even when the run holds several plan-level gates.
- Node-level concurrency never replaces `box-slots`, because a run can do
  unrelated work before reaching a node-level waiter.
- A run with no explicit host-admission plan gate still receives the
  default `box-slots` protection from process start through dispatch,
  unless it is covered by inherited host admission.
- If `--sw-box-slots` or `SPARKWING_BOX_SLOTS_PIN` is set, the pinned
  host slot is acquired only after plan admission promotes the run.
- Controller-triggered inherited host admission is verified against the
  ancestor plan holder before it is forwarded. Request JSON alone is not a
  trusted host-admission assertion.
- For store-backed concurrency, admission and waiter resolution prune
  holders whose run rows are terminal before computing budget or promoting
  waiters. Backends without a durable run table use lease expiry for orphan
  recovery.

## Event Contract

Plan-level concurrency emits:

- `concurrency_wait` when the plan parks behind a key.
- `concurrency_wait_update` when a queued plan's position or holder
  summary changes while polling.
- `concurrency_promoted` when the plan waiter becomes a holder.
- `concurrency_queue_timeout` when queue timeout cancels the plan waiter.
- `concurrency_cancelled` when plan admission is cancelled or evicted
  before dispatch.

Payload fields:

- `scope: "plan"`
- `resource: "host_admission"` when the plan gate owns host execution
  admission, otherwise `resource: "plan_admission"`
- `key`
- `kind`
- `position` for queue waiters
- `queue_length` for queue waiters
- `holders`, capped to 8 entries, each with `run_id`, `node_id`,
  `holder_id`, and `lease_expires_at`

Plan timeout diagnostics name the key and the inspection surface:

`inspect with sparkwing cluster concurrency --namespace <key> --profile <profile>`
