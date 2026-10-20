# Cloud billing

Cloud usage draws on a prepaid balance. A team buys credits with Add funds, and
can turn on automatic refill so a saved card tops the balance up before it runs
out. Cloud work never runs on credit: when the balance cannot pay for the next
slice of a running job, the job stops.

## Standing

Standing caps how much Cloud capacity a team can use at once and how long one
job may run. Payment history alone caps its automatic refills; see
[Automatic refill](#automatic-refill).

| Policy | Concurrent Cloud vCPU | Maximum duration per job | History requirement | Prepaid balance backing this standing |
| --- | ---: | --- | --- | ---: |
| New | 8 | 4 hours | Saved card, no payment yet | $5 |
| Established | 32 | 4 hours | First payment | $20 |
| Trusted | 128 | 6 hours | At least $50 paid and 30 clean days | $100 |
| High | 512 | 6 hours | At least $500 paid and 30 clean days, or operator grant of standing. More capacity is available by request. | None; prepaid funds never back High |
| Held | 0 new admissions | Running Cloud work receives a durable cancellation | Dispute, early fraud warning, operator hold or revoked standing | An active hold takes precedence over prepaid funds |
| Launch admission | Automatic admission stops at 100 admitted accounts. Further signups join the waitlist until an operator admits them. | | | |

Standing is the higher of the history tier and the tier backed by the current
prepaid balance. Prepaid funds raise standing at most one tier above the
history tier and never above Trusted. A first payment earns Established
history, so prepaying alone reaches Trusted at most. High needs $500 paid and 30
clean days, or an operator grant of standing, whatever the balance. Add funds
and automatic refills both count as payments.

A hold restarts the clean days when it is placed and again when it is released.
An operator hold that the operator who placed it releases as "no fault found"
counts as if it never happened. Dispute, fraud-warning and other automatic holds
always restart the count.

Adding funds raises standing in the settlement transaction. Spending the
balance can lower its balance-backed tier; payment history stays.

Admissions include resources already awarded and reserved for jobs that have
not started. A job larger than its standing's capacity is refused immediately
with the standing named. Each Cloud award carries a hard deadline. Renewal
cannot extend it. Reclaiming a job keeps its first Cloud admission clock. A
later standing change does not shorten a running job's awarded deadline. The
launcher passes over teams without standing capacity so other teams can
receive work. Local and self-hosted placement does not consume Cloud standing
capacity or receive a standing deadline.

A team owner can contact us for High standing, higher limits or larger payments
from the billing page or with
`sparkwing billing request-tier --profile cloud --tier high --message <text>`, with a message of
up to 1,000 characters (Unicode code points). A team has at most one open
request. The request reaches the operator console's standing
queue with the team's standing, payment history and message. The operator
approves it by granting standing or declines it with a reason. Each decision
names the request it reviewed and is refused once that request is no longer
open, so a stale or retried decision never resolves a later request. Both decisions
are audited, and the billing page and `sparkwing billing standing` show the
outcome. High-standing teams can also send requests. Describe the payment size
or limits needed in the message so the operator can contact you. Granting High
standing does not raise the maximum per payment.

New accounts receive no automatic free credits. Operators retain the per-account
credit grant action.

## Add funds

Team owners buy credits from the console billing page. Add funds opens a Stripe
Checkout page, and the credits land when the controller receives Stripe's
signed webhook for the paid checkout. The billing page also saves a card through
a Stripe setup page; a saved card is what automatic refill charges.

Each Add funds payment and automatic refill is at most $1,000, at every standing
tier including High. Larger amounts are refused before a payment starts. Use
the billing page contact request, or `sparkwing billing request-tier --profile cloud --tier high --message <text>`, to ask about larger payments. The request reaches the
operator console queue.

Every Add funds checkout asks Stripe to request 3-D Secure. Stripe can still
complete a payment without authentication, for example when the card or issuer
does not support it or authentication fails to run. The controller records the
result Stripe reports on the settled charge, or none, with the paid checkout
and its audit event; it does not refuse a payment by its result.

Checkout and saved-card setup requests require an idempotency key. Retry an
interrupted request with the same key. The controller retains its reservation
after a provider failure and returns the saved page after completion. Changing
the request under that key conflicts. An expired page requires a new key.

## Automatic refill

Automatic refill is off until an owner turns it on, and turning it on needs a
saved card. The owner sets a threshold and a refill amount: when the balance
falls below the threshold, the controller charges the saved card the refill
amount. A refill is at least $10, so card fees stay a small share of it, and at
most $1,000. The owner can also set a weekly and a monthly limit on refills;
zero means no limit.

Set refills on the console billing page, with `sparkwing billing refill`
(`--enable` or `--disable`, `--threshold <dollars>`, `--amount <dollars>`,
`--week-limit <dollars>`, `--month-limit <dollars>`), or with
`PUT /api/v1/team/billing/refill`. Settings take effect when their update
commits.

Refills are capped by the team's paid history alone, so a stolen card cannot buy
much compute before its owner disputes the charge. The prepaid balance never
raises these caps. Refills use the history tier's payment and clean-day requirements
listed above. An operator grant of standing
gives the High caps. Teams that outgrow these caps use the contact request for
higher limits.

| Refill history | Largest refill | Refills per day | Refill total per week |
| --- | ---: | ---: | ---: |
| New | $50 | 1 | $100 |
| Established | $200 | 2 | $500 |
| Trusted | $1,000 | 3 | $3,000 |

High still has the $1,000 maximum per payment and uses the team's own weekly
and monthly limits.

The controller checks for due refills every 30 seconds and keeps at most one
refill in flight per team. A refill fixes its amount, card, customer and receipt
address when it opens, and every retry sends that same request under the
refill's own id, so a retried or interrupted refill never charges twice. The
payment is created before it is confirmed. A refill whose payment was never
created is closed after 23 hours, or when the team saves a different card first,
and the next one starts fresh. Once its payment exists, the refill keeps the
team's one refill slot until its payment outcome is known. Before confirming
an unconfirmed payment, the controller rechecks that refills remain enabled,
the team has no hold, the saved card is still eligible, and the payment fits
the current limits. Otherwise it cancels the unconfirmed payment at Stripe and
retains its reservation record. Processing and succeeded payments are
reconciled without another confirmation. Credits settle only from Stripe's
signed webhook, through the same path as Add funds: the same card pinning,
refund ordering and standing update apply.

Every unresolved reservation and confirmed refill counts against the limits,
whether granted, declined, refused or refunded. A payment Stripe confirms was
canceled releases its reservation without consuming the limits. A refill
that would exceed a team limit or a cap is not made. The credits run down and Cloud work stops at zero; the
billing page names the limit that stopped the refill (`refill.limit` in
`GET /api/v1/team/billing`).

A refill the card declines, or that asks for authentication, is recorded with
its decline code, and the team's owners receive an email asking them to update
the card. A refill paid with a card pinned to another team is refused, refunded
in full, and the card is taken off the team, which turns refills off. Either
way no further refill starts until an owner changes the saved card or an Add funds payment
succeeds; changing the refill settings does not restart them. The balance runs
down as usual. A delayed failure from a refill opened before a successful Add
funds payment does not block refills again. A failure from a newer refill does.

## Metering

Cloud work is metered in 60-second slices. Admission reserves the first slice
from the balance; a job whose first slice the balance cannot cover is refused
with "out of credits", naming the balance and what the slice needs. Each lease
renewal charges the time used and reserves the next slice in the same
transaction, and a metered lease never runs past the time already paid for, so
a job whose runner loses the controller stops, and is settled, within what it
paid. When the balance cannot pay the next slice, the renewal records
the stop and cancels the metered job in the same transaction. Placing a hold
does the same for active metered work. The reason names the shortfall or hold
and what resolves it. The owner and keeper stop execution when they receive that
cancellation. The lease ends at the earlier of its paid horizon and a drain deadline at most one slice away. The host kills the SDK
process tree at that bound, including commands that ignore cancellation.
SDK cancellation and cleanup share the time remaining under the bound.
Kubernetes Jobs use the committed lease deadline and zero termination grace.
Renewals cannot move a funding stop's deadline. There is no grace period,
and lifting the hold or adding funds does not resume the stopped job.
Final settlement refunds the unused part of the last slice.

When a funding stop or hold stops a metered job, only that job stops. The run's
owner keeps orchestrating, and independent unmetered jobs continue. Normal
dependency settlement decides which descendants can run. When it stops the
run's owner itself (a trigger or plan running on Cloud), the whole run stops,
including jobs on customer-owned runners, because its orchestrator has stopped.

Retained storage is charged only from the balance there is: once the balance
reaches zero, storage accounting advances without a debit. The unfunded part
is forgiven and is not collected after a top-up.

## Holds

A hold stops new Cloud admissions and records cancellation and a drain deadline
for active metered work in the same transaction. Running Cloud work stops when
the owner or keeper receives the cancellation, or when its funded lease expires
if the controller connection is lost. Releasing the hold before the next
heartbeat still leaves that cancellation in place. Start a new run after the
hold is resolved. Unmetered jobs continue when their run's owner remains active.
A hold has one of four causes:

- an operator hold, placed from the operator console;
- an open payment dispute;
- an early fraud warning on one of the team's payments;
- revoked standing.

Each refusal and notice names the hold and points at support. A lost dispute
keeps its hold until an operator releases it. A won dispute, or a closed
warning dispute, releases its hold.

## Refunds, disputes and stolen cards

Any card payment can be refunded or disputed after its credits are spent. The
clawback takes the credits back even when that leaves the balance negative, and
a negative balance blocks new Cloud work until the team adds funds.

The controller's `--payment-mode` configures both provider verification and the
ledger mode for new reservations. Test and live provider records retain separate
mode tags. Empty mode disables settlement.

Each controller store holds a random payment deployment id, created once at
first start and never changed by configuration, so renaming the controller's
host leaves its in-flight payments its own. The checkout service stamps that id as
`metadata[sparkwing_deployment]` on every Checkout Session, setup intent,
payment intent and customer it creates for the controller. Deployments that
share one Stripe account each receive the other's events at `/webhooks/stripe`.
After checking the signature, and before any Stripe read or store write, the
controller classifies each event once:

- An object stamped with another deployment is foreign: acknowledged with 204,
  never read from Stripe or settled. If this controller recorded that object,
  the mismatch is an integrity fault instead: it is logged at error level with
  `alert=payment_ownership_conflict`, answered with a 500 so Stripe retries, and
  never settled.
- An object stamped with this deployment, or one that resolves to a checkout,
  page, intent or refill this controller recorded, is local and settles. An
  unstamped refund, dispute or fraud warning whose payment is not recorded yet
  is local only when its payment intent carries this deployment's stamp. A
  refund or warning is then kept against its payment, and a dispute whose payer
  is not known yet fails so Stripe redelivers it.
- An unstamped event that resolves to nothing is acknowledged. Card setup pages
  and Add funds payment intents settle through another object, so their other
  events are acknowledged too.

Card setup and Add funds pages are correlated by their Checkout Session, which
exists before payment. A completed card setup binds its verified setup intent to
the page's team. A refill's payment intent carries `purpose=card_refill` and
settles on its own events. Unsigned and badly signed requests are refused.

The controller is the only consumer of Stripe events, so it also answers stolen
cards. A card is pinned, by its Stripe fingerprint, to the team whose payment it
first funds. A payment by that card for another team, through Add funds or a
refill, grants nothing and is queued for a full refund, and the card cannot be
saved on another team.

An actionable early fraud warning on a payment this controller settled always
holds the team and blocks the card: it is removed wherever it is saved and
cannot be saved again. Sparkwing treats a settled 3-D Secure result of
`authenticated` as protected. The warning leaves that payment's credits frozen
under its hold and queues no refund. `attempt_acknowledged`, missing results and
all other results are unprotected. For those payments, the warning takes back
the credit even when that leaves the balance negative and queues a full refund
unless Stripe already returned it. The controller stores the authentication
result in `three_d_secure` and its protection decision separately in
`liability_shift` when the payment settles. This decision follows Sparkwing's
warning policy. Stripe's authentication result does not prove liability
protection in every case. Neither stored value changes on replay or gets read
from Stripe again when a warning arrives. A later payment by a blocked card
holds its team and is refunded. A refused payment raises the
`card_payment_refused` alert, and each refund made raises `card_refund`.

A refund and a fraud warning are kept against their payment whatever order Stripe
delivers them in. A refund that arrives before its payment is taken back in the
transaction that grants the payment, and a refund already made in full is not
queued again. A replay of a signed event is recognized by what Stripe signed,
not by the card details read from Stripe afterwards.
