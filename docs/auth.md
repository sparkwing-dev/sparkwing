# Authentication + authorization

Sparkwing uses a shared-secret bearer token model with typed principals
and per-endpoint scope annotations.

## Token format

Raw tokens are `<prefix>_<entropy>`:

- `swu_...` -- user. Created for humans (`sparkwing cluster tokens create --type user`,
  or `sparkwing cloud connect --admin-token-stdin`, which mints one and writes the
  profile that holds it).
- `swr_...` -- runner. Created for remote machine agents or pool replicas.
- `sws_...` -- service. Created for in-cluster back-channel callers.

The **prefix segment** is the first 12 characters of a raw token. It's
a non-secret identifier used in `sparkwing cluster tokens list`, `revoke`, and
audit logs. The remaining ~35 characters carry the secret entropy.

## Metered runners

A token carries a `metered` marker the operator sets, either at mint
(`sparkwing cluster tokens create --metered`) or afterwards
(`sparkwing cluster tokens set-metered --prefix P --metered true`). That marker
is the only thing that decides whether the work a runner does costs credits.
A claim-mode runner chooses its own labels, so a label saying "cloud" proves
nothing and metering never reads one.

A claim by a metered token reserves the minimum billable time, 20 seconds at
the node's class, inside the claim's own transaction. The reservation is what
makes the check safe when several runners poll at once: each one's spend is
visible to the next before either claim commits, so a balance that covers one
minimum hands out one node, not one per runner. When the balance cannot cover the reservation,
`POST /api/v1/nodes/claim` answers `402` with `"code": "insufficient_credits"`,
the node stays ready, the run records a `credits_blocked` event, and the runner
keeps polling.

A metered trigger claim starts a whole run, so it is gated too. The claim
names how the runner executes the trigger's nodes (`node_runner`: `k8s`,
`warm` or `inprocess`, which an empty value means). A metered token that names
`inprocess` gets `403` with `"error": "metered_inprocess_nodes"`, because nodes
a trigger holder runs in its own process hold no node claim and are never
charged; `sparkwing-runner runner --trigger-runner=inprocess` on a metered
token stops its trigger loop with that reason and keeps claiming nodes. A
metered pool therefore sets `runner.triggerRunner.kind` to `k8s` or `warm`
in the runner-bundle chart.

The trigger step itself, the planning and orchestration the holder runs on
the claiming pool, is billed too. A metered `k8s` or `warm` claim reserves
the cheapest class's minimum inside the claim's transaction, the same way a
node claim does, so claims racing for a balance that covers one minimum start
one run; the rest answer `402` and their triggers stay pending. The
claim does not say how large the pool is, so the step is billed at the
cheapest class. When the claim ends, the step is billed for the wall time
since the claim: a finish inside the minimum pays the minimum, one past it
bills the rest, and a lapsed lease is billed through the lease's end
rather than through the reap. A claim requeued before its run started is
refunded whole. These ledger rows carry the run id and an empty node id.

Billing runs from the moment the machine that executes a node starts work on
it to the node's finish, so fetching the source and compiling the pipeline are
billed; queueing and provisioning are not. A runner that claims work from the
queue or accepts an offer is that machine, so its node bills from the claim.
A dispatcher that claims a node and then creates a Kubernetes Job for it
claims before the pod exists, so that node bills from the pod's first claim
renewal, which the pod sends as it starts, or from its execution start if that
comes first. A heartbeat charges the seconds since the previous charge, and
the finish charges the tail the last heartbeat missed. Every node that starts
pays at least the minimum: the reservation is consumed rather than refunded,
so a node that runs for four seconds pays for twenty, and one that runs for a
minute pays for a minute.

A node whose machine never started gets its reservation back: a pod that never
came up, a claim reaped before its pod renewed it. A node the platform stops
before its execution starts gets back everything its claim billed, setup
included. That covers a lost runner (`agent_lost`), an expired runner lease
(`runner_lease_expired`), a claim reaped before execution, no machine of the
class coming free (`queue_timeout`), and a log service that refused or dropped
the node's writes (`logs_auth`, `logs_dropped`). Any other end before
execution is the pipeline's own and keeps its setup billed: a compile error, a
source fetch the repository refused, a cancellation, an out-of-memory kill. A
platform failure after execution starts is billed like any other finish.

Two bounds apply. No single charge bills more than the charge cap (30 seconds by default), so a controller
outage or a stalled heartbeat loop does not bill the gap it left behind. A
node that is requeued -- its lease reaped, its runner lost, or its attempt
reset for a retry -- releases its charge window, so the next attempt starts a
fresh reservation and the idle time between attempts is never billed.

Once the team's balance reaches zero the node keeps running for the grace
period. The first heartbeat after that window fails the node with the failure
reason `credits_exhausted`, releases its claim, and answers `409`, which is how
the runner learns to stop. The run records a `credits_exhausted` event naming
the balance and how long it had been spent. Both the balance and the instant it
ran out are the team's own, so one team spending its grants refuses and cancels
that team's nodes and leaves every other team on the controller running.

A token with no marker is neither checked nor charged, so a deployment that
marks none bills nothing.

## Credits

Cloud runner time is prepaid. One credit is one second of one vCPU, and
20,000 credits is one dollar, which prices compute at $0.18 a vCPU-hour.
Amounts are stored in micro-credits, 5,000 to the credit, so a dollar is
100,000,000 micro-credits and a cent is 1,000,000. A class costs its core count
in credits a second: a four-core second is 4 credits, a minute 240, an hour
14,400 ($0.72), so ten dollars (200,000 credits) buys just under fourteen
four-core hours.

A second is priced by the node's cpu class. The rate table prices one class per
whole-core size, and a node is billed at the class it pinned, which is the class
the pod is given. Nothing a claimant says about itself reaches the price,
because a runner that priced its own work would bill an 8-core node at the
smallest class. A request above the largest class the table prices is refused
when the class is chosen, failing the node with `unpriced_cpu_class` and a
`credits_unpriced_class` event naming both sizes, rather than reserving credits
for a node no claim can pay for.

An installation that never set a table bills the default ladder, each class at
its core count in credits a second: 2-core 10,000 micro-credits, 4-core 20,000,
8-core 40,000. `credit_rate_micro_per_second` is the four-core
entry of that ladder under another name. Once a table exists that setting is
derived: a `PUT` that names it, alone or beside `rate_table`, answers `400` and
says to write the table.
`sparkwing cluster credits settings --rate-table 2=10000,4=20000,8=40000` sets
the ladder and needs `admin`. A stored table this build cannot read is an error
on every credit read rather than a silent return to the flat rate.

A balance belongs to a team: it is the sum of that team's grants less the sum of
that team's charges, computed in SQL over the `credit_grants` and
`credit_charges` tables. A grant reference is a payment id, so it stays unique
across the deployment and a second team replaying one is refused rather than
granted the first team's credits. The price of a second, the grace period, the
charge cap, the rate table, the warm cpu class and the storage rate are the
deployment's and are the same for every team. A grant is `free` or `paid`
and records who added it and the payment it came from. A charge is a
`reservation` a claim took, the `usage` an interval billed, or the `refund` of
a reservation a node did not use; each names the run, node, token prefix, and
seconds it covered, and the class and rate it was billed at, so a later change
to the table never reprices a charge already written.

`sparkwing cluster credits show` prints the balance, the rate table, the charge
cap and the last day's burn. `sparkwing cluster credits grant --kind free|paid
--amount N` adds credits and needs `admin`. `sparkwing cluster credits history`
lists every movement newest first.

## Buying credits

A team owner buys credits from the dashboard's Team -> Billing page, and any
member reads the balance, the price table, usage by run and the team's
purchases there, from `GET /api/v1/team/billing`. A purchase is between $5 and
$500. Purchases are final and credits never expire.

The Stripe Checkout Session is created on the server, never in the browser,
so no caller chooses the team a payment funds. The dashboard posts only the
amount to `POST /api/v1/team/billing/checkout`, which needs an owner of the
active team. The controller takes the team from that session, refuses an
amount outside the range, and asks the hosted checkout service at
`--billing-url` (`SPARKWING_BILLING_URL`), authenticated with
`SPARKWING_BILLING_TOKEN`, to open a session for that team. The browser is
sent to the page it returns. When Stripe confirms the payment, the checkout
service verifies the webhook and grants the credits to the team the session
names through `POST /api/v1/credits/grants`, keyed on the payment id so a
redelivered webhook grants once. The service holds a token carrying only
`credits.grant`, which the operator mints with
`sparkwing cluster tokens create --type service --principal checkout-service --scope credits.grant`; it records paid
grants, reverses and holds by payment, and reads the ledger's units, and every
other route refuses it. A controller with no `--billing-url` sells no credits
and answers the checkout route with `503` and
`"code": "checkout_unavailable"`.

A team's balance holds at most $5,000, and the cap is held when a checkout
opens. The controller adds the balance, every checkout of the team still
open and the new purchase, and refuses the purchase before any session opens
when they pass the cap, with `409` and `"code": "balance_cap"` naming the
balance, the open checkouts (`open_micro`), the amount and the cap. A checkout
counts from the moment it opens until its payment is granted or its session
expires, about half an hour later, so two owners racing for the last room
cannot both open one. The grant that follows a verified payment is never
refused by the cap, because the money has already moved; the balance can pass
the cap only by a payment settled after its session expired. A `paid` grant is
at most one purchase, $500. The grant route still refuses an operator's `free`
grant that, with the checkouts still open, would pass the cap. A replay of a grant already written is answered as usual.

Purchases are final, so a refund is the operator's decision and is made by
hand. `sparkwing cluster credits refund --payment <pi_...>` takes back what the
purchase still has on the ledger, in the team it funded, through
`POST /api/v1/credits/reversals`, and prints the Stripe dashboard page where
the operator then issues the money back. The controller never moves money, and
a refund issued in Stripe alone changes nothing on the ledger. The whole
purchase is reversed even when its credits were spent, so the balance can go
below zero and the team's metered claims stop until it is funded again. A
reversal never takes back more than its payment paid, and running the refund
twice reverses once.

A chargeback holds the team. When Stripe reports a dispute opened on a
purchase, the checkout service logs an alert and holds the team the payment
funded through `POST /api/v1/credits/freezes`: its metered claims are refused
with `402` and `"code": "credits_frozen"` while work already running finishes,
and Team -> Billing says so. A dispute won releases the team. A dispute lost
reverses the purchase the way a refund does and leaves the team held until the
operator releases it with
`sparkwing cluster credits freeze --team <slug> --release`.

## Retained storage

Runner time stops costing when a node ends; retained bytes keep costing while
they are kept, so they are billed from the same ledger. An installation bills
storage only once an operator prices it: `storage_rate_micro_per_gb_day` is
what one gibibyte kept for one day costs and `storage_free_allowance_bytes` is
what every team keeps unbilled, and both are zero until written, so an
installation that sets neither writes no storage charge.

What the storage charge bills is run-event payload bytes, which is the one
thing the controller durably stores and already measures per team. Artifact
content, cache entries and hosted logs are not metered on this release, because
the controller never sees their sizes: a runner writes artifact blobs straight
to the object store and the controller holds only the manifest digest. A
published manifest counts one object against the team's quota and carries no
bytes, so it costs nothing here.

Each storage pass bills every team holding bytes for the interval since it was
last billed. The pass runs on the controller's hourly storage timer, so the
meter's error is one pass interval of bytes held and released between two
passes, not a whole day of them. A team the ledger has never billed is stamped
with the current instant and billed from the next pass, so pricing storage
never bills for the past, and so a team's first bytes cost one pass before the
meter reaches them. A team that drops to nothing keeps no watermark, and an
interval is never billed for longer than the bytes in it have been held, so an
idle stretch is not charged against whatever a team stores next. A pass bills
one team's bytes under one token principal, so two teams that both label a
token `ci` are metered apart. A team whose retained runs all carry no creation
date bills nothing for that interval. Each
team's watermark moves by compare-and-set, so two controllers on one database
bill an interval once whatever either clock says, and a clock that steps
backwards bills nothing rather than billing twice.

The amount is `bytes x rate x seconds` divided by a gibibyte-day, truncated
toward zero, so a fraction of a micro-credit is never billed and truncation
forgives at most one micro-credit per team per pass. Three gibibytes retained
against a one-gibibyte free allowance for one day is two gibibyte-days, which
at 333,333 micro-credits a gibibyte-day is 666,666 micro-credits, and that rate
is 2,000 credits a gibibyte-month, the published $0.10.

The charge is a `storage` row naming the team, the bytes it billed and the
interval it covered, so `sparkwing cluster credits show` and `credits history`
separate retained bytes from runner time. It carries no cpu class and no
per-second rate, because neither priced it.

Each team's allowance is how many retained bytes it asked to keep. The pass
expires its oldest finished runs above the allowance before it bills, and it
never bills for more than the allowance, so the allowance is both what a team
keeps and the most it pays for; an allowance of zero keeps everything and caps
nothing. Read and write one with
`sparkwing cluster credits allowance --principal NAME --gb N`, which needs
`admin`. Only that verb writes it: rewriting a team's quota leaves the
allowance where it stands.

An empty balance is a hard cut, the same as it is for runner time: a write that
would grow a team's retained bytes is refused with `402` and a reason naming
the team and the balance, and the pass drains retained bytes down to the free
allowance. That drain takes only runs whose retention window has already
elapsed, so non-payment never removes anything inside the window, and an
installation with no retention window drains nothing.

The cut is late by up to one storage pass, at most an hour: a balance that
reaches zero between passes keeps accepting writes until the next one, and the
team's storage quota is what bounds how much can land in the meantime. For a
team that held nothing before, the stamp pass comes first, so the cut can take
two passes to engage.

## Runner classes

A class is a whole number of cores with the memory that comes with it, and it
is the unit a pipeline buys. The ladder is 2, 4, and 8 cores, and each class
carries 4 GiB of memory for each of its cores: 8 GiB at two cores, 16 GiB at
four, 32 GiB at eight. A node takes the smallest class that covers both halves
of what it pinned, so a pin of three cores and 20 GB takes the 8-core class
because the 4-core class carries only 16 GiB. The pod is created with cpu and
memory requests and limits equal to its class, so a node never outgrows the
class it is billed at.

The 2-core class runs on the warm pool and starts in seconds. A larger class
starts a Kubernetes node of its own, which takes one to two minutes during the
preview, and the controller refuses every warm claim and offer for it so it
cannot land on a machine it shares. `warm_cpu_class_cores` is the largest class
the warm pool serves, 2 by default; zero starts a node of its own for every
class. Local claim-mode agents are unmetered and claim by their labels as they
always have.

Each class above the warm one names the band of machines it runs on. The 4-core
and 8-core classes select nodes labeled `sparkwing.dev/cpu-band: small` and
tolerate the `sparkwing.dev/cpu-band=small:NoSchedule` taint. The operator's own
node selector and tolerations are merged in and win on this key, so a cluster
that pins Jobs its own way keeps doing so. A cluster that serves classes above
the warm one needs a node pool carrying that label and that taint; without one
the pod is unschedulable and the node fails as below, with the scheduler's own
message.

Concurrency in the band is whatever the pool's own cpu limit fits. A Job that
finds no room waits for a machine to free rather than failing, because a full
fleet is a condition that clears. A Job whose shape no machine in the pool
could hold still fails after the five-minute wait below, because waiting cures
nothing.

A claim answers with the class it billed, as `credit_cpu_class_cores` and
`credit_cpu_class_memory_bytes`, and the Job is created from those two figures,
so the pod shape and the bill agree whichever ladder the operator priced. A
claim that names a node carries `sizes_to_class` to say it creates the node's
executor at that class; a metered claim without it is held to the warm class,
and an unmetered one may not set it at all, so a customer's local agent can
never route a class node to itself. The metered token is the operator's own
pool, and class routing trusts it: the warm loop and the Job dispatcher share
one process and one token, so the controller takes the flag at its word until
the Job builder moves server-side and the pod shape is the controller's own.
A runner cpu or memory ceiling below the billed class fails the node naming
both, because a customer must never be billed for a class the pod cannot get.
A pod no node accepts is one of two things, and the runner tells them apart by
measuring the pod's requests against the `allocatable` of the pool machines its
own node selector admits. No machine in the pool could hold the pod even when
empty, which is what a class larger than the cluster provisions looks like: the
node fails after five minutes with the scheduler's own message, as it always
has. A machine of the right shape is running and simply busy: the node queues,
its `status_detail` reads `queued: the runner fleet is full` with the
scheduler's message, a `capacity_queued` event opens the wait, and it runs the
moment a machine frees. The queue is bounded at nine minutes, one minute short
of the claim the dispatcher holds and does not renew, after which the node
fails `queue_timeout` with a `capacity_queue_timeout` event. A pool running no
machines at all is not a full pool, so a pod waiting on one that has yet to be
launched keeps the five-minute window, which is long enough for a machine to
boot. A node whose `.Requires()` labels no runner advertises, and which no
fallback may take, fails after five minutes with a `node_unmatchable` event
naming the labels, the labels the fallback does advertise, and the class, so
work the fleet cannot serve ends where an operator can see it.

The class is stamped on the node when it becomes ready, so the queue read
leaves the classes a warm runner may not take out of the scan entirely and a
2-core node behind thousands of larger ones is still claimed at once.

## Compute guards

The guards bound what the controller starts before the ledger bills it. Each
is one non-negative integer, and zero is unlimited, so a controller that sets
none behaves as it did before the guards existed.

| Guard | Bounds | Measured against |
|-------|--------|------------------|
| `max_concurrent_runners` | cloud runners held at once | one principal |
| `max_global_runners` | cloud runners the controller holds | every principal |
| `runner_alarm` | cloud runner count that logs a warning, set below the ceiling | every principal |
| `max_run_seconds` | wall-clock seconds a run may hold cloud runners for | one run |
| `max_nodes_per_run` | nodes a run may carry, which is what bounds a dynamic fan-out | one metered principal's runs |
| `max_runs_per_hour` | runs created in the last hour | one metered principal |
| `max_global_nodes_per_run` | nodes a run may carry | every run |
| `max_global_runs_per_hour` | runs created in the last hour | every run |
| `min_cron_interval_seconds` | shortest interval a controller schedule may declare | every controller schedule |
| `runner_scale_base` | runners one step of paid credit buys, at most a million; zero uses `max_concurrent_runners` | one principal |
| `runner_scale_step_credits` | paid credit that earns one more base, at most 200 billion; zero turns scaling off | the controller's ledger |
| `runner_scale_ceiling` | most a scaled cap may reach, at most a million; zero uses `max_global_runners` | one principal |

A cloud runner is a claim a metered token holds, so the runner guards count
exactly the work credits pay for. `max_nodes_per_run` and `max_runs_per_hour`
are a team's own budget: they measure the principal whose token created the
run and apply only while that principal holds a metered token, so local work
and unmetered runners pass them untouched. The `max_global_*` pair is the
operator's own ceiling and counts every run whichever principal created it.
The hourly window counts runs by their creation stamp, so a run that has
already finished still occupies the budget until it ages out of the hour.

### Scaling the per-principal runner cap

`max_concurrent_runners` scales with what the controller loaded recently, so a
customer that has paid for capacity gets it and one that has paid nothing
cannot spawn a thousand pods. The cap is the base plus one more base for every
`runner_scale_step_credits` of `paid` credit granted in the last 30 days, held
under `runner_scale_ceiling`. The base is `runner_scale_base`, or
`max_concurrent_runners` when that is zero; the ceiling is
`runner_scale_ceiling`, or `max_global_runners` when that is zero. With a base
of 100, a step of 5000 credits and 15000 credits loaded, a principal is held to
400 runners.

Every scaling setting is zero by default, which holds each principal to the
static `max_concurrent_runners`, and the rule applies only while that guard is
set. Scaling only ever raises that guard: a ceiling below it is ignored.
`runner_scale_base` and `runner_scale_ceiling` are capped at a million runners
and `runner_scale_step_credits` at 200 billion credits, ten million dollars, so
a typo cannot mint a cap. The step is written in whole credits, so schema v56
multiplied a step written when a credit was a cent by 200, keeping its dollar
value.

`free` credit earns nothing and a payment ages out after 30 days. A refund is a
`reversal` grant naming the payment's reference, and it is matched to that
payment rather than to its own date: a refund settled after the window still
takes back the payment that bought the cap, and refunding a payment that has
already aged out leaves this month's payments alone. The ledger belongs to the
controller and a controller serves one team, so every metered principal on it
derives the same cap.

The derivation is held for a minute so a claim costs no ledger query, and any
grant or reversal retires it at once. A ledger the derivation
cannot read holds the principal to the static `max_concurrent_runners` and
names the failure in the controller log. `max_global_runners` is checked first,
so the controller's own ceiling still refuses a claim a scaled cap would have
allowed.

`GET /api/v1/compute-limits` reports the result as `usage.derived_runner_cap`
with the `usage.recent_paid_micro` it was read from, which is the window's paid
grants less the reversals of them, and `sparkwing cluster limits show` prints it
as `DERIVED RUNNER CAP`.

Work a guard refuses answers `429` with `"code": "compute_limit"` naming the
guard, its ceiling and what was measured, and a `Retry-After` saying how soon
to ask again. The run records a `compute_limit_blocked` event that `sparkwing
runs status` prints on its `guard:` line; a refusal is recorded against a run
the refused principal owns, and a guard that names no principal records
nothing and reaches the operator through the log. A claim refused this way
leaves the node ready and the runner keeps polling. A run that passes
`max_run_seconds` loses its node on the next heartbeat: the node fails with the
reason `compute_limit`, its claim is released, and the heartbeat answers `409`.

`min_cron_interval_seconds` is measured over a schedule's next fires rather
than its text, so `*/5 * * * *` and `0,5,10,...` both measure five minutes. It
is checked when a repository arms its schedules and again when the tick is
about to fire one, so a guard set after arming still binds.

`sparkwing cluster limits show` prints every guard with the cloud runners in
use, per principal and in total. `sparkwing cluster limits set --name G --value
N` sets one guard and needs `admin`; a value of zero removes it.

## Scopes

The scope constants live in `pkg/controller/auth.go`; the full route-to-scope
mapping is in the generated [api-reference.md](api-reference.md):

| Scope             | Unlocks                                                                                           |
|-------------------|---------------------------------------------------------------------------------------------------|
| `runs.read`       | GET `/api/v1/runs`, `/runs/{id}`, `/runs/{id}/nodes`, `/runs/{id}/events`, `/trends`, `/agents`, `/queue/state`, `/credits`, `/credits/history`, `/compute-limits`, per-node metrics GETs, and similar deployment-wide reads. `/runs/{id}` alone also admits a `nodes.claim` or `triggers.claim` token holding a live claim on that run |
| `runs.write`      | POST `/api/v1/triggers`: starting new work. `/gitcache/refresh` fetches any repository with the operator's cache credential, so it takes `admin`; the CLI warms the cache with it before a trigger and proceeds without it |
| `runs.control`    | POST `/runs/{id}/cancel`, `/runs/{id}/retry`, `/runs/{id}/nodes/{id}/bounce`, `/runs/{id}/nodes/{id}/release`, and the cron writes (`/crons/repos`, `pause`, `resume`, `run`, `disarm`, `override`): acting on a run or schedule somebody else started |
| `nodes.claim`     | POST `/nodes/claim`, `heartbeat`, the per-node write routes, GET claimed node data, GET the claimed run and trigger, and read-only Git proxy routes scoped to a live claimed run |
| `logs.read`       | GET on logs-service (`/api/v1/logs/*`, `/api/v1/logs/search`)                                      |
| `logs.write`      | POST + DELETE on logs-service (`/api/v1/logs/{runID}/{nodeID}`, `/api/v1/logs/{runID}`)            |
| `triggers.read`   | GET `/api/v1/triggers`, `/triggers/{id}`, `/triggers/spawned-child`. `/triggers/{id}` alone also admits a `nodes.claim` or `triggers.claim` token holding a live claim on that run |
| `triggers.claim`  | POST `/api/v1/triggers/claim`, `/triggers/{id}/heartbeat`, `/triggers/{id}/done`, and GET the live claimed trigger and its run. The heartbeat and the done name a trigger, and each is bound to the claimant that trigger's row records |
| `runs.state`      | POST `/api/v1/runs`, `/runs/{id}/finish`, `/runs/{id}/plan`, `/runs/{id}/nodes`, `/runs/{id}/events`, per-node `start`, `finish`, `deps`, `status`, the offer-round routes `mark-ready`, `revoke-ready`, `finalize-ready`, `auto-retry/reset`, the slot routes `/concurrency/{key}/acquire`, `heartbeat`, `release`, `holder`, `resolve`, and PUT `/pipelines/{name}/profile/pin`. Every write naming a run is bound to a run the caller owns; the pin names a pipeline and is bound to a live claim on a run of it |
| `secrets.read`    | GET `/api/v1/secrets/{name}`, resolved against the pipeline of the run the caller holds a claim in |
| `approvals.write` | POST `/api/v1/runs/{id}/approvals/{nodeID}` (approve / deny a gate)                                |
| `team.admin`      | Administering the caller's own team: rename it, change roles, remove members, invitations, and revoking any of its runner tokens. A team owner holds it; it reaches no other team |
| `credits.grant`   | The hosted checkout service's scope: POST `/api/v1/credits/grants` for `paid` grants only, POST `/api/v1/credits/reversals`, POST `/api/v1/credits/freezes` naming a payment, and GET `/api/v1/credits/units`. It reaches no other route, and only the operator mints it; no team's token may carry it |
| `admin`           | tokens / users / secrets CRUD, the token metering marker, credit grants, the compute guards, run delete, gitcache seed, warm-pool checkout / return / heartbeat, and the two cross-run concurrency routes `force-release` and `cancel-waiter` -- see [api-reference.md](api-reference.md) for the per-route mapping |

Scope checks are set membership. `admin` is a superset -- any handler's
scope check passes if the principal carries `admin`.

A runner needs `nodes.claim`, `triggers.claim`, `runs.state`, `secrets.read`,
and `logs.write`. That set claims work, drives the run it claimed from plan to
finish, reads the secrets its pipeline owns, and ships logs. It mints no token,
reads no user, lists no secret, cannot start or retry a run of its own
choosing, and cannot read cached source for an unclaimed run. A pool replica that executes
already-created nodes still needs `runs.state`, because `start`, `finish`, and
event append are its own writes; it can drop `triggers.claim` when a separate
dispatcher claims triggers.

The set carries neither `runs.read` nor `triggers.read`, and a node process
opens with exactly those two reads: `GET /api/v1/runs/{id}` and
`GET /api/v1/triggers/{id}`. Both admit a caller holding a live claim on that
run in place of the scope, so the claim the runner already took is what opens
them. Add `runs.read` only to give a token the deployment-wide view.

A warm-pool dispatcher hands nodes to a pool on that same set. It opens and
closes each offer round while holding the run's trigger claim, so the readiness
routes admit it without an `admin` token.

A route can narrow a field below its route scope. The node dispatch reads
(`GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch` and `/dispatches`) admit
`runs.read`, but fill `env_json` only for an `admin` principal. Every reader
still gets `redacted_keys`, the names the snapshot dropped as credentials.

## Claim ownership

Scope decides which routes a token may call; the claim decides which node it
may write. `POST /api/v1/nodes/claim` binds the claim to the **claiming
token**: the controller records that token's prefix segment alongside the
principal name and the client-supplied `holder_id`. The prefix is what the
gate matches on, because it is unique per token while a principal name is a
free-form label two tokens may share; the name stays for display. Afterwards
the per-node write routes require that token plus the exact holder,
membership, reservation, and claim generation while the lease is unexpired.
A missing fence gets `403` with `"error": "claim_required"`; an expired or
stale fence gets `409 Conflict`. Another runner token is refused, and
`POST /runs/{id}/nodes/{nodeID}/heartbeat` answers `409` unless the token, the
principal, and the holder id all match. `admin` bypasses the check, which is
what lets a dispatcher mark a node ready, start it, and finish it.

The lease is an authorization window, so the claimant does not choose how long
it lasts. `lease_secs` above the server cap of 10 minutes is clamped, on the
claim and on every heartbeat; a runner renews well inside that.

`POST /api/v1/triggers/claim` binds the same way and increments a claim
generation. A trigger's id is the id of the run it creates, so trigger-driven
node mutations carry that exact generation and are accepted only while the
same token holds the live claim for that run. A stale generation gets `409`.

`POST /api/v1/triggers` requires more than `runs.write` when it names
`parent_run_id`. A node-spawned child carries the exact live claim for
`parent_run_id` and `parent_node_id`; a trigger-spawned child carries the exact
live trigger generation for `parent_run_id`. Another principal, another token
with the same principal name, a stale generation, a different parent node, or
no claim cannot attach lineage or inherit repository provenance. `admin`
retains its operator override.

`GET /api/v1/runs/{id}` accepts `runs.read` or a live `nodes.claim` or
`triggers.claim` owner of that run. `GET /api/v1/triggers/{id}` likewise
accepts `triggers.read` or either live claim. Claim-scoped access expires with
the lease and never widens list routes.

Run-definition writes -- run create and finish, plan snapshot, node create, and
run-level events -- require the source trigger's exact live generation.
Per-node writes accept either the node's exact live claim or that source
trigger generation. A run heartbeat likewise accepts one exact live node claim
from the run or the source trigger generation. A runner with `runs.state` but
without the applicable fence gets `403 claim_required`; a stale generation
gets `409 Conflict`.

An assisted executor acknowledges its claim generation and next monotonic
attempt ordinal immediately before each job-body invocation. Node log appends
must carry that started ordinal in addition to the exact claim fence. The logs
service validates it against the controller and stores it in an immutable
attempt substream. Trigger-owned node logs carry the trigger generation and
started attempt ordinal; trigger-generation-only logs are reserved for the
coordinator's `_compile` output.
Ordinary node reads return executor and attempt attribution but remove holder
and reservation values; claim responses still return the fence the winner must
present.

`PUT /pipelines/{name}/profile/pin` is the one `runs.state` write that names a
pipeline instead of a run, and a pin becomes a hard Kubernetes limit for every
later run of it. The caller must hold a live claim on some run of that
pipeline, so a token executing one pipeline cannot pin another's. `admin`
bypasses, which is what lets a dispatcher pin a pipeline it is not running.

The two reads a node process opens with, `GET /api/v1/runs/{id}` and
`GET /api/v1/triggers/{id}`, run the ownership check the other way: a caller
without `runs.read` or `triggers.read` is admitted when it holds a live claim
on that run, and refused with `403 missing_scope` otherwise.

`mark-ready`, `revoke-ready`, and `finalize-ready` take `runs.state` plus the
live claim on the run's trigger, the same gate `auto-retry/reset` carries. A
node claim never satisfies them: readiness is a dispatcher decision, and the
dispatcher is whoever claimed the trigger. A caller that does not hold that
claim gets `403 claim_required`, and a run with no trigger row answers `404`.
`admin` bypasses.

The slot routes under `/api/v1/concurrency/{key}/` -- `acquire`, `heartbeat`,
`release`, `holder` and `resolve` -- take `runs.state` plus a live claim on the
run the request names, so a pipeline that declares a concurrency group or a
memoized node runs on the runner scope set. `acquire` and `resolve` name their
run outright; `heartbeat`, `release` and `holder` name a holder, and the
controller reads the run off that holder's row. A caller holding no live claim
on that run gets `403 claim_required`, and so does a holder whose lease has
already lapsed, because a lapsed row proves nothing about who is calling. The
two routes that act on rows another run owns, `force-release` and
`cancel-waiter`, stay `admin`. `admin` bypasses all of it.

A `nodes.claim` token also reaches only the runs it is working on. The node
read routes (`GET nodes/{id}`, `nodes/{id}/output`, `nodes/{id}/bounce`) and
`POST /runs/{id}/heartbeat` answer `403 claim_required` unless the caller holds
an unexpired claim on some node of that run. `admin` bypasses; so does
`runs.read` on the reads, which already grants the wider view through
`GET /runs/{id}/nodes`.

Node mutations validate the exact fence in the same transaction as the write.
If the lease expires or another generation wins while an old executor is
paused, the old write cannot land. An append already accepted by the log
service remains confined to the old attempt substream rather than entering the
replacement's log.

The execution view (`GET /api/v1/runs/{id}?include=secret_values`) follows the
same rule: it returns plaintext argument values to an `admin` principal, or to
a `nodes.claim` principal holding an unexpired claim on one of the run's nodes.
A trigger claimant may read the run record but receives redacted secret values
until it holds a node claim.
A controller serving unauthenticated returns **plaintext**, because the whole
API is open in that mode and handing a runner `***` as a real argument value
would corrupt the run rather than protect it. Once authentication is on, a
request that carries no principal is refused.

## Secret ownership

A secret carries an owning pipeline, or none. Store one with
`sparkwing secrets set --name DEPLOY_KEY --file ./key --pipeline deploy-web
--profile prod`. A secret stored with neither `--pipeline` nor `--shared`
answers `admin` callers only; `--shared` opens an unscoped secret to **every
run in the cluster**, so reserve it for values that are genuinely shared, such
as a registry pull token.

The scope is the pipeline and not the repository, because a run's repository is
a string its submitter typed: the product grants nothing on it. Two teams may
name the same repository and it means nothing either way.

`GET /api/v1/secrets/{name}` resolves differently per principal:

- An `admin` principal reads any row. `?pipeline=<name>` selects a pipeline's
  row, `?run=<id>` selects the pipeline of that run, and without either the
  unscoped row answers.
- A `secrets.read` principal without `admin` cannot name a pipeline. It names
  the run it is executing with `?run=<id>`, and the controller answers only
  when the caller holds that run's claim; the name then resolves against that
  run's pipeline, falling back to an unscoped row only when that row is shared.
  A caller holding no claim reads nothing. A caller holding claims in one
  pipeline may omit `?run`; holding claims in two, it must name the run.

So one runner token cannot lift another pipeline's deploy credential by asking
for it by name, and a token working two runs cannot read the wrong one's
credential by accident. `GET /api/v1/secrets` (the list) and the secret writes
stay `admin`; the list carries each row's pipeline and shared flag.

Token creation validates scopes against that same set: a scope the
controller does not honor is rejected with a `400` naming the offending
scope and the valid set, so a typo fails at mint time instead of
producing a token that authenticates and then fails every scope check.
A token with no scopes is still legal; it just unlocks nothing.

Per-endpoint scope annotations live in `pkg/controller/server.go`. If
you add a new route, annotate it with `requireScope`.

`GET /api/v1/auth/whoami` is authenticated by the middleware like any
other route but carries no scope check, so any valid token can read
back its own principal, kind, scopes, and prefix. The logs service uses
it to resolve tokens against the controller. It shows as `public` in
[api-reference.md](api-reference.md) because that table is generated
from `requireScope` wrappers -- there, `public` means no scope check,
not no authentication.

## Teams and sign-in

A controller holds one team, `default`, unless it runs with a signed
multi-team license. The license is one line,
`base64url(payload).base64url(signature)`, where the payload is JSON naming
`features` (`multi-team`), `issued_to`, `issued_at` and `expires_at` (RFC
3339), and the signature is Ed25519 over those payload bytes. The controller
verifies it against a public key compiled into the binary and reads it from
`--license-file` or from `SPARKWING_LICENSE`. A missing, malformed, expired or
wrongly signed license is logged at startup and leaves the controller holding
one team; it never stops the controller starting.

A multi-team controller always requires authentication. A request with no
credential would act as the operator of `default`, so the license turns token
auth on even while the tokens table is empty, and such a request gets 401. With
no token yet, only Google or GitHub sign-in sessions are accepted; supply the first admin
token with `--bootstrap-admin-token-file` (`SPARKWING_BOOTSTRAP_ADMIN_TOKEN`).

With the license and a Google OAuth client (`--google-client-id` or
`SPARKWING_GOOGLE_CLIENT_ID`, `SPARKWING_GOOGLE_CLIENT_SECRET`, and the
dashboard callbacks in `--oauth-redirect-uris` or
`SPARKWING_OAUTH_REDIRECT_URIS`), the dashboard offers Google sign-in. The controller
runs the server half of a PKCE flow: `POST /api/v1/auth/oauth/google/start`
returns the authorize URL, state and verifier for a redirect URI on the
allowlist, and `POST /api/v1/auth/oauth/google/exchange` redeems the code,
verifies the ID token (signature against Google's published keys, issuer,
audience, expiry, `email_verified`) and opens a session. The dashboard keeps
the state and verifier in a `__Host-` cookie and checks the state at its
callback, which is what proves the same browser finished the flow.

GitHub sign-in works the same way through `POST /api/v1/auth/oauth/github/start`
and `/exchange`, configured with `--github-client-id` (or
`SPARKWING_GITHUB_CLIENT_ID`) and `SPARKWING_GITHUB_CLIENT_SECRET` under the same license and redirect allowlist.
It asks for `read:user user:email` only, keys the identity on GitHub's numeric
account id so a renamed login keeps its account, and trusts only the primary
email GitHub has verified, never the profile's public email.

A Google identity joins an existing user only when Google and that user both
hold the email verified, and never when that user already has a different
identity from the same provider; a GitHub identity joins a Google user the same
way. A second account from one provider on one address is a recycled address
or another person, so it gets its own user and the first user's claim on the
address is withdrawn. A user's email follows what Google asserts at each
sign-in. A user with no team gets a personal space: a team
whose only member is its owner, slugged from the email's local part, with the
smallest free integer appended on a collision. The user's active team is
stored on the user, so the next sign-in returns to it. One user creates at
most ten teams, the personal space included, and slugs such as `default`,
`app`, `api`, `auth`, `login`, `admin` and anything starting `demo-` are
reserved. Without the license, sessions opened by a Google sign-in stop
authenticating as well as new sign-ins.

A membership carries one role, and the role decides the session's scopes on
every request, so a demotion or removal bites on the user's next request:

| Role     | Scopes                                                                 |
|----------|------------------------------------------------------------------------|
| `reader` | `runs.read`, `logs.read`, `triggers.read`                              |
| `editor` | reader, plus `runs.write`, `runs.control`, `approvals.write`; mints runner tokens for the team |
| `owner`  | editor, plus `team.admin`                                              |

No role grants `admin`, which stays the deployment operator's scope. Nobody
grants a role above their own, and a team keeps at least one owner. An
invitation names an email and a role, expires after seven days, is used once,
and is accepted only by a signed-in user whose verified email is that address.
Every `/api/v1/team/...` route acts on the caller's active team, taken from the
session and never from the request, and an id belonging to another team
answers 404.

A runner token minted from team settings carries the runner scope set and
belongs to the team that minted it. It expires 90 days after it is minted, and
the runner-token list shows when. Removing a member revokes every runner token
they minted in that team, and demoting a member to `reader` revokes theirs,
both in the same step as the role change. A team holds at most 10 live runner
tokens, at most 50 open invitations, and creates at most 100 invitations a day;
withdrawing an invitation still counts toward that day. No token minted into a
team carries `admin`.

Any member mints a personal CLI token with `POST /api/v1/team/cli-tokens`,
from a signed-in session only; a bearer token cannot mint one. It is a user
token bound to the member and the active team, carrying the member's role
scopes without `team.admin`, so a reader's token only reads. It expires 90 days
after it is minted. Removing the member, or demoting them to `reader`, revokes
it with their runner tokens; an owner demoted to `editor` keeps it, since it
already carried only the editor's scopes. The member lists and revokes their
own CLI tokens and no one else's, and holds at most 10 live ones in a team.

Owning a secret means creating and deleting it, not keeping it from editors.
Anyone who can run a team's pipelines -- an editor or above -- can use the
secrets those pipelines read: they can change what a pipeline runs, and a
runner token they mint reads the secrets of every run it claims. This is the
same model as GitHub Actions, where anyone who can push a workflow can use the
repository's secrets. Grant `editor` only to someone you would hand those
secrets.

## Unauthenticated endpoints

Routes registered on the controller's outer router are matched before
the auth middleware runs, so they are open regardless of auth config:
the health and metrics probes (k8s httpGet probes and Prometheus
scrapes can't carry `Authorization`), the service-discovery endpoint
the runner uses to find the cache pod, the browser session endpoints
the dashboard uses to establish, validate, and end a session (login,
logout, session, and the Google sign-in start and exchange), the
capabilities report a signed-out dashboard draws its sign-in page from,
the bootstrap probe, and the GitHub webhook, which
is HMAC-verified instead of bearer-authenticated. The logs service
opens its health and metrics probes the same way. Every registered
route is listed in
[api-reference.md](api-reference.md).

With controller-backed dashboard login enabled, the browser authenticates
same-origin dashboard requests with its `HttpOnly` session cookie. The
dashboard validates that session before its server-side proxy adds the service
bearer to an upstream controller request; the service credential never enters
browser HTML or JavaScript. CLI and automation clients should authenticate
directly to the controller through a profile rather than send a bearer to the
browser-facing dashboard proxy.

## Dashboard authorization

The dashboard proxies a fixed list of controller routes: the run, node,
approval, agent, and trend reads the SPA renders, plus the trigger, cancel,
retry, debug-release, approval-resolve, and run-delete writes its buttons
issue. A second list covers the logs service and carries reads only, so the
browser cannot delete a run's logs or append a forged line through the web pod.
Every other path under `/api/v1/` answers `404` at the web pod and never
reaches the upstream, so a signed-in tab cannot mint a token, read a secret, or
create a user through the proxy. Both lists live in
`internal/web/proxy_routes.go`, and a test holds each entry to the scope
`pkg/controller/server.go` and `pkg/logs/server.go` register for that route.
A third list forwards the identity and team routes (`/api/v1/me`,
`/api/v1/teams`, `/api/v1/team/...`, invitation accept) with no dashboard
scope, because a membership role decides them and the controller resolves that
role on every request.

A browser session carries the scopes of the user who signed in. The proxy
checks them against the target route before forwarding, so an account holding
only `runs.read` reads runs and gets `403` on cancel. Create narrower accounts
with `sparkwing cluster users add --scope runs.read,logs.read`; omitting
`--scope` grants `admin`. The first-visit bootstrap account defaults to
`admin` and may carry more scopes beside it, but a scope set that omits
`admin` is rejected with `400`, and `sparkwing cluster users list` prints the
scope set of every account.

Under `--require-login` the web pod reaches the controller as the signed-in
user: the proxy and the pod's own run, node and event reads send
`Authorization: Session <id>` for that browser's session, and never the pod's
service token. On a multi-team controller a service token reads every team,
so the session, which belongs to one user and one active team, is the only
credential that keeps a browser inside its own team. The service token still
authenticates the logs service, which accepts bearers only, and the services
health probe.

Without `--require-login` there is no session, and the web pod's own service
token carries every request. It needs `runs.read` plus `logs.read`. Add
`runs.control` where the UI cancels, retries, or releases a debug pause,
`runs.write` where it submits a trigger, and `approvals.write` where it
resolves approval gates.

Deleting a run from the dashboard needs `admin`, because the controller
registers `DELETE /api/v1/runs/{id}` at `admin`: the signed-in account must
carry it, or, without `--require-login`, the web pod's token. Without it the
dashboard button reports `delete needs the admin scope` and nothing is removed.

### Google and GitHub sign-in

Account sign-in needs a dashboard started with `--controller`, which forwards
every read with the signed-in user's own session. A dashboard started with
`--profile` or `--state` reads the operator's store directly, so it offers no
provider, answers `/auth/<provider>/...` with `404`, and treats any account
session, or any session acting for a team other than the operator's, as signed
out.

When the controller's `GET /api/v1/capabilities` reports `teams.enabled`, the
sign-in page offers "Sign in with Google" when `auth.providers` lists `google`
and "Sign in with GitHub" when it lists `github`, above the password form. Each
flow runs through the dashboard host: `GET /auth/<provider>/start` asks the
controller's `POST /api/v1/auth/oauth/<provider>/start` for an authorize URL, a
state and a PKCE verifier, keeps the provider, state and verifier in a
ten-minute `__Host-sw_oauth` cookie (`Secure`, `HttpOnly`, `SameSite=Lax`,
`Path=/`), and redirects to the provider. The provider returns to
`GET /auth/<provider>/callback`, which refuses a callback whose `state` does not
match that cookie or whose provider is not the one the flow started with, then
has the controller exchange the code and sets the dashboard session cookies.
Any other provider name answers `404`.

Register `https://<dashboard-host>/auth/<provider>/callback` as the OAuth
client's redirect URI (the authorization callback URL on GitHub). The scheme
follows the same TLS evidence as the CSRF origin check, so a dashboard behind a
TLS-terminating proxy needs `--trusted-proxy-cidrs` or `--hsts`. The host is the
one the browser used, so a local dashboard reached as `http://localhost:4343`
uses `http://localhost:4343/auth/google/callback`, and reaching it as
`127.0.0.1` sends a redirect URI the provider does not recognize.

`sparkwing-web --require-login` needs a controller session backend. Pass
`--controller URL`, or select a `--profile` whose `controller.url` is set. A
state-only configuration such as `--state-spec=postgres://... --require-login`
now fails at startup instead of silently serving an unauthenticated dashboard.
The controller URL must be an absolute `http` or `https` URL without embedded
credentials, a query, or a fragment.

Every dashboard response carries `Content-Security-Policy`
(`default-src 'self'` plus a per-response nonce for the bundle's inline
scripts), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and
`Referrer-Policy: same-origin`, and adds `Strict-Transport-Security` when the
request carries evidence of TLS: the listener terminates TLS itself, a peer
inside `--trusted-proxy-cidrs` forwarded `X-Forwarded-Proto: https`, or the
operator passed `--hsts` because TLS terminates somewhere that forwards no
trusted header. That same evidence decides the scheme the CSRF origin check
expects, so a dashboard behind an HTTPS proxy keeps `Secure` cookies without
the insecure-cookie override. The page reads its configuration from
`/sparkwing-runtime.js`, which carries the dashboard version and the login
mode. The service bearer stays in the web process and rides only its
server-side proxy, so the browser talks to one origin and `connect-src 'self'`
holds.

A dashboard that carries `--token`, runs without `--require-login`, and binds a
non-loopback address refuses to start, because every caller that reaches the
listener would drive the controller with that token. Pass `--require-login`,
bind a loopback address (chart: `web.addr`), or accept the exposure with
`--allow-unauthenticated-remote` (chart: `web.allowUnauthenticatedRemote`).
`--token` with no controller, logs, or profile backend is a startup error too:
nothing would authenticate with it, so the dashboard would serve
unauthenticated while the flag suggested otherwise.

Login throttling uses the TCP peer address and ignores forwarded headers by
default. When a reverse proxy fronts `sparkwing-web`, pass its egress networks
as `--trusted-proxy-cidrs=<CIDR,...>` or set the chart's
`web.trustedProxyCIDRs`. Sparkwing accepts `X-Forwarded-For` only from a trusted
peer and walks append-style chains from right to left until it reaches the
nearest untrusted address. Values to its left are ignored. A malformed entry in
the trusted suffix or an untrusted immediate peer falls back to the TCP peer.
IPv4-mapped CIDRs with prefix lengths `/96` through `/128` normalize to IPv4;
broader mapped prefixes fail startup. List proxy networks, not client networks.

The controller throttles `POST /api/v1/auth/login` the same way and takes the
same `--trusted-proxy-cidrs` flag, because it is reachable without going
through the dashboard. `sparkwing-web` forwards each browser's resolved
address to the controller as `X-Forwarded-For`, so the controller's list must
include the web pod's source; otherwise the controller ignores the header and
keys every dashboard login on the web pod's own address. See
[security.md](security.md#login-and-hashing-budgets) for its budgets, the
per-prefix bearer budget, and the argon2 memory bound.

The login, first-admin, and logout forms carry a CSRF token in both a
`SameSite=Strict` cookie and a hidden field. Sparkwing rejects a missing,
cross-origin, or mismatched token with `403` before it calls the controller.
Unsafe browser API requests (`POST`, `PUT`, `PATCH`, and `DELETE` under
`/api/v1/`) also require a same-origin request whose `X-CSRF-Token` header
matches both the browser's CSRF cookie and the live controller session.
The dashboard proxy removes browser cookies and the CSRF header before adding
its server-side bearer to controller or logs-service requests.
Logout also verifies the token against the live controller session. It clears
the browser session only after the controller confirms revocation; a controller
failure returns `502` and leaves the cookies in place so the browser does not
claim a session was revoked when it was not.

The dashboard resolves the controller session on every HTML, data, and API
request. Hashed files under `/_next/static/` contain no tenant data and do not
touch the session backend. Deleting a session on another web replica or at the
controller therefore takes effect on the next protected data request rather
than after a local cache expires. A controller `401` authoritatively clears the
browser session; a controller outage, `5xx`, or malformed response returns
`502` and preserves the cookies so a transient failure cannot log out every
user. The controller answers `5xx` when the state store or the session signing
key is unreadable, so only an unknown or expired session reaches the browser as
`401`. Browser redirects preserve the original path and query as one encoded
`next` value and accept only same-origin absolute paths.

The session and CSRF cookies are named `__Host-sw_session` and
`__Host-sw_csrf`. A browser honors that prefix only on a cookie that carries
`Secure`, names no `Domain` and is scoped to `/`, and the `Domain` refusal is
the point: host-only scoping stops a sibling host under the same registrable
domain reading these cookies but does nothing to stop one writing a same-named
cookie with a longer `Path`, which sorts first in the `Cookie` header and is
the one the server reads. The insecure-cookie escape below drops the prefix
along with `Secure`, because a browser discards a `__Host-` cookie that is not
`Secure`; on those deployments the names are `sw_session` and `sw_csrf`. A
custom browser client reads whichever name the deployment sets, preferring the
prefixed one.

Login cookies are `Secure` by default, so a login-required dashboard must be
served over HTTPS. A plain `http://localhost` port-forward can reach health
endpoints but cannot retain those cookies. For a loopback-only development
process, `SPARKWING_WEB_INSECURE_COOKIES=1` permits HTTP cookies. The dashboard
reads that variable once at startup and refuses a non-loopback bind with it
set. That check reads the bind address only: a proxy or sidecar in front of a
loopback bind still carries the cookie unencrypted to everything it publishes.
An operator who publishes the dashboard over plain HTTP through a proxy or
ingress adds `--allow-insecure-cookies-remote` to accept cookies that travel
without TLS; the chart renders that flag with the variable whenever
`ingress.allowInsecure` opts a TLS-less ingress in.

## First-visit signup

Controller authentication is enabled at startup when the tokens table contains
an active token. `--require-auth` makes startup fail when it does not, and
`--bootstrap-admin-token-file` (`SPARKWING_BOOTSTRAP_ADMIN_TOKEN`) puts the
first admin token in that table before the listener binds, so a provisioned
controller starts with both satisfied; see the
[security operator checklist](security.md#operator-checklist).

A freshly-installed sparkwing cluster has no users, so there is
nothing to log in *as*. While controller authentication is disabled,
browsing to `/login` on an empty cluster renders a "Create first admin"
form. Submitting it creates the first admin user via `POST
/api/v1/users`, then signs the new admin in automatically.

The bootstrap path is one-shot and latched: once any user exists,
the controller serves `{"needed": false}` to the probe, the login
page reverts to the standard sign-in form, and `POST /api/v1/users`
goes back to requiring an admin token. There is no way to reopen
the bootstrap path short of restarting the controller against a
freshly emptied database.

When controller authentication is enabled, the bootstrap probe reports
`{"needed": false}` and `POST /api/v1/users` requires an admin token even
if the users table is empty. An operator can use that token with
`sparkwing cluster users add` to create the first dashboard user. That
first account has to be an admin, so leave `--scope` off, or name a list
that contains `admin`; a narrower list is refused with `400` while the
users table is empty.

After the first admin is created, additional users are added via
`sparkwing cluster users add`. Pass `--scope` to bound what that account's
dashboard sessions reach; omitting it grants `admin`.

## CLI

Every `sparkwing` command that talks to a remote controller reads
connection info from a profile. Register one first:

```sh
# Register a prod profile (controller URL + admin bearer).
# --token-stdin prompts without echo on a terminal and reads a pipe otherwise.
sparkwing configure profiles add --name prod \
    --controller https://sparkwing.example.com \
    --token-stdin
```

`--token` accepts the bearer on the command line instead, but every process
on the machine can read it from the process list and the shell records it in
history. Use it only where a prompt or a pipe is impossible.

Then the tokens commands are terse:

```sh
# Mint a user admin token. Emits the raw token ONCE. Stash it.
sparkwing cluster tokens create --type user --principal alice --scope admin --profile prod

# List all active tokens.
sparkwing cluster tokens list --profile prod

# List including revoked, for audit.
sparkwing cluster tokens list --include-revoked --profile prod

# Revoke a token by its non-secret prefix.
sparkwing cluster tokens revoke --prefix swu_6cF9r2Kp --profile prod

# Look up metadata for a prefix.
sparkwing cluster tokens lookup --prefix swu_6cF9r2Kp --profile prod

# Rotate: mint a replacement, with a grace window before the old one 401s.
sparkwing cluster tokens rotate --prefix swu_6cF9r2Kp --grace 48h --profile prod
```

`--grace` is capped at 7 days; a larger value is rejected with `400`.
Revoking the old prefix cuts an open grace window short, so a rotation
you started before learning the old token leaked can still be stopped.

Deleting a user removes the user row, deletes every session that user
holds, and revokes every token whose principal is that name, in one
transaction. The token the delete request authenticates with is left
alone, so an operator whose admin token shares a name with the account
being deleted keeps working. Principals are free-form labels, so any
other token minted under the same name is revoked too, including one
minted for an unrelated caller; keep human account names and service
principal names distinct.

Profiles are the only path for targeting a remote cluster, which keeps
it hard to accidentally point at the wrong one. The
`SPARKWING_CONTROLLER_URL` environment variable is a fallback only for
the local dashboard dev flow, not for remote-cluster targeting.

## Argon2 parameters

Hash parameters (`pkg/store/tokens.go`):

- `time = 1`
- `memory = 64 MiB`
- `threads = 4`
- key length = 32 bytes

Measured on an arm64 laptop: ~8-15ms per `argon2.IDKey`. Token lookup on
the hot path is prefix-indexed + cached in-process for 60s, so argon2
only runs on cold lookups. Concurrent hashing is capped by a memory
budget, and a hash that waits more than 250ms for a slot is shed with
`503` and a `Retry-After` instead of queueing.

Requests that arrive while one token is being verified wait on that
verification rather than starting one of their own, so a fleet polling
the claim routes costs one hash per token per cache window however many
runners poll and however often. `sparkwing_auth_token_cache_total`
counts verifications by how the cache answered them (`hit`, `miss`,
`coalesced`) and `sparkwing_auth_hashing_rejected_total` counts the
hashes the memory budget shed; a climbing rejection count on ordinary
polling means the window is too short or the budget too small.

A claim or heartbeat that is answered `503` with a `Retry-After` is a
load signal, so the runner waits the header out, capped at 30 seconds,
and logs it at debug with at most one warning a minute. It does not
fail the poll or the node.

A claim that finds no work can also name the interval the runner should
wait before polling again, in the `X-Sparkwing-Poll-After` header. The
controller widens what it suggests with how long it has had no work,
up to `--idle-claim-poll` (default 5s), and stops suggesting anything the
moment work arrives or is handed out, so a queue that fills returns its
fleet to full cadence on the next poll. A runner caps what it accepts at
8 seconds whatever the header says, and the controller refuses to start
unless two of the longest wait its suggestion permits, spread included,
still fit inside `--placement-hold` and `--placement-liveness`, because a
runner silent past those windows stops counting as live for local-first
placement. The header is advice a
runner may only widen its own cadence to: it never polls faster than it
was configured to, it spreads its return with jitter so a fleet advised
together does not come back together, and a runner that ignores the
header polls exactly as often as it always did. A controller running a
limits profile enforces the suggestion instead, answering an early claim
`429` with the rest of the wait; see
[security.md](security.md#idle-poll-enforcement). A host's own admission
daemon and the loopback controller suggest nothing: they serve one
machine's runs, where a widened idle poll costs pickup latency and
protects no fleet.

## How long revocation takes to bite

The verified-token cache holds an answer for 60 seconds, keyed by the
token's public prefix and a SHA-256 of the whole credential, so the raw
token is never held in controller memory between requests. That window
is the outer bound on how long a revocation the replica did not serve
takes to bite.

Revoking a token, rotating one, and deleting a user all drop the
affected prefixes from the controller replica that served the request,
so the next request on that replica re-reads the row and gets `401`.
A cached entry also carries the row's `expires_at` and `revoked_at`,
which are rechecked on every hit, so a token that expires or whose
rotation grace closes mid-cache stops authenticating on time rather
than at the end of the cache window. An authentication that was already
reading the row when the revoke landed does not install its entry, so it
cannot put the revoked row back into the cache.

Three windows remain:

- **Other controller replicas.** Invalidation is in-process. A replica
  that did not serve the revoke keeps its cached entry for up to 60
  seconds. Restart or scale the controller to zero to close it now.
- **The loopback controller each run starts.** A local run serves the
  admin API from the orchestrator process over the same tokens table,
  behind its own 60-second cache that a controller restart does not
  reach. It is bound to loopback and exits with the run.
- **The logs service.** `sparkwing-logs` resolves callers through the
  controller's `whoami` and caches the answer for its own TTL (60s by
  default), on top of whatever the controller replica held. Its worst
  case is the sum of the two.

Sessions carry no cache: the controller reads the `sessions` row on
every request and the dashboard resolves the session on every protected
request, so deleting a session or a user logs that browser out on its
next request.

A session expires 12 hours after its last use and the controller renews it when
under an hour remains, but never past seven days from the moment it was
created. Reaching that age deletes the row and answers `401`, so the browser
signs in again. An embedder changes the cap with
`controller.Server.WithSessionMaxLifetime`.

## Extension points

- **OIDC / SSO**: not implemented. The `users` + `sessions` tables are
  shape-compatible; an OIDC callback can populate sessions directly by writing
  `sha256(session id)` into `sessions.hash` and keeping the raw id only in the
  browser cookie. There is no `csrf_token` column: Sparkwing derives that token
  per request as an HMAC of the session id under a key in `sparkwing_meta`.
- **Audit trail**: the principal name is stamped onto the OTel trace
  span. There is no dedicated audit database.
- **Per-user multi-tenancy**: principals are a free-form label. Adding a
  roles model is orthogonal and doesn't require a wire-shape change.
- **Fine-grained `admin` split**: `triggers.claim`, `runs.state`, and
  `secrets.read` carved the runner's work out of `admin`. What remains can be
  split further into `cache.write`, `locks.admin`, and similar when a real
  caller needs that narrower trust.
- **Execution capabilities beyond assisted nodes**: workstation and gateway
  agents keep their enrollment bearer in the supervisor and give each
  job-body child a process-lifetime loopback capability for its exact run,
  node, and acknowledged attempt log/lifecycle. Schema 30 is the internal
  current-node dependency; schema 31 adds the current-attempt mutation fence and
  durable grants for `Memoize`, `Concurrency`, `ToolSlot`, `RunAndAwait`,
  cross-pipeline references, and dynamic `SpawnNode` before this path can ship.
  Other execution modes retain their documented credential boundary. A future
  capability service could make the same split portable across container and
  process boundaries that do not share one supervisor.
