# Cloud billing

Cloud usage draws prepaid funds first. With a saved card, usage beyond that
balance is billed through a collection ladder. Each successful collection moves
the team up one rung. A decline moves it down one rung and places it in Held.
The final rung repeats. Outstanding debt is also collected each month, subject
to the payment provider's minimum.

Standing and collection follow this policy table.

| Policy | Concurrent Cloud vCPU | Maximum duration per job | History requirement | Prepaid balance backing this standing |
| --- | ---: | --- | --- | ---: |
| New | 8 | 4 hours | Saved card, no successful charge | $5 |
| Established | 32 | 4 hours | First successful charge | $20 |
| Trusted | 128 | 6 hours | At least $50 paid and 30 clean days | $100 |
| High | 512 | 6 hours | At least $500 paid and 30 clean days, or operator grant of standing. More capacity is available by request. | None; prepaid funds never back High |
| Held | 0 new admissions | Running work finishes within its awarded deadline | Decline, dispute or chargeback | An active hold takes precedence over prepaid funds |
| Collection ladder | $5 → $20 → $50 → $100 → $250 → $500 → $1,000, then each $1,000 | | | |
| First collection | $5 owed or day 7 of outstanding debt, whichever comes first | | | |
| Monthly collection | Outstanding balance on the 1st | | | |
| Provider minimum | $0.50 | | | |
| Launch admission | Automatic admission stops at 100 admitted accounts. Further signups join the waitlist until an operator admits them. | | | |

Standing is the higher of the history tier and the tier backed by the current
prepaid balance. Prepaid funds raise standing at most one tier above the
history tier and never above Trusted. A first payment earns Established
history, so prepaying alone reaches Trusted at most. High needs $500 paid and 30
clean days, or an operator grant of standing, whatever the balance. A hold
restarts the clean days when it is placed and again when it is released. Two
holds count as if they never happened: an operator hold the operator who
placed it releases as "no fault found", and a declined-charge hold that a later
successful charge clears. Dispute, fraud-warning and other automatic holds always restart the
count.
Adding funds raises standing in the settlement transaction.
Using prepaid funds can lower its balance-backed tier. Successful collections
and payment history remain available after the balance is spent. Holds prevent
new Cloud admissions even when the balance is positive.
An open dispute holds the team. A lost dispute retains Held standing until an
operator releases its hold. A won or closed warning dispute releases its hold.
Each declined collection attempt creates a separate hold. Releasing an earlier
hold does not exempt a later declined retry. Replaying the earlier decline
leaves its released hold unchanged. Successful collection clears decline holds.

A failed charge on the saved card, whether a decline, an expired card or a
failed collection reported by Stripe, holds new Cloud admissions at once.
Running jobs finish and work on the team's own runners continues. The billing
page and the run's notice say the card was declined and to update it. The next
successful charge releases the hold, including a retry of the declined payment
in the same checkout. Each refusal and notice names the hold that blocks the
team: a declined charge points the owner at the card, while a payment dispute,
an early fraud warning, an operator hold or revoked standing points at support.

While a collection is pending, new Cloud work waits and running work finishes.
Admissions include resources already awarded and reserved for jobs that have
not started. A job larger than its standing's capacity is refused immediately
with the standing named. Each Cloud award carries a hard deadline. Renewal
cannot extend it. Reclaiming a job keeps its first Cloud admission clock. A later
standing change does not shorten a running job's awarded deadline. The launcher
passes over teams without standing capacity so other teams can receive work.

Owners may set a rolling spend budget. No team or shared-card dollar ceiling or
daily spend cap applies. Local and self-hosted placement does not consume Cloud
standing capacity or receive a standing deadline.

Every Add funds checkout asks Stripe to request 3-D Secure. Stripe can still
complete a payment without authentication, for example when the card or issuer
does not support it or authentication fails to run. The controller records the
result Stripe reports on the settled charge, or none, with the paid checkout
and its audit event; it does not refuse a payment by its result.

A team owner can ask for High standing from the billing page or with
`sparkwing billing request-tier --tier high --message <text>`, with a message of
up to 1,000 characters (Unicode code points). A team has at most one open
request. The request reaches the operator console's standing
queue with the team's standing, payment history and message. The operator
approves it by granting standing or declines it with a reason. Each decision
names the request it reviewed and is refused once that request is no longer
open, so a stale or retried decision never resolves a later request. Both decisions
are audited, and the billing page and `sparkwing billing standing` show the
outcome. A chargeback still holds the team, and no new Cloud work is admitted
while it is held.

New accounts receive no automatic free credits. Operators retain the per-account
credit grant action. Add funds remains available to teams with saved cards.


Checkout, saved-card setup and pay-now requests require an idempotency key.
Retry an interrupted request with the same key. The controller retains its
reservation after a provider failure and returns the saved page after completion.
Changing the request under that key conflicts. An expired page requires a new key.
Owner budgets take effect when their update commits.

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
  page, intent or attempt this controller recorded, is local and settles. A
  local event whose parent is not recorded yet fails, so Stripe redelivers it;
  a refund, dispute or fraud warning that arrives before its paid event waits
  this way, and an unstamped one is local only when its payment intent carries
  this deployment's stamp.
- An unstamped event that resolves to nothing is acknowledged. Card setup and
  pay-now pages and Add funds payment intents settle through another object, so
  their other events are acknowledged too.

Card setup and pay-now pages are correlated by their Checkout Session, which
exists before payment. The completed session's verified setup or payment intent
then binds to the page's team and collection attempt, and that intent's own
events settle against it. Unsigned and badly signed requests are refused.

The controller is the only consumer of Stripe events, so it also answers stolen
cards. A card is pinned, by its Stripe fingerprint, to the team whose payment it
first funds. A payment by that card for another team, through Add funds or a
collection, grants nothing and is queued for a full refund; a refused
collection counts as a declined charge, and the card cannot be saved on another
team. An actionable early fraud warning on a payment this controller settled
holds the team, takes back the credit the payment granted even when that leaves
the balance negative, and queues a full refund unless Stripe has already
refunded the charge. The warning also blocks the card on every team: it is
removed wherever it is saved, cannot be saved again, and its later payments are
refused and refunded. The controller's refund queue makes every such refund, and
settling a refund never takes the same credit back twice.
