# Authentication + authorization

The controller's shared policy routes use the store's authentication snapshot
and binding checker by default. Each request reads current capability ancestry
and account membership. Browser writes require a configured CSRF verifier and return `policy_missing`
when it is absent. Verified local-peer and operator-console presentations need
their transport-specific adapters. Request headers and loopback addresses do
not supply those identities.

`GET /api/v1/queue` uses the shared capability policy. A member or verified
local peer needs `runs.read` for its active team. Queue groups and advertised
runner headroom belong to that team. Machine roots and work claims cannot read
the team queue. The former `/api/v1/queue/state` path is removed.

Run intake, plan acceptance, job status reads, job transitions, finish,
cancellation, retry and run/job renewal use the shared capability policy. The
controller resolves the resource's team before authorization and derives
actors and fences from the credential. Unknown or ended credentials get
`unauthenticated`. A live
credential without the grant or binding gets `forbidden`.

`GET /api/v1/runs/{run}/jobs` returns a status array from one committed
snapshot. It accepts member `runs.read` or the live run owner's `trigger.host`.
`GET /api/v1/runs/{run}/jobs/{job}` also accepts that job's live `data.read`
authority. An executor cannot list siblings or read another job through this
route. Both responses use the shared safe job status fields. They omit job
declarations, environment, claim identities and execution settings. The old
`GET /nodes` and `GET /nodes/{nodeID}` paths are removed.

`POST /api/v1/runs/{run}/jobs/{job}/cancel` accepts `runs.control` from a
member or `trigger.host` from the live owner of that run. The state transaction
requests cancellation of held jobs and preserves the holder's result rights.
The current job transition contract has no cancellation-reason field.

`GET /api/v1/runs/{run}/jobs/{job}/failure?generation=N` requires `data.read`
and a declared recovery read edge to that source generation. It returns the
persisted masked job failure, including its stage, or `null` when none was
recorded. Status text is never converted into a synthetic failure. Missing,
duplicate or nonpositive generation selectors return `bad_request` after
credential authentication.

`GET /api/v1/runs/{run}/snapshot` requires a live trigger owner with
`trigger.host` bound to that run. It returns one committed scheduling view,
including generated jobs and empty generator groups. Ordinary members and
work or plan claims cannot read this private owner view. Cancellation and
owner lease loss refuse new reads. A cancellation committed after
authentication appears in the returned snapshot. Accepted expansion bodies,
hashes and source generations travel in the same committed read. Replayed
expansion responses retain each generated job's current state and attempt.

Pipeline sizing uses `/api/v1/pipelines/{name}/sizing`. Members need
`runs.read`. Executable claims need `source.read` and the bound run's exact
sealed repository and pipeline. Bare runner and Actions roots cannot read
sizing. Bootstrap credentials can acquire source but cannot read sizing.
The `job` query selects one job. Omitting it selects the pipeline rollup.
An authorized missing measurement returns `not_found`.

Pins, observations, contention and waits use the `pin`, `observations`,
`contention` and `waits` suffixes on that path. They require `node.report`
and the same pipeline binding. Cancellation permits reporting while the
claim remains live. Member and admin grants cannot bypass this binding.
Writes require an authority-bearing sizing transaction adapter. Without one,
the controller returns `policy_missing` before storing a measurement.
The old `/profile` paths are removed.

Token revocation and member updates use the same policy router. A team token is
revoked at `DELETE /api/v1/tokens/{token}`. Deployment service/operator roots use
`DELETE /api/v1/operator/tokens/{token}` with `operator.tokens` or an operator
console session. Team routes derive their active team from the committed
credential. Header and query hints cannot select another team.

`GET /api/v1/team/members` returns the active team's member directory and
requires `runs.read`. An empty team returns `[]`.

`GET /api/v1/credits` and `GET /api/v1/credits/history` require a live member
or verified local peer with `runs.read`. The committed credential selects the
team. Machine roots, executable claims and bootstrap credentials cannot read
the team ledger. Both routes use the common error envelope and authenticate
before checking whether credit billing is enabled. An authorized caller gets
`not_found` when billing is unavailable. History retains the `limit` query
and returns empty grants and charges as arrays.

`PATCH /api/v1/team/members/{account}` accepts a role of `owner`, `editor` or
`reader`. `DELETE` on that path removes the membership. The store rechecks the
authenticated authority and updates the membership generation in the same
transaction. A missing member returns `not_found`. A last-owner change returns
`conflict`. Browser mutations require CSRF proof. Revocation stops descendants'
new controller operations, including those parented to a session. Previously
issued external credentials and signed URLs keep their documented lifetimes.

`POST /api/v1/runs` accepts a `request` and `idempotency_key`. The request names
its team. A bound job creating a child must name its own run and job as the
parent. Intake returns the committed run number and first seal. A here bearer
is returned once. Replaying intake returns the committed response without
another bearer.

`POST /api/v1/runs/{run}/plan` accepts `declarations`, `resolved` and `plan`.
The store validates the second seal against the persisted first seal, including
a retry's preserved source seal. Raw plan snapshots are no longer accepted.
Plan, job-transition and finish requests from ended claims require the exact
recorded body, actor and fence to replay. Mutations recheck authority inside
the store transaction. A lost fence gets `lock_held`.

Job transitions use `/api/v1/runs/{run}/jobs/{job}/transition`. Finish ends the
run and its owner unit together. Published local runs use a separate transition
edge and receive no executable owner. Cancellation carries a reason. Retry
carries an idempotency key and leaves source seals unchanged. Run/job heartbeat
responses carry the committed lease expiry, cancellation and watched jobs.
Every request uses JSON. Bodies cannot replace authority, holders or fences.

Job and step annotations, summaries and step transitions use the same policy
under `/api/v1/runs/{run}/jobs/{job}`. A job claim reports only its own job.
A trigger owner may report its run's jobs, subject to the transaction's holder
check. A local publisher uses its separate run-bound publication authority.
Cancellation permits reporting while the claim remains live. An ended claim
cannot write these reports.

Step start and skip take `step_id`. Step finish also takes `status` (`passed`,
`failed` or `cancelled`) and an optional masked inner error. The controller
derives the attempt from the claim or the persisted job, and the store owns
timestamps and stale-attempt refusal. Annotations and summaries accept at most
128 KiB of masked text. The old `/nodes/` reporting paths are removed.

## Data read authority

Download signing and batched presence use the shared capability policy.
A live claim needs its data grant and sealed run/repository/ref binding.
A repository reader needs existing repository authority. Cache grants,
ordinary member tokens and machine roots cannot substitute for either.
Each operation resolves current authority again and checks the read set
before reading metadata. Presence queries omit unreadable digests. Download
signing applies the team's daily byte cap and returns a 60-second URL for
one immutable object. The returned storage headers contain no controller bearer.

## Credential discovery

`GET /api/v1/auth/whoami` uses the shared policy verifier. It returns the
committed subject ID as `principal`, the subject kind as `kind`, and grants
as `scopes`. `team` is present when the credential has an active team.
`token_prefix` names a capability. Sessions and verified local peers omit it.
The response excludes session IDs, ancestry, claim fences and billing facts.
An empty grant set returns `"scopes":[]`.

`GET /api/v1/services` and `GET /api/v1/data/capabilities` use the same verifier
and require an active team. They announce configured services and data transfer
support. Availability is checked after authorization. An authorized caller gets
`not_found` when nothing is configured. Missing or dead credentials receive
`unauthenticated` regardless of service configuration. Bootstrap and eval
credentials cannot use these discovery routes or `whoami`.

All three routes return `Cache-Control: no-store`. Unsupported methods return
`method_not_allowed` with `Allow: GET`. Identity lookup requires an explicit
credential or a verified local peer. An address, query parameter or identity
header cannot supply authority.

The OIDC discovery document at `/.well-known/openid-configuration` and public
key set at `/.well-known/jwks.json` also use the policy router. Providers can
read them without a Sparkwing credential. Both retain their one-hour public
cache header. A controller without an issuer returns `not_found`, and
unsupported methods return `method_not_allowed` with `Allow: GET`.

## Generator expansion authority

`POST /api/v1/runs/{run}/jobs/{job}/expansion` uses the shared capability
policy. It requires `expansion.submit`, the bound source job and generation,
and a live owner or eval lease. Eval also requires a live controller ownership
lease. The ended source claim cannot submit an expansion.

The body supplies `source_generation` and exactly one of `body` or `failure`.
A successful empty expansion uses `"body":{"jobs":[]}`. A failure uses the
shared inner error object with `code` and `message`. The controller derives
the actor, holder and lease generation from authorization. Success and failure
reach the same store transaction, which rechecks authority before acceptance
and owns replay. Unsupported methods return a JSON `method_not_allowed` error
with `Allow: POST`.

## Token format

Member issuance and rewritten claim routes return opaque reference capabilities.
Controller capabilities start `swk_`. Node-host capabilities start `swn_`.
The stored authority determines subject, grants, binding and billing. A prefix
does not grant a role. The response's `prefix` is the first 16 characters and
is safe to use as an identifier. The controller keeps a SHA-256 presentation
digest and returns the reusable bearer only at issuance.

Member issuance uses `POST /api/v1/tokens` (see [team tokens](#team-tokens)).

## Metered runners

A token carries a `metered` marker that Sparkwing Cloud operators set with
the private `sparkwing-ops` tool. That marker is the only thing that decides
whether the work a runner does costs credits.
A claim-mode runner chooses its own labels, so a label saying "cloud" proves
nothing and metering never reads one.

A claim by a metered token reserves the minimum billable time, 60 seconds at
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
cheapest class. Each heartbeat charges whole seconds beyond the reserved
minimum and renews the lease in the same transaction. If the balance cannot
cover the elapsed time, the charge is recorded and the trigger and run fail;
the heartbeat answers `409` and the runner stops. A ledger error also answers
`409` without renewing the lease. Finish and lease expiry bill only the unpaid
tail, through the lease's end when it lapsed before the reap. A claim
requeued before its run started is refunded whole, including heartbeat
charges. If an earlier claim left an open reservation, a new claim waits for
operator ledger review; a named claim answers `409` with that reason. These
ledger rows carry the run id and an empty node id.

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
so a node that runs for four seconds pays for sixty. Past the minimum, billing
is per second with no rounding, so a node that runs for 61 seconds pays for 61.

A node whose machine never started gets its reservation back: a pod that never
came up, a claim reaped before its pod renewed it. A node the platform stops
before its execution starts gets back everything its claim billed, setup
included. That covers no machine of the class coming free (`queue_timeout`)
and a log store that refused or dropped the node's writes (`logs_auth`,
`logs_dropped`). A runner that stops renewing its claim, before or after
execution starts, ran until its lease ran out: when the reaper or agent-loss
recovery clears the claim, the seconds since its last charge are billed to the
lease's end, under the per-charge cap. Any other end before
execution is the pipeline's own and keeps its setup billed: a compile error, a
source fetch the repository refused, a cancellation, an out-of-memory kill. A
platform failure after execution starts is billed like any other finish.

Two bounds apply. The up-front reservation is one charge of the whole
60-second minimum; after it, no single charge bills more than the charge cap
(30 seconds by default), so a controller
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

A token with no marker is neither checked nor charged. A controller without a
signed metering license ignores even a previously marked token when it claims
work, and refuses requests to mint or mark a metered token.

## Credits

Credits are a Sparkwing Cloud and enterprise feature. The controller requires
the `metering` feature in its signed license. A signed `multi-team` license
includes metering, including licenses issued before `metering` was named.
Without either feature, credit and team billing routes return `404`, claims
never check a balance or write charges, and the dashboard omits Billing.
Customers who need metering can ask the Sparkwing maintainers for help running sparkwing-ops.

Cloud runner time is prepaid. One credit is $0.001, and that value is fixed:
prices change by moving rates, never by changing the credit. Amounts are
stored in micro-credits, a fixed money unit of 10^-8 dollars: 100,000 to the
credit, 1,000,000 to the cent and 100,000,000 to the dollar. A four-core
second costs 0.18 credits, a minute 10.8 and an hour 648 ($0.648), so ten
dollars (10,000 credits) buys a little over fifteen four-core hours. A charge
is a fraction of a credit, so the dashboard shows charges to a hundredth of a
credit and balances in whole credits, rounded down.

A second is priced by the node's cpu class. The rate table prices one class per
whole-core size, and a node is billed at the class it pinned, which is the class
the pod is given. Nothing a claimant says about itself reaches the price,
because a runner that priced its own work would bill an 8-core node at the
smallest class. A request above the largest class the table prices is refused
when the class is chosen, failing the node with `unpriced_cpu_class` and a
`credits_unpriced_class` event naming both sizes, rather than reserving credits
for a node no claim can pay for.

An installation that never set a table bills the default ladder, each class at
90% of the GitHub Actions Linux runner of its size: 2-core 9,000 micro-credits
a second ($0.0054 a minute), 4-core 18,000 ($0.0108), 8-core 33,000 ($0.0198).
`credit_rate_micro_per_second` is the four-core
entry of that ladder under another name. Once a table exists that setting is
derived: a `PUT` that names it, alone or beside `rate_table`, answers `400` and
says to write the table.
Sparkwing Cloud operators set the ladder through the private `sparkwing-ops`
tool. A stored table this build cannot read is an error on every credit read
rather than a silent return to the flat rate.

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

Sparkwing Cloud operators read balances and history and change pricing through
the private `sparkwing-ops` tool, and grant credits from the
[operator console](#operator-console). The controller's credit API routes
remain the authority.

## Buying credits

A team owner buys credits from the dashboard's Team -> Billing page, and any
member reads the balance, the price table, usage by run and the team's
purchases there, from `GET /api/v1/team/billing`. The minimum purchase is $10
and the maximum $500; the controller refuses any other amount with `400`.
Purchases are final and credits never expire.

The Stripe Checkout Session is created on the server, never in the browser,
so no caller chooses the team a payment funds. The dashboard posts only the
amount to `POST /api/v1/team/billing/checkout`, which needs an owner of the
active team. The controller takes the team from that session, refuses an
amount outside the range, and asks the hosted checkout service at
`--billing-url`, authenticated with the
`billing-token` credential, to open a session for that team. The browser is
sent to the page it returns. Stripe delivers payment events to
`POST /webhooks/stripe`. The controller verifies the exact body, signature,
timestamp and configured test/live mode before passing a verified event to
the store. The committed checkout supplies the team and credit amount.
Unpaid completions stay pending. Failed and expired sessions close without
granting credit. Unsupported authenticated events are acknowledged without
settlement. A failed settlement returns an error so Stripe can retry.

Configure the callback with `--payment-mode test` or `--payment-mode live`
and the private `stripe-api-key` and `stripe-webhook-secret` files in the
controller's credentials directory. Both modes use the same verifier and
settlement path. Missing or inconsistent credentials refuse startup.
An unconfigured callback returns `503 unavailable`.

The separate card and dispute service holds a service token carrying
only `credits.grant`; it records card outcomes, holds by payment, and reads the
ledger's units, and every other route refuses it. Those routes answer only a
service token carrying `credits.grant`: an admin token, a session, a user or
runner token and an anonymous caller are refused, the last even on a
controller that serves without credentials.

`credits.grant` is outside the capability grant vocabulary.
`POST /api/v1/tokens` rejects it with `400`. The remaining legacy rotation
route also refuses that service credential. The controller seeds the token from the `credits-token` file in its
credentials directory, as it seeds the bootstrap admin token. Generate the
value once and give the same string to the checkout service:

```bash
printf 'sws_%s' "$(openssl rand -hex 24)" > credits-token
```

The controller seeds each value once and records that it did, so a restart
with the same file writes nothing. Once that token is revoked, the value is
never seeded again, even after the revoked row is pruned: the controller logs
an `ERROR` naming the `credits-token` credential at every start until the file
holds a new value. To rotate it, put a new value in the file and in the
checkout service, restart the controller, then revoke the old prefix with
`sparkwing cluster tokens revoke`. A token stored under the file's prefix
before it was provisioned, such as one minted by an earlier release, is
adopted as it stands. A controller with no `--billing-url` sells no credits
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

A team also buys at most a set amount over 30 days, because a prepaid team
cannot spend more than it bought and a stolen card should buy little. A new
team buys up to $50 over 30 days and at most $50 at once; a trusted team buys
up to $500. The controller counts the team's payments of the last 30 days,
less refunds of them, plus its open checkouts, and refuses a purchase past the
limit with `409` and `"code": "purchase_limit"`. An unpaid checkout keeps
counting for three days after its session expires, because a session paid
just before expiry is granted when its webhook arrives, and Stripe retries a
webhook for up to three days. A team earns trust when its
oldest unrefunded payment is at least 30 days old, it has spent at least $50,
and it has never been held over a dispute. The operator grants or revokes
trust, with a reason, from the [operator console](#operator-console); a grant may
raise the limit to $5,000, and a revocation holds the team to the new-team
limits even when it would earn trust; `automatic` returns it to the rule. Team -> Billing shows the limit and what
remains of it.

Purchases are final, so a refund is the operator's decision, issued in the
Stripe dashboard. The refund reaches the ledger: the checkout service answers
`charge.refunded` by reversing exactly what each refund returned, under the
refund's id, through `POST /api/v1/credits/reversals` with `amount_micro`. A
reversal never takes back more than its payment has left, and replaying it
reverses once. The balance can go below zero, and the team's metered claims
then stop until it is funded again.

Every team spends within limits, counted in daily buckets of gross usage at
list price, promotional credit included. A new team may spend $50 over 30
days and $25 in one UTC day; a trusted team $500 and $200. An owner may set a
lower budget through `PUT /api/v1/team/billing/budget`, and owners are
mailed at 50, 80 and 100% of it. A claim past any limit is refused with `402`
and `"code": "spend_limit"`, and the run log's `credits_blocked` event names
the limit. Limits stop new work; running jobs finish within about a minute.
A refunded reservation counts against the day of its charge, so a refund
never frees room today.

A trusted team may add a card (`POST /api/v1/team/billing/card`, owners
only), which opens a Stripe setup page that asks for 3-D Secure and shows the
charge authorization. With a card on file the team buys no prepaid credit
(`409`, `"code": "card_on_file"`), spends any prepaid balance first, and may
then run up to twice its rung below zero. The controller's payment worker
charges the card off-session when the debt reaches the rung ($100), on the
first of the month for a smaller debt, and at once when trust is revoked.
Each charge has one attempt in flight at a time, and the attempt id is both
Stripe's idempotency key and the payment's `metadata.attempt_id`, so a retried
attempt finds the payment already made. Stripe emails the receipt. A declined
charge withdraws the card's credit, so new work waits, and is retried after
1, 3 and 7 days; an owner can pay it at once through
`POST /api/v1/team/billing/pay`, and a new card retries it at once. A team
with debt or a charge open cannot be deleted.

An early fraud warning gives each affected team its own hold, blocks the
card's fingerprint for every team, and refunds the payment; the refund then
reverses the credits as above. Debt is usage already consumed, so any card may
repay it through `POST /api/v1/team/billing/pay`: a payment of the open charge
by a card warned on another payment pays off that charge, repays what the
team owes at that moment, grants nothing beyond it, holds the team, and logs
`alert=card_payment_warned` with the repaid and unapplied amounts. Returning
the unapplied rest, or the whole payment, is the operator's decision; a refund
returns the unapplied rest first, which reverses nothing, and only what it
returns beyond that reopens the debt. Use the returned hold id to release
that hold. Existing warning holds keep their ids, and replaying
a warning does not undo an operator's release. Business events retain the
provider's warning id alongside the hold id.

A chargeback holds the team. Holds are one per dispute, and a team is held
while any of its holds stands: its metered claims are refused with `402` and
`"code": "credits_frozen"` while work already running finishes, and
Team -> Billing says so. When Stripe reports a dispute opened on a purchase,
the checkout service logs an alert (`alert=chargeback_opened`) and holds the
team the payment funded through `POST /api/v1/credits/freezes`. A dispute
lost reverses the purchase the way a refund does and holds the team too, so a
lost dispute holds it even when Stripe delivered the close before the open. A
dispute won logs `alert=dispute_won` and changes nothing: the checkout service
never releases a team, because one dispute's outcome says nothing about
another's. The operator releases a settled dispute's hold from the
[operator console](#operator-console). Replaying any
dispute event changes nothing, and a replayed hold never undoes a release. A
dispute is bound to the one payment and team its first hold named: a hold or
a lost-dispute reversal that names the same dispute for another payment is
refused with `409` and `"code": "dispute_conflict"` and logged as
`alert=dispute_conflict`. A hold takes the ledger lock a metered claim takes,
so no claim that read the team as not held commits after the hold.

## Operator console

The dashboard's `/operator` page lets the operator look up a team and act on
its billing from a browser: grant, revoke or reset trust, set or clear a
trusted team's overrides (the 30-day purchase limit, and optionally its daily
spend cap, the debt at which its card is charged and its Cloud node cap),
grant free credits up to $5,000, hold or release the team, and release a
payment dispute's hold once the dispute is settled. Its Waitlist tab lists the
accounts waiting on the [sign-up gate](#sign-up-gate), newest first, and
admits them. Each team action takes a reason and a confirmation step naming
the team and the effect.

Trust, overrides, free grants, holds and the waitlist are reachable only here.
No bearer token reaches them, an admin token included, and the checkout
service's `credits.grant` routes refuse every other credential, so a
standing credential grants no credit, changes no trust and releases no hold.

Console API actions require an operator-console session and session-bound CSRF
proof for writes. A live bearer, ordinary account session or local peer receives
`403 forbidden`. An unknown, expired or revoked session receives
`401 unauthenticated`. Trust, grant and hold events record the authenticated
account ID. A request body cannot select the actor.

Account/team deletion, installation unbinding and
credit-settings writes also use console-only routes under `/api/v1/operator`.
The former deployment-admin URLs are removed. The controller's
`--operator-accounts` list gates the dashboard page. API authentication uses the
shared route-policy adapters and fails closed when they are not configured.

Every action commits together with its business event, which names the
operator and the reason: `billing.trust_changed` for trust and the limit,
`credit.granted` for a grant, `team.frozen` and `team.unfrozen` for holds. A
grant carries a key, and a retried grant with the same key grants once. A limit
on a revoked team is refused until trust is granted again without one, so
raising a limit never undoes a revocation. Each trust change replaces every
override, so one left empty returns to its default. Unfreeze releases the
console's own holds and never a dispute's; a dispute's hold is released by
name with `hold` on `POST /api/v1/operator/teams/{team}/unfreeze`. A console
hold is a hold like a dispute's, so it also ends the team's automatic trust.

### Break-glass

When console sign-in is down, the operator holds or releases a team with the
controller binary itself, run in the controller pod:

```bash
kubectl exec -n "$NAMESPACE" "deploy/$CONTROLLER" -- \
  sparkwing-controller admin freeze --team acme --reason "abuse in progress"
kubectl exec -n "$NAMESPACE" "deploy/$CONTROLLER" -- \
  sparkwing-controller admin unfreeze --team acme --reason "resolved"
```

`unfreeze --hold ID` releases that one hold, a payment dispute's included.

It acts on the controller's state database directly, beside the running
controller, so the cluster access that reaches the pod is the credential and
the cluster's own audit log records who used it. Pass the
`--credentials-dir` the controller runs with when its state is in
PostgreSQL. `freeze` and `unfreeze` behave as the console's actions do and
commit the same `team.frozen` and `team.unfrozen` business events, with
`sparkwing-controller admin` as the actor and the reason given; each also
writes an `audit` line to stderr with `"principal_kind":"break-glass"`. Trust,
grants and the waitlist wait for the console.

## Retained storage

Storage uses the credit ledger. `storage_rate_micro_per_gb_day` prices one
retained gibibyte-day. `storage_free_allowance_bytes` is the amount each team
keeps unbilled. Both default to zero. A zero price disables storage billing.

The inventory is unique committed blobs plus persisted log-segment bytes.
Reference counts and historical replica usage cannot multiply that inventory.
A team's optional retained allowance caps it before the free allowance comes
off. Teams without quota rows receive no retained ceiling.

Each team's first sample establishes billing time. Later passes bill the
elapsed whole seconds, leaving fractional seconds for the next pass. The
maintenance lease, ledger debit and watermark update commit together, so
concurrent replicas cannot charge an interval twice. Failed ledger writes
advance no watermark. A backward clock charges nothing until it catches up.
Disabling the price resets the watermark, so resumed billing starts fresh.

The amount is `bytes x rate x seconds` divided by a gibibyte-day, truncated
toward zero. Three gibibytes against a one-gibibyte free allowance for one day
cost two gibibyte-days. At 333,333 micro-credits per gibibyte-day, that is
666,666 micro-credits.

Billing samples retained bytes on the hourly maintenance pass. Bytes removed
between samples are absent from the next sample. An idle gap is bounded by
the oldest surviving bytes' creation time. This is a sampled inventory rather
than an exact history of every byte's lifetime. Invalid inventory or policy
returns an error.

Storage charges name the team, bytes and interval. They carry no run, CPU
class or runner price. Run retention preserves the financial ledger and
recorded usage, so deleting a run never restores spent credit. Quota and
allowance configuration is described in [self-hosting](self-hosting.md#storage-allowances).

## Runner classes

A class is the unit a pipeline buys: a whole number of cores and the memory a
Job gets with them. The ladder is 2, 4, and 8 cores. A class names a machine of
N vCPU and 4N GiB, and a Job gets that machine less the system's own overhead,
the same way a hosted 2-core runner is a 2-vCPU virtual machine rather than two
reserved cores: about 5.4 GiB at two cores, 12.8 GiB at four and 27.6 GiB at
eight. A node takes the smallest class whose cores cover its cpu pin and whose
Job memory covers its memory pin, so a pin of three cores and 14 GiB takes the
8-core class because the 4-core class gives a Job 12.8 GiB. A memory pin above
the largest class's Job memory fails the node when it is claimed, naming both
sizes. The class sets the price. A node with a 0.25-core pin still pays for the
2-core class, including the 60-second minimum.

Where a node runs decides whether it gets the machine to itself. Before any
runner takes a node, a warm dispatcher offers it to claim-mode agents for up to
five seconds; a local agent is unmetered and claims by its labels as it always
has. A metered warm runner, one claiming nodes with the operator's pool token,
takes only classes up to `warm_cpu_class_cores` (2 by default; zero sends every
class elsewhere) and runs them on a machine it shares with its other nodes, so
those nodes do not get a machine of their own. Everything else becomes a
Kubernetes Job. A Job on a band pool, below, runs alone on a machine of its
class: its pod requests N minus half a core with no CPU limit, so it can use
every cycle the machine has spare, and the class's Job memory as request and
limit. That machine takes one to two minutes to start during the preview,
unless one its team just freed is still up. A Job off the band keeps the
runner's configured shape and shares nodes with its own team's Jobs.

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
could hold fails before the Job is created, because waiting cures nothing.

A claim answers with the class it billed, as `credit_cpu_class_cores` and
`credit_cpu_class_memory_bytes`, the latter the class's Job memory. A Job off
the band uses the pipeline's resource pin or measured profile for its requests, with 100m CPU and 128 MiB
memory defaults when no usable measurement exists. Limits retain the runner's burst settings
and operator ceilings. A claim that names a node carries `sizes_to_class` to
select the class-routed Kubernetes path; a metered claim without it is held to
the warm class,
and an unmetered one may not set it at all, so a customer's local agent can
never route a class node to itself. The metered token is the operator's own
pool, and class routing trusts it: the warm loop and the Job dispatcher share
one process and one token, so the controller takes the flag at its word until
the Job builder moves server-side and the pod shape is the controller's own.
An operator ceiling caps the pod request without changing the billed class.
A pod no node accepts is one of two things, and the runner tells them apart by
measuring the pod's requests against the `allocatable` of the pool machines its
own node selector admits. If no matching machine could hold the pod even when
empty, the node fails before Job creation with a clear error.
If none exists yet, the five-minute wait allows the pool to provision one.
A machine of the right shape that is simply busy causes the node to queue,
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
| `max_concurrent_runners` | cloud runners held at once | one team, across all its tokens |
| `max_global_runners` | cloud runners the controller holds | every principal |
| `runner_alarm` | cloud runner count that logs a warning, set below the ceiling | every principal |
| `max_run_seconds` | wall-clock seconds a run may hold cloud runners for | one run |
| `max_nodes_per_run` | nodes a run may carry, which is what bounds a dynamic fan-out | one metered principal's runs |
| `max_runs_per_hour` | runs created in the last hour | one metered principal |
| `max_global_nodes_per_run` | nodes a run may carry | every run |
| `max_global_runs_per_hour` | runs created in the last hour | every run |
| `min_cron_interval_seconds` | shortest interval a controller schedule may declare | every controller schedule |

A cloud runner is a claim a metered token holds, so the runner guards count
exactly the work credits pay for. `max_nodes_per_run` and `max_runs_per_hour`
are a team's own budget: they measure the principal whose token created the
run and apply only while that principal holds a metered token, so local work
and unmetered runners pass them untouched. The `max_global_*` pair is the
operator's own ceiling and counts every run whichever principal created it.
The hourly window counts runs by their creation stamp, so a run that has
already finished still occupies the budget until it ages out of the hour.

### The team-wide runner count

`max_concurrent_runners` counts every cloud runner a team holds, whichever of
its tokens claimed it, so minting more machine tokens gives a team no more
concurrency than its spend limits assume. It always applies: unset or zero
means 100 per team, never unlimited. An operator raises one granted team
past it with the Cloud node cap override in the
[operator console](#operator-console) (`runner_cap` on
`POST /api/v1/operator/teams/{team}/trust`), up to 10,000. Schema v87 replaced the
per-principal cap that scaled with recent payments, and deleted its
`runner_scale_*` settings. Only an operator sees the controller-wide runner
count, per-principal counts and `runner_alarm` state; team readers receive
none of those fields.

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
| `runs.read`       | GET `/api/v1/runs`, `/runs/{id}`, `/runs/{id}/nodes`, `/runs/{id}/events`, `/trends`, `/agents`, `/queue`, `/credits`, `/credits/history`, per-node metrics GETs, and similar team-scoped reads. `/runs/{id}` alone also admits a `nodes.claim` or `triggers.claim` token holding a live claim on that run |
| `runs.write`      | POST `/api/v1/triggers`: starting new work |
| `runs.control`    | POST `/runs/{id}/cancel`, `/runs/{id}/retry`, `/runs/{id}/nodes/{id}/release`, and the cron writes (`/crons/repos`, `pause`, `resume`, `run`, `disarm`, `override`): acting on a run or schedule somebody else started |
| `nodes.claim`     | POST `/nodes/claim`, `heartbeat`, the per-node write routes, GET claimed node data, GET the claimed run and trigger, and read-only Git proxy routes scoped to a live claimed run |
| `logs.read`       | GET `/api/v1/runs/{id}/logs`, `/runs/{id}/logs/{node}`, `/runs/{id}/logs/{node}/stream`, `/runs/{id}/logs/{node}/completeness`, `/runs/{id}/logs/search` and `/runs/grep` |
| `triggers.read`   | GET `/api/v1/triggers`, `/triggers/{id}`, `/triggers/spawned-child`. `/triggers/{id}` alone also admits a `nodes.claim` or `triggers.claim` token holding a live claim on that run |
| `triggers.claim`  | POST `/api/v1/triggers/claim`, `/triggers/{id}/heartbeat`, `/triggers/{id}/done`, and GET the live claimed trigger and its run. The heartbeat and the done name a trigger, and each is bound to the claimant that trigger's row records |
| `runs.state`      | POST `/api/v1/runs`, `/runs/{id}/finish`, `/runs/{id}/plan`, `/runs/{id}/nodes`, `/runs/{id}/events`, per-node `start`, `finish`, `deps`, `status`, the offer-round routes `mark-ready`, `revoke-ready`, `finalize-ready`, the slot routes `/concurrency/{key}/acquire`, `heartbeat`, `release`, `holder`, `resolve`, `cancel-waiter`. Every write naming a run is bound to a run the caller owns |
| `secrets.read`    | Releases no secret. A bearer holding it without `admin` or `team.admin` is answered `403 claim_required` on GET `/api/v1/secrets/{name}`; a node reads its run's secrets with its claim token. Tokens minted with it keep authenticating, and no team token or runner token is minted with it |
| `approvals.write` | POST `/api/v1/runs/{id}/approvals/{nodeID}` (approve / deny a gate)                                |
| `team.admin`      | Administering the caller's own team: rename it, change roles, remove members, invitations, and revoking any of its machine tokens. A team owner holds it; it reaches no other team |
| `credits.grant`   | The card and dispute service's scope: POST `/api/v1/credits/reversals`, POST `/api/v1/credits/freezes` naming a payment, the card routes under `/api/v1/credits/`, GET `/api/v1/credits/units`, and GET `/api/v1/credits/payments/{reference}`. It reaches no other route. Only the controller's `credits-token` credential provisions it; no API call mints or rotates it, and no team's token may carry it. These routes answer only a service token carrying it; `admin` does not stand in |
| `storage.count`   | The cache service's scope: POST `/internal/storage/reserve`, `/internal/storage/commit` and `/internal/storage/release` for any team's cache or logs, POST `/internal/downloads/charge`. It reaches no other route, and only the operator mints it, with `sparkwing cluster tokens create --type service --principal cache --scope storage.count`; no team's token may carry it. The cache reads it from the file `controller-token` in its `--credentials-dir` |
| `admin`           | tokens / users / secrets CRUD, the token metering marker, credit pricing, the sign-up gate's mode and limits, the compute guards, run delete, gitcache registration, and concurrency `force-release` -- see [api-reference.md](api-reference.md) for the per-route mapping |

Scope checks are set membership. `admin` is a superset -- any handler's
scope check passes if the principal carries `admin` -- except on the
`credits.grant` routes and the [operator console](#operator-console)'s
routes, which no admin token reaches.

A runner needs `nodes.claim`, `triggers.claim` and
`runs.state`. That set claims work, drives the run it claimed from plan to
finish, and uploads its nodes' log segments; the claim tokens its claims
return read the secrets the run's plan declares. The controller refuses to
mint the removed `logs.write` and `logs.delete` scopes; see
[the migration notes](migrations/_unreleased.md#node-logs-reach-the-controller-as-segments). It mints no token,
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
closes each claim round while holding the run's trigger claim, so the readiness
routes admit it without an `admin` token.

Registered secret string values are masked in child-run events before storage,
including values escaped by JSON. Numeric fields retain their types and precision.

For registered multiline secrets, each complete nonblank line is also masked
using literal matching. Identical short or common text elsewhere can therefore
be hidden. CRLF line endings are normalized for line matching; other whitespace
is retained. Arbitrarily truncated prefixes are not guaranteed to match.

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

Once a run finishes or cancellation is recorded on its run or node, the
heartbeat answers `409`. The runner stops its node and its child processes.

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

A runner acknowledges its claim generation and next monotonic
attempt ordinal immediately before each job-body invocation. Node log appends
must carry that started ordinal in addition to the exact claim fence. The logs
service validates it against the controller and stores it in an immutable
attempt substream. Trigger-owned node logs carry the trigger generation and
started attempt ordinal; trigger-generation-only logs are reserved for the
coordinator's `_compile` output.
Ordinary node reads return executor and attempt attribution but remove holder
and reservation values; claim responses still return the fence the winner must
present.

### Claim tokens

Every claim answers with a `claim_token`, and the process that runs the
claimed work authenticates with it instead of the credential that claimed.
`POST /api/v1/nodes/claim` and `POST /api/v1/runs/{id}/nodes/{nodeID}/claim`
return a node claim token, and `POST /api/v1/triggers/claim` and
`POST /api/v1/triggers/{id}/claim` a trigger claim token. The runner keeps its
own token for claiming, heartbeating and its own calls; a pooled node's
node host and a Kubernetes Job run on the node's claim token, and the Job spec
carries nothing else that authenticates.

A node claim token acts as the runner that made the claim, confined to that
claim:

- It reaches the routes under the claim's run (`/api/v1/runs/{run}`,
  `/api/v1/runs/{run}/...`, `GET /api/v1/triggers/{run}`), the run's
  secrets, the slot routes for its own `run/node` holder and
  `GET /api/v1/services`.
- For `Ref.Get`, `TryGet` and `RunAndAwait` it also reaches, in its own
  team: `GET /api/v1/pipelines/{name}/latest`, any run's
  `GET /api/v1/runs/{id}/nodes/{nodeID}/output`, `POST /api/v1/triggers`
  naming its run and node as the parent (and as `retry_of` only a child that
  node started), `GET /api/v1/runs/{id}` with no query for such a child, and
  `GET /api/v1/triggers/spawned-child` for its node under its run or the run
  that run retries. Each is served with the one scope its route needs for
  that request alone, and another team's pipeline, run or output answers as
  missing. The child is claimed like any other run and never holds the
  parent's claim token. Anything else answers `403 claim_mismatch`.
- It reads any node of its run but writes only to its own node. Of the
  run-level writes it makes only a node's own: events, the run heartbeat, the
  cache grant, the run's workload OIDC token, the run's source credential and
  the run's Git proxy. Run
  create and finish, plan, node create and the readiness routes answer
  `403 claim_mismatch`.
- Every request carries the claim's own holder, membership, reservation and
  generation, whatever fence headers it sent, and never a trigger generation,
  so it never acts as the run's dispatcher.
- It holds `nodes.claim` and `runs.state` where the runner holds them, and
  never `admin`, so the claim's fence binds it even when an operator token
  claimed. `runs.state` is what its node's log segment uploads need.
- It stops when the claim ends: the node finishes, the lease lapses, the node
  is claimed again, or the runner's own token is revoked or expires. A
  request after that answers `403 claim_ended`, or `401` for a dead runner
  token.

A live trigger, plan or job capability uses `source.read` and its run binding
at `POST /api/v1/runs/{run}/source-credential`. A bare runner root cannot use
this route. The store checks current enrollment and the machine owner's consent,
then commits the release audit before decryption. A job loses source access
when execution starts. Missing or ended credentials receive 401. Live authority
without the required grant or binding receives 403. See
[Git credentials](git-credentials.md).


The two reads a node process opens with, `GET /api/v1/runs/{id}` and
`GET /api/v1/triggers/{id}`, run the ownership check the other way: a caller
without `runs.read` or `triggers.read` is admitted when it holds a live claim
on that run, and refused with `403 missing_scope` otherwise.

`mark-ready`, `revoke-ready`, and `finalize-ready` take `runs.state` plus the
live claim on the run's trigger. A
node claim never satisfies them: readiness is a dispatcher decision, and the
dispatcher is whoever claimed the trigger. A caller that does not hold that
claim gets `403 claim_required`, and a run with no trigger row answers `404`.
`admin` bypasses.

The slot routes under `/api/v1/concurrency/{key}/` -- `acquire`, `heartbeat`,
`release`, `holder` and `resolve` -- take `runs.state` plus a live claim on the
run the request names, so a pipeline that declares a concurrency group or a
memoized node runs on the runner scope set. `acquire` and `resolve` name their
run outright; `heartbeat`, `release` and `holder` name a holder, and the
controller reads the run off that holder's row. `cancel-waiter` names its run
in the request body. A caller holding no live claim on that run gets
`403 claim_required`, and so does a holder whose lease has
already lapsed, because a lapsed row proves nothing about who is calling. The
`force-release` can drop another run's superseded holder and stays `admin`.
`admin` bypasses all of it.

A `nodes.claim` token also reaches only the runs it is working on. The node
read routes (`GET nodes/{id}`, `nodes/{id}/output`) and
`POST /runs/{id}/heartbeat` answer `403 claim_required` unless the caller holds
an unexpired claim on some node of that run. `admin` bypasses; so does
`runs.read` on the reads, which already grants the wider view through
`GET /runs/{id}/nodes`.

Node mutations validate the exact fence in the same transaction as the write.
If the lease expires or another generation wins while an old executor is
paused, the old write cannot land. An append already accepted by the log
service remains confined to the old attempt substream rather than entering the
replacement's log.

`GET /api/v1/runs/{run}` always returns display-safe inputs, even when a live
claim reads it. `include=secret_values` cannot reveal plaintext arguments.
Missing or invalid classification redacts every value. Execution uses the
private awarded wire. Status reads require member `runs.read`, or a live
claim with `source.read` or `trigger.host` bound to the run. Verified local
authority follows the same policy. Missing or dead credentials receive `401`.

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
- A `team.admin` principal reads its own team's rows the same way, a
  management read by name. A masked row answers it `403 write_only`.
- Any other bearer, a runner's token or a `secrets.read` token included, is
  answered `403 claim_required`, even while it holds the run's claim: a node
  reads with its claim token.
- A claim token (a controller-dispatched work node's, a runner's node claim
  or a runner's trigger claim) names its own run with `?run=<id>` and reads
  only a name the run's plan declares, resolved against the run's pipeline
  and falling back to an unscoped row only when that row is shared. Each
  release is recorded as a `secret_released` event on the run, naming the
  node for a node's claim. The pipeline's code declares its secrets, as in GitHub
  Actions, so push access to the repository is the trust boundary; fork pull
  requests never run on this path. A plan is refused when a declared name
  breaks the secret-name grammar, it declares more than 100 names, or a
  required name resolves to no row the run may read, and a
  read of an undeclared name is answered `403` `secret_undeclared` and logged
  as an `audit` record with `event=secret_undeclared` and the claim's token
  prefix. A team-shared secret is readable by any of the team's opted-in
  repositories whose pipeline declares its name; scoping a shared secret to
  chosen repositories is a future feature.

So one runner token cannot lift another pipeline's deploy credential by asking
for it by name, a claim reads only its own run's declared secrets, and every
node read leaves a `secret_released` event.

`GET /api/v1/secrets` requires live member or verified local authority with
`runs.read` or `team.admin`. It lists only the authenticated team's rows,
including their pipeline and shared flag. Masked values are omitted. Unmasked
config values are decrypted with their stored row binding. If a config value
cannot be opened, the request fails without returning a partial list. Missing
decryption returns `unavailable`. The response uses `Cache-Control: no-store`.
Machine roots, claims and bootstraps cannot read the inventory.

Secret creation and deletion require live member or verified local authority
with `team.admin`. Session mutations require CSRF proof. These routes use only
the authenticated team and return `Cache-Control: no-store`. Secret names must
pass the shared grammar on every write, including replacement. Masking defaults
to true. Missing encryption returns `unavailable`, and failed writes return no
secret value.

Embedded controllers supply `WithCredentialWriter` with transactions that
recheck authority before commit. A missing or foreign-team writer returns
`policy_missing`. The legacy tenant writer is not used as a fallback.

`POST /api/v1/tokens` rejects unknown or duplicate grants and caller-selected
authority fields with `400`. Issuance beyond current grants or expiry is
refused with `403`. An explicit empty grants array is legal and unlocks
nothing. Endpoints check the credential's audience, complete grant conjunction,
resource binding and liveness before entering their handler.

Identity and service discovery use the shared policy described in
[Credential discovery](#credential-discovery).

## Teams and sign-in

A controller holds one team, `default`, unless it runs with a signed
multi-team license. The license is one line,
`base64url(payload).base64url(signature)`, where the payload is JSON naming
`features` (`multi-team`, `metering`), `issued_to`, `issued_at` and `expires_at` (RFC
3339), and the signature is Ed25519 over those payload bytes. The controller
verifies it against a public key compiled into the binary and reads it from
the `license` file in the controller's `--credentials-dir`. A missing, malformed, expired or
wrongly signed license is logged at startup and leaves the controller holding
one team; it never stops the controller starting.

A multi-team controller always requires authentication. A request with no
credential would act as the operator of `default`, so the license turns token
auth on even while the tokens table is empty, and such a request gets 401. With
no token yet, only Google or GitHub sign-in sessions are accepted; supply the first admin
token as the `bootstrap-admin-token` credential.

With the license and a Google OAuth client (`--google-client-id`, the
`google-client-secret` credential, and the dashboard callbacks in
`--oauth-redirect-uris`), the sign-in page offers Google sign-in. The
controller runs the whole PKCE flow on its own origin: `GET /auth/google/start`
creates a state and verifier for a redirect URI on the allowlist, keeps them in
a `__Host-` cookie, and sends the browser to Google. `GET /auth/google/callback`
checks the returned state against that cookie, which is what proves the same
browser finished the flow, then redeems the code, verifies the ID token
(signature against Google's published keys, issuer, audience, expiry,
`email_verified`) and opens a session.

GitHub sign-in works the same way through `GET /auth/github/start` and
`/auth/github/callback`, configured with `--github-client-id` and the `github-client-secret` credential under the same license and redirect allowlist.
It asks for `read:user user:email` only, keys the identity on GitHub's numeric
account id so a renamed login keeps its account, and trusts only the primary
email GitHub has verified, never the profile's public email. When the client id
and secret belong to a GitHub App, that App is also how a team connects its
repositories; see [GitHub App](github-app.md).

A sign-in never joins an existing user by email address. A first sign-in whose
verified address an existing user holds, from a provider that user has no
sign-in with, answers `409` and the person links the provider from account
settings instead (see [Linked sign-ins](#linked-sign-ins)). A second account
from one provider on one address, or one the user unlinked, is a recycled
address or another person, so it gets its own user and the first user's claim
on the address is withdrawn. A user's email follows what the
provider asserts at each sign-in, except through a sign-in the user linked
(see [Linked sign-ins](#linked-sign-ins)). A user with no team, other than one on the
[sign-up waitlist](#sign-up-gate), gets a personal space: a team
whose only member is its owner, slugged from the email's local part, with the
smallest free integer appended on a collision. The user's active team is
stored on the user, so the next sign-in returns to it. One user creates at
most three teams over the account's life, the personal space included, and
deleting a team does not give one back. A slug that ever named a team is
never registered again, and slugs such as `default`,
`app`, `api`, `auth`, `login`, `admin` and anything starting `demo-` are
reserved. Without the license, sessions opened by a Google sign-in stop
authenticating as well as new sign-ins.

A membership carries one role, and the role decides the session's scopes on
every request, so a demotion or removal bites on the user's next request:

| Role     | Scopes                                                                 |
|----------|------------------------------------------------------------------------|
| `reader` | `runs.read`, `logs.read`, `triggers.read`                              |
| `editor` | reader, plus `runs.write`, `runs.control`, `approvals.write`; mints machine tokens for the team |
| `owner`  | editor, plus `team.admin`                                              |

No role grants `admin`, which stays the deployment operator's scope. Nobody
grants a role above their own, and a team keeps at least one owner. An
invitation names an email and a role, expires after seven days, is used once,
and is accepted only by a signed-in user whose verified email is that address.
Every `/api/v1/team/...` route acts on the caller's active team, taken from the
session and never from the request, and an id belonging to another team
answers 404.

A team holds at most 50 open invitations, and creates at most 100 invitations
a day; withdrawing an invitation still counts toward that day.

With `--email-sender` set, the controller
emails each invitation through Amazon SES, taking credentials and region from
the AWS default chain; `--email-configuration-set` names the SES configuration set every
message carries. The email names the inviter by display name, the team, the
role and the accept link, and says the invitation expires in seven days; it
strips control and bidirectional-formatting characters from names and caps
each at 80 characters. One address, compared without regard to case, receives
at most 5 invitation emails in any 24 hours from every team together. The
count lives in a log of address digests that deleting a team does not touch.
Past the cap the invitation is still created and the response says
`email_sent: false`. Without a sender the controller logs each invitation
instead of mailing it. Either way the response carries `accept_url` for the
owner to hand on.

An owner deletes the active team with `DELETE /api/v1/team`, typing its slug
back as `confirm_slug`. The request closes the team in one transaction: its
members leave it and their sessions move to another team they belong to, every
token the team holds is revoked, open invitations are withdrawn, queued runs are
cancelled and running ones asked to stop, and the team no longer resolves for
any request. Another replica may accept a revoked token from its cache for up to
a minute, so a background pass, once a minute and at least two minutes after the
request, then deletes the team's log segments, its artifacts
and build cache through the cache service, and its rows in every team-owned
table, secrets and storage watermarks included. A cache grant minted before
the deletion stays valid for up to six hours, and a log upload URL for fifteen
minutes, so seven hours after the purge a second pass deletes the log segments
and the cache tree and sweeps the team-owned tables once more.
One replica works on a deletion at a time, under a five-minute lease it renews
before each step. A pass that fails leaves the deletion where it was with the
error recorded and the next pass starts that step again;
`GET /api/v1/me/team-deletions` shows the requester its state. The slug is
never registered again. The controller deletes the team's cache tree under
an operator grant it signs with its `cache-grant-key` credential, the key the
cache verifies grants with, and the purge deletes the team's segments under
every retention class's `teams/<team>/` prefix. An owner cannot delete their
only team; deleting their account does that.

A user deletes their account with `DELETE /api/v1/me`, typing their email back
as `confirm_email`, from a session signed in within the last 10 minutes; an
older session answers `403` with `reauth_required`. The account, its sign-in
identities, memberships and sessions go, and every token it minted in any team
is revoked. Teams the user is the only member of, their personal space
included, are deleted as above. In teams the user shares, rows that recorded
the user stay with the team and name `deleted user` instead: runs, triggers,
approvals, cron schedules armed, debug-pause releases, credit
grants, node and trigger claims, secrets, egress usage (bytes kept, merged per
month) and the text of event payloads. The match is on the account's email and
every email its sign-in identities asserted, never a linked sign-in's, in every
team, and on the principal of every token it minted, only in that token's team,
because another team may
use the same principal name for its own token; egress usage carries no team,
so it is relabeled for the emails alone. Credit
charges name the team, not a person, and are left as they are. A replica's
in-memory egress counter for the address can write it back once until that
month's rows are pruned. While the user is the last owner of a team that has
other members the request answers `409` and lists those teams, so they hand
ownership on or delete each first. The operator carries out a request that
arrived by mail with `DELETE /api/v1/accounts/{account}`, naming the account by
id or email, and deletes a team with `DELETE /api/v1/teams/{team}`; both need
`admin`.

Owning a secret means creating and deleting it, not keeping it from editors.
Anyone who can run a team's pipelines -- an editor or above -- can use the
secrets those pipelines read: they can change what a pipeline runs, and a
machine token they mint reads the secrets of every run it claims. This is the
same model as GitHub Actions, where anyone who can push a workflow can use the
repository's secrets. Grant `editor` only to someone you would hand those
secrets.

### Team tokens

`POST /api/v1/tokens` accepts `{grants, expires_at?}`. A signed-in account
session provisions a controller root for its own account and active team.
A member bearer creates a narrowed child that keeps the bearer's revocation
ancestry. The request cannot choose a subject, team, origin, metering, pool or
registration. Cookies require the session-bound CSRF proof.

The transaction checks current membership, grants and expiry. A provisioned
member root survives the issuing session ending, but loses authority when its
membership ends. Delegation cannot outlive or exceed its parent. Omitting a
delegation expiry inherits the parent's expiry. No automatic retry is permitted
because the raw bearer is returned once.

The response carries `capability`, `prefix`, `team`, `grants` and optional
`expires_at`. Expiry is an RFC3339 timestamp. The dashboard sends its selected
member grants to this route. The former member mint through
`POST /api/v1/team/tokens` is removed. That legacy route still handles machine
registration while the runner enrollment cutover is pending.

- A **machine** token is for a runner. Its scopes are a subset of
  `nodes.claim`, `triggers.claim` and `runs.state` that includes
  `nodes.claim` or `triggers.claim`. Minting one
  needs `editor` (`403` otherwise), and the request names the agent in `name`
  (1 to 63 letters, digits, `-`, `_` or `.`) and the repositories in `repos`,
  allowlist patterns that the returned connect `command` passes to the runner.
  The token's principal is `agent:<name>`, and a name a live machine or runner
  token already holds answers `409`. It expires once it goes 90 days unused:
  each use moves its expiry to 90 days past that use, written at most once an
  hour, and the token list shows when. A team holds at most 100 live machine
  tokens.

Token issuance uses the role held when the token is written, so a pending
request cannot restore authority after a demotion. No token minted into a team
carries `admin`.

`GET /api/v1/team/tokens` answers any member with the tokens they minted, of
either family, and adds every machine token of the team for an editor or
owner. Each row names `prefix`, `name`, `family`, `scopes`, `created_by`,
`created_at`, `expires_at`, `last_used_at` and `git_credentials`.
`DELETE /api/v1/tokens/{token}` revokes a token at once: the member who
minted it revokes their own, and an owner revokes any machine token. A token
the caller cannot see answers `404`, and one they see but may not revoke
answers `403`. The machine owner consents to stored git credential release with
`PUT /api/v1/tokens/{token}/git-credentials` (see
[Git credentials](git-credentials.md#who-receives-it)).

Membership changes advance the team auth generation in the same transaction.
Authentication rechecks the current membership and effective grants. Here
authority also ends when its requesting parent loses `runs.write`.

CLI tokens (`swu_`) and runner tokens (`swr_`) minted from team settings
before team tokens existed keep authenticating as they did until they expire
or are revoked. The same routes list and revoke them, and they count toward
the 10 member tokens and 100 machine tokens a team may hold.

### Linked sign-ins

An account holds sign-in methods and team memberships. Runs, secrets, credits
and machines belong to teams. A signed-in user adds a sign-in method to their
own account from **Account -> Linked sign-ins** in the dashboard, whatever
address the provider holds. Linking adds a way to sign in and moves nothing
from another account: Sparkwing does not combine accounts.

1. The page's **Link** button posts to `/auth/{provider}/link` with the
   session's CSRF token. The controller creates the provider's authorize URL,
   a state and a PKCE verifier, keeps the state and verifier in its flow
   cookie, and sends the browser to the provider. The state carries the
   controller's signature and names the account, the session and the
   provider, and expires after ten minutes.
2. The provider returns the browser to the sign-in callback,
   `/auth/{provider}/callback`, so no new redirect URI needs registering. The
   controller keeps the code in the flow cookie and moves on to
   `/auth/{provider}/link/complete`, a same-site request that carries the
   session.
3. `/auth/{provider}/link/complete` checks the state against the caller's
   account and session, records it as used, redeems the code, and attaches the
   provider account by its stable subject: Google's `sub`, GitHub's numeric id.

The controller refuses, and changes nothing, when the provider account is
already attached to any account (`identity_linked_elsewhere`, or
`identity_already_linked` for this one), or when the account already has a
sign-in from that provider (`provider_already_linked`). A refused link returns
the browser to **Account -> Linked sign-ins**, which names the refusal. A user with a
second Sparkwing account either keeps both, invites one into the other's team
and switches teams with the team switcher, or deletes the other account and then
links its sign-in.

A linked sign-in never changes the account's email, at the link or at any later
sign-in through it, and does not withdraw another account's claim on its
address. A provider account joins an existing account only through this link
flow, never by its address. A first sign-in whose verified address an existing
account holds, from a provider that account has no sign-in with, answers `409`:
"An account with this email already exists. Sign in the way you did before,
then link this provider from account settings." The message names none of the
account's providers, because the person holding the address may not be the
account's owner. A matching address shows only who holds it now, and a reassigned work
address would otherwise reach the previous holder's teams. A second provider
account from a provider the account already signs in with gets an account of its
own, as does one the account unlinked.

`DELETE /api/v1/me/identities/{provider}` unlinks a sign-in while the account
keeps at least one other (`409 last_sign_in_method` otherwise). It ends every
other session of the account, and the unlinked provider account then signs in
as a new account, even when it asserts the account's own address. Unlinking
GitHub leaves each GitHub App installation the user connected bound to its
team; connecting another installation needs a linked GitHub sign-in again.

Link start, link completion and unlinking need a session signed in within the
last 10 minutes (`reauth_required`; unlinking answers `403`). An account makes
at most ten link attempts a minute (`rate_limited`); the limit is per
controller replica.
The controller logs `identity.linked`,
`identity.unlinked` and `identity.change_refused` with the account, the provider
and, for a change, the provider's subject. `GET /api/v1/me/identities` lists
the account's sign-in methods and the providers it can link.

### Sign-up gate

Every new user costs a personal space, and a personal space holds a free
storage allowance, so the sign-up gate bounds how many a burst of new provider
accounts can take before the operator looks. It never touches a user that
already exists: a returning user, and a sign-in the user linked, sign in as
before in every state.

A new user meets the gate at its first sign-in. The gate admits it, which
creates its personal space, or places it on the waitlist. A waitlisted user
still gets an account and a session, so it can be admitted later without
signing up again, but it holds no personal space, cannot create a team
(`POST /api/v1/teams` answers `403`), and reaches no team route. It can accept
an invitation: a team that invites someone vouches for them, and they join that
team with the invited role while staying on the waitlist for everything else.
Because one admitted user could otherwise invite a farm of waitlisted accounts
into team scopes, each such acceptance counts toward the hourly and daily
limits as an admission, and a team that has bought no credits holds at most
`free_team_members` members (default 10; accepting past it answers `403`).
Members a team already holds are never removed, and a team with purchased
credits has no member limit.
`GET /api/v1/me` and the sign-in exchange report `"waitlisted": true`, and the
dashboard shows a waitlist page in place of the team views.

A new user is waitlisted, with the reason recorded on the account, when any of
these holds:

| Reason | Condition |
|--------|-----------|
| `deployment` | the controller runs with `--signup-gate=waitlist` |
| `operator` | an operator set the stored mode to `waitlist` |
| `hourly_signups` | the last hour already admitted `hourly_limit` new users (default 50); the gate closes itself |
| `daily_signups` | the last 24 hours already admitted `daily_limit` new users (default 500); the gate closes itself |
| `free_tier_closed` | the deployment's free storage reports `closed`; the gate reopens when it does |
| `free_tier_unreadable` | the free-tier source failed; the gate fails closed and logs `signup.free_tier_unreadable` |
| `github_account_age` | a GitHub account younger than `github_min_account_days` (default 7), or one whose creation date GitHub did not state |

A velocity closure is stored with the source `hourly_signups` or
`daily_signups` and stays closed until an operator reopens it, so a burst that
pauses does not reopen the gate on its own. Reopening restarts both windows
from that moment, so the burst already dealt with does not close it again.
Only admissions count toward a limit: new users admitted, and waitlisted users
who accepted an invitation. A burst of waitlisted sign-ups, such as young
GitHub accounts, does not close the gate on everyone else. Every sign-up and
acceptance locks the one gate row before counting, so concurrent sign-ups at a
limit admit exactly the limit. A limit of `0` turns its check off.

The free-tier state comes from a source the deployment wires in. A multi-team
controller with none logs `signup.free_tier_unwired` once at startup and
treats the free tier as open. Google states no account age, so the age
check applies to GitHub alone.

The gate's settings require a deployment credential with `operator.limits` or an operator-console session. Console writes require CSRF proof:

| Route | Does |
|-------|------|
| `GET /api/v1/operator/signups` | the effective state (`open` or `waitlist`) and every reason holding it, the stored mode with its source, reason, setter and time, the free-tier state, the limits, and the counts of users created in the last hour and day and waiting on the list |
| `PUT /api/v1/operator/signups` | sets `mode` (`open` or `waitlist`, with an optional `reason`) and any of `hourly_limit`, `daily_limit`, `hourly_warn`, `github_min_account_days`, `free_team_members`; fields left out keep their values |

The operator lists and admits waitlisted users from the
[operator console](#operator-console)'s Waitlist tab, which no token reaches.
`GET /api/v1/operator/waitlist` lists them newest first, with the reason each
was waitlisted, 50 at a time (`?limit=` up to 1000, `?offset=`), and
`POST /api/v1/operator/waitlist/approve` admits `{"account_ids": [...]}` or
`{"oldest": n}`, at most 1000 at a time; each admitted user without a team
gets its personal space, and approving an admitted user again does nothing.

An admitted user's open session moves into its new space on its next
`GET /api/v1/me`. The controller logs `signup.approved` for each admission and
runs the approval notifier the deployment installed, which is where a
"you're in" email would be sent from. This build installs none and has no
mailer, so it logs `signup.approval_not_sent` instead and the person finds
their space at their next visit.

The controller logs `signup.velocity_warning` and counts
`sparkwing_signup_velocity_warnings_total` once each time the last hour's new
users cross `hourly_warn` (default 20), which is below the hourly limit so an
alert fires before anyone is waitlisted. It logs `signup.gate_closed` and counts
`sparkwing_signup_gate_closed_total` when a limit closes the gate, and
`sparkwing_signups_total` counts every new user by outcome and reason, with
waitlisted users admitted by invitation under `admitted`/`invitation`. Alert on
any increase in the closure counter, on the warning counter, on
`signup.free_tier_unreadable`, and on `GET /api/v1/operator/signups` answering
`"state": "waitlist"`.

## Unauthenticated endpoints

Routes registered on the controller's outer router are matched before
the auth middleware runs, so they are open regardless of auth config:
the health probe (k8s httpGet probes can't carry `Authorization`), the
service-discovery endpoint
the runner uses to find the cache pod, the session endpoints a client
uses to validate and end a session (`/api/v1/auth/session`,
`/api/v1/auth/logout`), the capabilities report a
signed-out dashboard draws its sign-in page from, and the GitHub webhook,
which is HMAC-verified instead of bearer-authenticated. The sign-in pages
(`/login`, `/auth/{provider}/start` and `/auth/{provider}/callback`) and the
dashboard bundle's static assets need no session either. The controller's
`/metrics` is not among them: its run counters name every team's
pipelines, so on the API listener it needs an `admin` bearer, and
`--metrics-addr` moves it to a listener of its own that serves it open.
Every registered
route is listed in
[api-reference.md](api-reference.md).

A signed-in browser authenticates its same-origin API requests with its
`HttpOnly` session cookie, and the controller resolves that session's
principal and scopes as it does for an `Authorization: Session` credential.
The cookie authenticates only a request that carries no `Authorization`
header of its own. CLI and automation clients authenticate with a bearer
through a profile.

## Dashboard authorization

The controller serves the dashboard on the listener that serves its API, so
the dashboard holds no credential of its own. Every page and API request
carries the signed-in browser's session, and the controller checks each
route's scope against that session as it does for any caller. An account
session carries the scopes of the account's role in its active team, so a
viewer reads runs and gets `403` on cancel; the session belongs to one account
and one active team, which keeps a browser inside its own team. An
operator-token session carries the scopes of the token it was opened with.

Whenever the controller enforces token auth, because a token exists or a
multi-team license is loaded, it sends a browser without a session to `/login`
for every page except the bundle's static assets. A controller with auth
disabled serves its pages without sign-in.

Log panes read through the controller's log routes, which need `logs.read`
and answer only for the caller's team. The controller serves each node from
its segments (see
[observability.md](observability.md#node-logs-kept-as-segments)).

Deleting a run from the dashboard needs `admin`, because the controller
registers `DELETE /api/v1/runs/{id}` at `admin`, so the signed-in account must
carry it. Without it the dashboard button reports `delete needs the admin
scope` and nothing is removed.

### Google and GitHub sign-in

The controller's `/login` page offers "Sign in with Google" when its sign-in
providers include `google` and "Sign in with GitHub" when they include
`github`, above the operator-token form. Each flow runs on the controller's own
origin: `GET /auth/<provider>/start` creates a state and a PKCE verifier,
keeps the provider, state, verifier and return path in a ten-minute
`__Host-sw_oauth` cookie (`Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`), and
redirects to the provider. The provider returns to
`GET /auth/<provider>/callback`, which refuses a callback whose `state` does
not match that cookie or whose provider is not the one the flow started with,
then redeems the code and sets the session cookies. Any other provider name
answers `404`.

Register `https://<console-host>/auth/<provider>/callback` as the OAuth
client's redirect URI (the authorization callback URL on GitHub), and list it
in `--oauth-redirect-uris`. The scheme follows the same TLS evidence as the
CSRF origin check, so a controller behind a TLS-terminating proxy needs
`--trusted-proxy-addr` or `--hsts`. The host is the one the browser used, so a
controller reached as `http://localhost:4344` uses
`http://localhost:4344/auth/google/callback`, and reaching it as `127.0.0.1`
sends a redirect URI the provider does not recognize.

Every response the controller's browser surface sends carries `Content-Security-Policy`
(`default-src 'self'` plus a per-response nonce for the bundle's inline
scripts), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and
`Referrer-Policy: same-origin`, and adds `Strict-Transport-Security` when the
request carries evidence of TLS: the listener terminates TLS itself, a request
accepted on `--trusted-proxy-addr` forwarded `X-Forwarded-Proto: https`, or the
operator passed `--hsts` because TLS terminates somewhere that forwards no
trusted header. That same evidence decides the scheme the CSRF origin check
expects, so a controller behind an HTTPS proxy keeps `Secure` cookies without
the insecure-cookie override. The page reads its configuration from
`/sparkwing-runtime.js`, which carries the dashboard version and the login
mode. The page calls the API on its own origin, so `connect-src 'self'`
holds.

`sparkwing serve` answers only loopback `Host` names and the hosts named in
its `--allow-origin`, so a page whose DNS name was rebound to `127.0.0.1` gets
`403`. It also refuses a browser request whose `Origin` is another site, a
cross-site subresource request, and a browser write whose body is not
`application/json`.

The controller refuses a write a browser sends from another site to any route
under `/api/`, whether or not it enforces auth, so a page a visitor opens
cannot drive an open controller from the visitor's browser. A `POST`, `PUT`,
`PATCH` or `DELETE` answers `403` with the reason when its `Origin` host
differs from the `Host` header, or when its `Sec-Fetch-Site` is `cross-site`
or `same-site`. The dashboard's own pages send a matching `Origin`, and a
client that sends neither header, such as `curl` or a script, passes
unchanged, because the attack needs a victim's browser. A proxy in front of
the controller must pass the browser's `Host` through, or same-origin writes
are refused too. Reads are not checked.

Login throttling uses the TCP peer address and ignores forwarded headers by
default. `sparkwing-controller` takes `--trusted-proxy-addr=<host:port>`, a
second listener that serves the same routes. On that listener alone, a
request's single `X-Real-IP` is the client address and its
`X-Forwarded-Proto: https` is evidence of TLS; on `--addr` both headers are
ignored, and `X-Forwarded-For` is never taken as a client address on either. Point the fronting proxy at the trusted
listener, make it overwrite `X-Real-IP` on every request (ingress-nginx does by
default), and let nothing else reach that port, for example with a
NetworkPolicy.

The controller keys login throttling, the per-prefix bearer failure budget, and the
`client_ip` of its audit records on that one address, and callers on `--addr`,
such as runners, key on their TCP peer. See
[security.md](security.md#login-and-hashing-budgets) for the budgets and the
argon2 memory bound.

The sign-in and logout forms carry a CSRF token in both a
`SameSite=Strict` cookie and a hidden field. The controller rejects a missing,
cross-origin, or mismatched token with `403` before it checks a credential.
Unsafe API requests (`POST`, `PUT`, `PATCH`, and `DELETE`) that the session
cookie authenticates also require a same-origin request whose `X-CSRF-Token`
header matches both the browser's CSRF cookie and the live session.
Logout also verifies the token against the live session. It clears the browser
session only after the session is deleted; a store failure answers `5xx` and
leaves the cookies in place so the browser does not claim a session was
revoked when it was not.

The controller resolves the session on every page and API request. The
bundle's favicons and hashed build assets contain no tenant data and skip the
session lookup. Deleting a session therefore takes effect on the next request
on any replica. An unknown or expired session answers `401` and clears the
cookies; an unreadable state store or session signing key answers `503` with
`Retry-After` and keeps them, so a transient failure cannot sign every user
out. Browser redirects preserve the original path and query as one encoded
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

Both sign-in cookies persist across browser restarts for 30 days. Their
presence does not extend a controller session: the controller checks the
session on every protected request and clears invalid cookies.

Session cookies are `Secure` by default, so a controller that requires
sign-in must be reached over HTTPS; a browser that does not treat
`http://localhost` as a secure origin drops them over a plain port-forward. An
operator who publishes the controller over plain HTTP passes
`--insecure-cookies`, which drops `Secure` and the `__Host-` prefix so a
browser keeps its session. The cookies then travel readable to every hop on
the path. The chart passes the flag whenever `ingress.allowInsecure` opts a
TLS-less Ingress in.

`--hsts` asserts that browsers reach the controller over TLS that terminates
somewhere forwarding no trusted `X-Forwarded-Proto`: the controller then sends
`Strict-Transport-Security` and requires an `https` origin on
cookie-authenticated writes. The chart passes it whenever the Ingress has
`ingress.tls`.

## Signing in

Dashboard sign-in is Google or GitHub, where a multi-team license and an OAuth
client offer them, or an operator token. Sparkwing keeps no passwords.

An operator token is a user token holding the `admin` scope, such as the
bootstrap admin token below or one minted with `sparkwing cluster tokens
create --type user --principal NAME --scope admin`. Paste it into the `/login` form and the
controller opens a browser session for its principal. The session stores no
copy of the token's scopes: every request reads the token's scopes and team,
so revoking or expiring the token signs its browsers out on their next
request. A token without `admin`, and every runner or service token, answers
`403` and opens no session. Failed attempts spend the same per-prefix and
per-client budgets a bearer does.

Controller authentication is enabled at startup when the tokens table contains
an active token. `--require-auth` makes startup fail when it does not, and
the `bootstrap-admin-token` credential puts the first admin token in that table before the listener binds, so a provisioned
controller starts with both satisfied; see the
[security operator checklist](security.md#operator-checklist). That token is
how the first operator signs in, by API or by the sign-in form. A controller
with authentication disabled serves its pages without sign-in, and its
`/login` sends the browser straight on to the page it asked for.

## CLI

Every `sparkwing` command that talks to a remote controller reads
connection info from a profile. Register one first:

```sh
# Register a prod profile (controller URL + admin bearer).
# --token-stdin prompts without echo on a terminal and reads a pipe otherwise.
sparkwing cloud connect --name prod \
    --controller https://sparkwing.example.com \
    --token-stdin
```

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
sparkwing cluster tokens list --prefix swu_6cF9r2Kp --profile prod

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

Two windows remain:

- **Other controller replicas.** Invalidation is in-process. A replica
  that did not serve the revoke keeps its cached entry for up to 60
  seconds. Restart or scale the controller to zero to close it now.
- **The loopback controller each run starts.** A local run serves the
  admin API from the orchestrator process over the same tokens table,
  behind its own 60-second cache that a controller restart does not
  reach. It is bound to loopback and exits with the run.

Sessions carry no cache: the controller reads the `sessions` row on
every request and the dashboard resolves the session on every protected
request, so deleting a session or a user logs that browser out on its
next request.

A session expires seven days after its last use. Each successful use renews
that term, but never past 30 days from creation. Reaching that age deletes the
row and answers `401`, so the browser signs in again. An embedder changes the cap with
`controller.Server.WithSessionMaxLifetime`.

## When a runner's token dies

A controller answers a revoked or expired token with a `401` whose body
carries `"token_state": "revoked"` or `"token_state": "expired"` and a
`Retry-After: 3600` header. Other `401`s, such as an unknown token, carry no
`token_state`. A token's state is only reported to a caller that sent a
bearer whose hash matched that row, so the field tells nobody anything about
a token they do not already hold.

Every loop that polls the controller for work reads that field: the runner
agent, the other `sparkwing-runner` claim loops (node pool, trigger loop,
worker and Job launcher), and `sparkwing cluster worker`. On a dead token
the loop logs one error naming the token's prefix and how to recover, then
parks. It does not
exit, because a supervisor would restart it into the same refusal. While
parked it asks the controller at most once an hour, so a fleet of revoked
machines costs the controller one request per machine per hour rather than
one per poll. A network error during that hourly check keeps it parked. If
the controller answers again, the loop resumes and logs that it did.

A heartbeat for work already claimed stops instead: a dead token can neither
renew the claim nor report the result, so the runner cancels the node or run
rather than beating until the lease lapses.

A dispatcher whose token dies mid-run treats it as a lost claim. The local,
Kubernetes and warm-pool runners stop their node heartbeats and status polls,
write no row for the node, and hand it back as cancelled. They leave running
work alone: a local child process or a Job's pod renews its node claim with its
own credential, so it finishes or stops by that claim. A warm-pool node is not
handed to the fallback. `sparkwing runs logs --follow` against a controller
stops at the first dead-token refusal, and backs off from 300ms to 5s on other
failures.

To recover, give the machine a live token:

- **Runner agent** (`sparkwing cluster runners add`): run
  `sparkwing cluster runners add --force` with the same `--profile` and
  `--name`. The parked agent watches its config file and reloads it within
  seconds of the rewrite, without a restart.
- **`sparkwing cluster worker`**: point the profile at a live token. The worker
  watches the profile file and reloads it within seconds.
- **A token in a `--credentials-dir`**: replace the `agent-token` file with a
  live token and restart the process.

Failures that are not a dead token back off rather than parking. A network
error, a `5xx` or another `401` waits about a second, doubling on each
consecutive failure up to five minutes, with up to a quarter of each wait
taken off at random so a fleet does not retry in step. A loop whose own poll
interval is longer than five minutes still waits at most five minutes after a
failure. A `429` or `503`
waits at least as long as its `Retry-After`. The first answered request
resets the backoff.

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
- **Fine-grained `admin` split**: `triggers.claim` and `runs.state`
  carved the runner's work out of `admin`. What remains can be
  split further into `cache.write`, `locks.admin`, and similar when a real
  caller needs that narrower trust.
- **Execution capabilities beyond runner job bodies**: a runner keeps its
  token in the supervisor and gives each job-body child a process-lifetime
  loopback capability for its exact run, node, and acknowledged attempt
  log/lifecycle. Other execution modes retain their documented credential
  boundary. A future
  capability service could make the same split portable across container and
  process boundaries that do not share one supervisor.
