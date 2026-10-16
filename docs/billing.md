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
| High | 512 | 6 hours | At least $500 paid, or operator grant of standing. More capacity is available by request. | $500 |
| Held | 0 new admissions | Running work finishes within its awarded deadline | Decline, dispute or chargeback | An active hold takes precedence over prepaid funds |
| Collection ladder | $5 → $20 → $50 → $100 → $250 → $500 → $1,000, then each $1,000 | | | |
| First collection | $5 owed or day 7 of outstanding debt, whichever comes first | | | |
| Monthly collection | Outstanding balance on the 1st | | | |
| Provider minimum | $0.50 | | | |
| Launch admission | Automatic admission stops at 100 admitted accounts. Further signups join the waitlist until an operator admits them. | | | |

Standing is the higher of the history tier and the tier backed by the current
prepaid balance. Adding funds raises standing in the settlement transaction.
Using prepaid funds can lower its balance-backed tier. Successful collections
and payment history remain available after the balance is spent. Holds prevent
new Cloud admissions even when the balance is positive.
An open dispute holds the team. A lost dispute retains Held standing until an
operator releases its hold. A won or closed warning dispute releases its hold.
Each declined collection attempt creates a separate hold. Releasing an earlier
hold does not exempt a later declined retry. Replaying the earlier decline
leaves its released hold unchanged. Successful collection clears decline holds.

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

New accounts receive no automatic free credits. Operators retain the per-account
credit grant action. Add funds remains available to teams with saved cards.
