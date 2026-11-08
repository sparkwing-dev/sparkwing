# Public run events

Status: proposed. This is a design handoff, not an accepted wire contract.
The three product questions below need Korey's decision before publication.

Commit one immutable `run.finished` event and its selected delivery work with
each persisted run's terminal state. An existing controller worker delivers
webhooks or admits follow-up runs. This needs no separate event service.

## Language and existing owners

| Term | Meaning |
| --- | --- |
| Trigger | An input that requests a run. |
| Public event | An immutable fact that a run finished. |
| Subscription | A rule selecting public events and one destination. |
| Delivery | The tracked work of handing one event to one subscription. |
| Follow-up run | A separate run caused by a public event, with its own result. |

Acceptance can move these definitions into the root glossary and record a short
ADR under `docs/adr/`. This proposal stays in `design/rewrite/`.

The implementation seams already exist:

- [run_state.go](../../internal/store/run_state.go)'s `transitionRunTx` owns
  terminal state, fences and cleanup. Extend its transaction.
- [business_events.go](../../internal/store/business_events.go) reads audit
  facts. [runstate/events.go](../../internal/runstate/events.go) defines run
  stream names. [api/event.go](../../internal/api/event.go)'s `PublicEvents`
  sanitizes that stream. All remain separate from integration events.
- [journal.go](../../internal/wingd/journal/journal.go) can drop queued records
  and rotates files. It remains diagnostic, never the delivery queue.
- [github_delivery_inbox.go](../../internal/store/github_delivery_inbox.go)'s
  incoming effect receipts are a precedent. Its manual retry policy (decision
  41) stays separate from outgoing retries.

The older tickets name S3/mirrored state. The inspected
[architecture](../../docs/architecture.md) has SQLite/PostgreSQL state,
filesystem/S3 bytes, and host snapshots. Prove those paths without restoring
removed backends.

## Event contract

Use [CloudEvents 1.0 structured JSON](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/formats/json-format.md),
sent as `application/cloudevents+json`. The first type is
`dev.sparkwing.run.finished.v1`, configured as `run.finished`. Incompatible data
changes get another type version; `specversion` remains the CloudEvents version.

| Field | Proposed value |
| --- | --- |
| `specversion` | `1.0` |
| `id` | Opaque event ID, generated once and persisted. |
| `source` | URI containing persistent controller/store identity and immutable tenant identity. It survives restart and failover. |
| `type` | `dev.sparkwing.run.finished.v1` |
| `subject` | `runs/<run_id>` |
| `time` | Store-owned terminal timestamp in RFC 3339 UTC. |
| `datacontenttype` | `application/json` |
| `data` | The allowlisted object below. |

Publish a versioned JSON Schema following the
[CloudEvents specification](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md).

`data` contains `run_id`, `pipeline`, `status`, `trigger`, `repository`, `git`,
`created_at`, nullable `started_at`, `finished_at`, `invocation`, and `lineage`:

- `status`: `success`, `failed`, `cancelled`, or `dry_run`, matching persisted
  terminal states. A cancellation request alone emits nothing.
- `trigger`: normalize stored origin and authenticated trigger facts into
  `manual`, `push`, `pull_request`, `release`, `create`, `delete`, `schedule`,
  `hook`, `actions`, `child`, `retry`, `publish`, or `follow-up`. Trigger grants no trust.
- `repository`: canonical key plus whether identity is proven. `git` holds
  available ref and exact SHA. Missing facts stay absent. Never manufacture a
  proven identity from a display name or branch string.
- `invocation`: sorted, distinct `argument_names` and `sparkwing_flag_names`
  for explicitly supplied, schema-valid names. Exclude defaults and all values.
  Capture explicitness at intake, before argument merging loses it.
- `lineage`: `root_run_id`, nullable `parent_run_id`, nullable
  `caused_by_event_id`, nullable `retry_of`, and `follow_up_depth` (root zero).

Exclude argument/flag values, environment, secrets, credentials, local paths,
commit messages, errors, outputs, raw provider payloads and webhook URLs.
Build from typed allowlisted fields. Receivers deduplicate `(source, id)`.
Every subscription receives the same stored bytes.

## Subscriptions and configuration

Proposed project YAML, alongside the existing intake `on:` declarations:

```yaml
defaults:
  events:
    webhook:
      url: https://ci.example.org/run-events
      signing_secret: run-event-hmac
pipelines:
  - name: test
    entrypoint: Test
    events:
      - name: notify
        filter:
          status: [failed, cancelled]
          guards:
            when: [where:controller]
        webhook:
          url: https://ci.example.org/failures
          signing_secret: run-event-hmac
      - name: report
        filter:
          status: [success]
          trigger: [push, schedule]
        run:
          pipeline: report
          args:
            source_run:
              event: data.run_id
```

`events` accepts a mapping or list. Several subscriptions require unique names;
a singleton receives a reserved identity. Omitted `events` inherits
`defaults.events`; a present value replaces the list, and `[]` disables
repository subscriptions. Replacement avoids ambiguous destination/credential
merges. Absent filters and `status: any` match every outcome.
Lists OR within a field; fields AND together.
Unknown input and contradictory destinations fail validation.
`run.finished` is implicit until another event type exists.

Use the existing guard evaluator with `guards.when` / `guards.unless`: all
required predicates match; any rejected predicate prevents delivery.
These are the tickets' require/reject semantics. Profile predicates
use `where:local|controller`; profile connection names are machine aliases,
not portable controller authority. Evaluate private argument predicates inside
the controller without exporting their values or evaluated expressions.

Freeze repository subscriptions from the exact revision/uploaded snapshot
before pipeline code executes, then include them in the plan seal. Capture
configuration before planning so planning failures can notify. Without valid
configuration, record its absence and use controller subscriptions only.
A request without a persisted run has no terminal event.

Controller subscriptions add explicit tenant, repository-key and pipeline
selectors to the same union. Only operator configuration may span tenants;
team administrators configure their own tenant. Freeze the active controller
subscription revision at finalization. Controller rules are additive and
repository YAML cannot remove them. Origin and config revision live in the
delivery record. Retries never reread repository YAML or rematch filters.
Subscription identity includes origin, tenant, repository, pipeline and name;
the delivery also pins its configuration revision.

## Durable completion and delivery

For each first terminal transition, the store atomically writes the verdict,
event bytes and matched delivery rows. Enforce unique `(team, run_id, type)`
and `(team, event_id, subscription_id)` keys. Preserve the existing actor,
claim-generation and controller-epoch fences. Failed finalization rolls back
the terminal write. Repeated finish calls return the existing event/work.
Every terminal path, including reapers and unpublished-plan failures, must
reach this owner.

Persist payload, source snapshot reference, destination revision, delivery state
(`pending`, `delivering`, `retrying`, `succeeded`, `failed`, `cancelled`), attempt
count, next attempt, fenced lease, safe error and resulting run ID. Attempt
records hold identity, timing and HTTP status/safe error code. Expired leases
allow recovery; stale workers cannot overwrite receipts. Network calls happen
after commit, outside the transaction and run request context.

Webhooks POST the saved body with fixed Sparkwing headers, including event,
delivery and unique attempt IDs. A logical delivery ID survives retries and
manual replay. Each HTTP attempt gets a fresh attempt ID and signing time.
Proposed signature header: `Sparkwing-Signature: t=<unix>,v1=<hex>` where
`v1 = HMAC-SHA256(secret, ASCII(t) + "." + raw_body)`. Receivers compare in
constant time, check timestamp freshness, and durably deduplicate the event
before applying effects. Metadata headers are diagnostic; the signature
authenticates the timestamp and body. Retry/replay preserves event identity
and bytes. Secret rotation resolves the current authorized key per attempt;
receivers may accept old and new keys during their rotation window.

Any 2xx means accepted. A lost acknowledgement may cause duplicate effects,
so the contract is at-least-once attempts with visible exhaustion, never an
exactly-once or eventual-success promise. Retry network failures, timeouts,
408, 429 and 5xx. Other 4xx, redirects and destination-policy refusals require
operator action. Honor valid `Retry-After` within the remaining delivery
window. The proposed retry budget is question 1. There is no arrival-order
guarantee across runs, subscriptions or replays. Timestamp and lineage let a
consumer interpret causation without serializing unrelated deliveries.

Local queues survive CLI exit. Standalone execution needs a durable worker
handoff. Host snapshots never emit again. S3 source/log failures cannot bypass
SQL finalization. Any future nontransactional state store needs equivalent
atomic completion and recoverable delivery intents.

## Follow-up runs and trust

Admit through existing intake, deduplicated by tenant, event and subscription
ID. Commit admission and receipt together so a crash returns the same run.
Delivery succeeds at admission. The follow-up's result cannot change the source
result. Detach from awaited-child cancellation because the parent is terminal.

Default to the source repository, ref, exact code revision or retained worktree
snapshot, and dispatch/profile context. Never refetch a moving branch as a
substitute. Propagate dry-run execution policy so a preview cannot launch a live
follow-up. Typed mappings accept validated literals or allowlisted event fields,
not expressions, shell evaluation or implicit copies of source arguments.

Use controller-internal authority bound to the persisted subscription, tenant,
event and destination. A finished run's expired job capability cannot authorize
delivery. Recheck current team permission, repository access and destination
policy at admission. Reuse
[sealed provenance rules](../../internal/store/intake_provenance.go): exact-source
inheritance retains source trust; changed source is re-evaluated or demoted.
Seal the destination pipeline and follow-up causation separately. Operator
selection cannot promote asserted work. Secrets, caches, OIDC and runner
eligibility still consume those proofs. Resolve destination-pipeline secrets
without copying source environments or local run secrets.

Record lineage server-side. Reject `(canonical repository key, pipeline)`
cycles across the full ancestry. Unproven snapshots need stable local repository
identity. Permit follow-up depths one through five; refuse six visibly.
Retries and ordinary children preserve event depth and ancestry. Structural
child depth and descendant budgets remain separate. Question 3 covers approval.

## Security and recovery

Every event, subscription, claim, receipt, replay and secret lookup carries
tenant scope. Foreign IDs remain indistinguishable from missing IDs. Run-read
permission permits safe delivery status; subscription managers alone see
destinations and secret references or request replay. Controller credentials
never become available to branch-defined destinations. Repository signing
references resolve only secrets the source repository and pipeline are allowed
to use. Terminal cleanup still removes ephemeral local run secrets.

Branch URLs make outbound HTTP an SSRF boundary. Hosted delivery must apply an
operator-owned policy at each actual connection: parse HTTP(S) strictly, reject
userinfo and fragments, verify TLS, resolve and check every IPv4/IPv6 address,
pin the checked address while retaining hostname verification, disable redirects,
ambient proxies and automatic credentials, and protect metadata/internal service
ranges. DNS rebinding and mapped addresses must not bypass it. Private services
require explicit operator routing/allowlisting. Local owner-authorized delivery
can reach loopback/private receivers. The worker's network and credentials must
remain within that owner's authority. Question 2 covers these customer-visible
defaults. Fixed method/body/headers avoid an arbitrary request-builder API.

Redact entire destination URLs and secret references in ordinary views. Store
URL-bearing configuration as restricted data because paths and queries can be
credentials. Do not retain response bodies, signature headers, raw transport
errors or resolved secrets. Bound response consumption and worker concurrency.
Subscription removal stops future matches. Manager cancellation stops queued
work. Policy changes and credential revocation apply immediately.

Retain events and receipts alongside their source run. Automatic retention
keeps outstanding work and its source snapshot until its delivery deadline;
explicit run/team deletion atomically cancels pending work and follows existing
deletion ownership. A request already sent cannot be recalled. Failed webhook
replay uses the stored destination/event with current policy and credentials,
and adds a new attempt. It never reruns a follow-up already admitted. Replay
after deletion reports that the retained event is unavailable.

## Cost and implementation order

With N finished runs, S matching subscriptions/run and A attempts/delivery,
storage is O(N + NS + NSA); outbound requests are NS times average A. At 100,000
runs/day and two subscriptions, a hypothetical 2 KiB body produces about
195 MiB/day of event bodies and 391 MiB/day of first-attempt payload egress,
before SQL/index/TLS overhead. Measure actual sizes before pricing. Store the
body once, retain compact attempt metadata, index due deliveries, batch claims
fairly across teams, and expose backlog age and outcome counters with bounded
labels. An idle controller needs no new service or warm Cloud capacity.

Implement in these slices, with the owning proof in each slice:

1. Accept vocabulary and product choices. Define schema, invocation-name capture,
   normalized config, inheritance, explain output and drift checks.
2. Extend the terminal transaction with outbox rows and frozen subscriptions.
   Prove rollback, repeated finish, restart and every terminal cause on both SQL
   dialects before attaching external effects.
3. Add the webhook worker, HMAC, egress policy, retry state and delivery inspection.
   Ship status with delivery rather than leaving failures invisible.
4. Add subscription-bound follow-up admission, receipts, exact source acquisition,
   trust and lineage checks. Prove crash-after-admission deduplication.
5. Add controller-scoped subscriptions through the same finalizer and adapters.
6. Complete replay, cancellation, retention, metrics and API/CLI recovery. Finish
   cross-path proofs and source/generated docs, OpenAPI and changelog updates.

Host-owned emission needs no SDK release. Add declarations only for authoring
capabilities beyond project configuration.

## Test plan

Use deterministic clocks, controlled HTTP receivers and crash boundaries.

| Proof | Cases |
| --- | --- |
| Contract/config | Schema validation; absent/any filters; OR/AND; guard context; defaults replacement/disable; singleton/list names; unknown input refusal; source snapshot unchanged after branch edits. |
| Secret exclusion | Distinct canaries in args, flags, defaults, environment, errors, URL, secrets and provider payload; verify event bytes and ordinary status/attempt views. Explicit names survive; values do not. |
| Completion | Success, failure, dry run, cancel request versus drained cancel, launch/credit refusal, plan failure, queue timeout, owner loss, reaping, publish, retry and follow-up completion. Roll back each write boundary; replay finish; race a stale owner. |
| Delivery | Verify raw-body HMAC and freshness; changed bytes; rotation/revocation; 2xx, timeout, lost acknowledgement, 429/Retry-After, 5xx, 4xx, redirect, restart and lease fencing; duplicate and manual replay identities. |
| Isolation | Two tenants with matching IDs/names; unauthorized reads/replay; repo versus controller secrets; private-address policy, redirects, rebinding, IPv6 and metadata targets; local receiver allowed. |
| Follow-up | Branch movement, snapshot retention/loss, exact versus changed source trust, asserted source, destination secret scope, dry-run propagation, terminal parent, admission crash, cancellation independence, retry lineage, repository aliases, cycles, depths five accepted/six refused. |
| Paths/backends | SQLite and PostgreSQL through direct store, controller HTTP, local loopback, foreground/local queue, runner and reaper paths; filesystem and S3 data fixtures; host snapshot refresh must not duplicate emission. |
| Recovery/cost | Delete with pending/in-flight work, deadline exhaustion, replay after retention, fair backlog progress, bounded metrics, indexed claim query, measured finalization latency and storage amplification. |

Run focused packages with low parallelism. Prove an isolated installed candidate
through a real receiver, follow-up pipeline and controller restart. Share
fixtures across transports. This design-only change needs prose/link checks.

## Competitor evidence and open product questions

| Product | Relevant behavior |
| --- | --- |
| GitHub Actions / GitHub webhooks | [`workflow_run`](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_run) uses a default-branch workflow, limits chaining to three levels, and can grant downstream secrets despite an unprivileged predecessor. [Failed webhook delivery](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries) has a ten-second response deadline and no automatic redelivery. [Manual redelivery](https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/redelivering-webhooks) covers three days. [Ordering](https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/troubleshooting-webhooks) is not guaranteed. |
| CircleCI | [Outbound webhooks](https://circleci.com/docs/guides/integration/outbound-webhooks/) cover terminal workflow/job events, expect 2xx within ten seconds, retry failures, warn of duplicates, and support an optional HMAC secret. |
| Buildkite | [Pipeline YAML notifications](https://buildkite.com/docs/pipelines/configure/notify) accept webhook URLs and conditions. [Organization webhooks](https://buildkite.com/docs/apis/webhooks/pipelines) offer timestamped HMAC signatures; payload state may reflect a later transition. That page does not establish a retry horizon, so this proposal assumes none from it. |

The following are recommendations, not approved customer limits or security
defaults. Each question changes customer-visible behavior.

1. **How long should Sparkwing retry a failed webhook?** Options: manual replay
   only, a bounded automatic window, or automatic retry until deletion.
   Recommendation: a 24-hour automatic window with a ten-second attempt timeout,
   exponential backoff with jitter capped at one hour, then visible failure and
   manual replay while the source run is retained. This follows
   [CircleCI's automatic-retry approach](https://circleci.com/docs/guides/integration/outbound-webhooks/)
   while keeping recovery explicit like
   [GitHub](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries).
2. **Which webhook destinations and signing defaults should hosted Sparkwing allow?**
   Options: unrestricted URLs with optional signing, public HTTPS with required
   signing plus operator-approved private routes, or operator-registered endpoints
   only. Recommendation: public HTTPS with required signing for hosted delivery,
   explicit private routes for self-hosted controllers, and optional signing plus
   HTTP/loopback for owner-authorized local receivers.
   [CircleCI](https://circleci.com/docs/guides/integration/outbound-webhooks/) makes signing
   optional; [Buildkite](https://buildkite.com/docs/apis/webhooks/pipelines) offers timestamped HMAC. The hosted requirement tightens
   the ticket's optional-signing contract and needs approval.
3. **What should the first follow-up release compose?** Options: exact-source
   same-repository pipelines, explicitly authorized cross-repository sources too,
   or default-branch handlers with separate privilege grants. Recommendation:
   exact-source same-repository pipelines first, preserved source trust, cycle
   rejection, and the ticket's five follow-up hops with the sixth refused.
   [GitHub's default-branch handler and three-level chain](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_run) offer a different model;
   Sparkwing should require a later explicit design for cross-repository or
   privileged promotion rather than infer either from a completion event.
