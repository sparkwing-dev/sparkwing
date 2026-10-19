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

`--billing-url` and `--payment-mode` need `--external-url`, whose host names the
deployment. The checkout service stamps that name as
`metadata[sparkwing_deployment]` on every Checkout Session, setup intent,
payment intent and customer it creates for the controller. Deployments that
share one Stripe account each receive the other's events at `/webhooks/stripe`.
A correctly signed event is acknowledged with 204 and settles nothing when it
names another deployment, when it is a type or object the controller settles
elsewhere (a card setup or pay-now page, or an Add funds payment intent), or
when it names no deployment and refers to nothing this controller recorded. An
event naming this deployment for an object it does not know still fails, so
Stripe retries it. Unsigned and badly signed requests are refused.

