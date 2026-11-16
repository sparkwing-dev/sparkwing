# Run events and subscriptions

Sparkwing tells a team's own services when its runs start and finish. This note
records the architecture: the event contract, the subscription and delivery
model, and where each rule lives in the code.

## Terms

- **Run event:** a CloudEvents 1.0 record of one run lifecycle change,
  `run.started` or `run.finished`, identified by the run and its state sequence.
  Run events are separate from the audit stream and from log record events.
- **Subscription:** a team-owned request to receive run events at a URL,
  narrowed by event type, final status, pipeline and repository, with a signing
  secret Sparkwing generates and stores sealed.
- **Delivery:** one attempt series to send one run event to one subscription.
  Its row keeps the exact body, so every retry and redelivery sends the same
  event.

## Goal

A team creates a subscription through the API or CLI. Sparkwing then POSTs
each matching run lifecycle event (started, finished with success, failure,
cancellation or dry run) to the subscription's URL. Delivery is signed and
at-least-once, retried with backoff, and visible when it fails. A run's state
change and its event commit together or not at all.

## Where the rules live

- `internal/store/run_state.go` `transitionRunTx` is the only writer of
  `runs.status` (the one other `UPDATE runs SET status` is in `teststore`). Every
  path ends here: owner finish, cancel drain, owner loss, queue timeout, launch
  rejection, credit refusal, publish, reaper, controller HTTP and local loopback.
  It already appends `run_started` and `run_<terminal>` state events with
  `state_seq` inside the transaction.
- `internal/store/completion.go` `run_effects` is an existing transactional
  outbox. It is written by `writeRunEffectsTx` in the same transaction and
  drained by `ReconcileRunEffects`, which runs from the controller reaper tick
  and from the local daemon's maintenance loop
  (`internal/wingd/localcontroller/controller.go`). It keeps one row per
  (run, effect) and coalesces revisions. That suits "desired state" effects
  such as GitHub checks, but it would merge started and finished into a single
  delivery when both are pending. It is the precedent for run events, not the
  table to reuse.
- `internal/store/actions_dispatch.go` fences concurrent controller replicas
  with a `sending` state plus a `send_until` lease. Run event delivery copies
  that pattern.
- `internal/controller/cipher.go` `BoundCipher` seals values bound to their
  team and row. Local mode has a cipher too (`local_services.go`).
- `internal/sourceurl` rejects internal names and unroutable addresses.
  `CheckPublicHost` applies those checks to a subscription URL at create time
  without the deployment's clone host policy; the webhook dialer resolves,
  checks and pins each address it connects to.
- `internal/runevents` builds the event body from an allowlisted `Facts` value
  and matches subscription filters. `internal/store/run_event_subscriptions.go`
  owns the subscription rows and the store's event source identity.
- Routes are declared in `internal/capability/routes.go`, and
  `api/openapi.yaml` is generated from them by `bin/gen-api-docs.sh`. Team
  routes use `OwnerTeam` together with `member(<grant>)`.
- Schema changes are a forward step in `schemaUpgrades`
  (`internal/store/schema_upgrade.go`) on top of the neutral `schema.sql`,
  translated to SQLite and PostgreSQL. Schema 39 adds the run event tables.

## 1. The first slice (v1)

**In v1:**

1. Two event types: `run.started` (pending to running) and `run.finished`
   (any terminal state, with `status` in the data).
2. Team-scoped subscriptions as an API resource. Each has a URL, filters and a
   generated signing secret. Subscriptions are managed through the API and CLI
   by team admins.
3. A durable outbox. The run transition, in the same transaction, writes one
   delivery row for each matching subscription.
4. A delivery worker that sends signed HTTP POSTs, retries with backoff for 24
   hours, marks the delivery `failed` (the dead letter) after that, and is safe
   across controller replicas.
5. Minimal recovery: list a subscription's deliveries, filtered by state, and
   redeliver one.
6. The same code on local SQLite (wingd) and controller Postgres.

**Deferred:**

- Subscriptions declared in repository YAML: branch filters, defaults
  inheritance and plan-seal capture.
- Follow-up runs started by an event.
- Operator subscriptions that span teams.
- Pausing or editing a subscription. v1 has create, delete and rotate; editing
  means delete and recreate.
- Signing with old and new secrets during rotation. Retries cover the gap: see §3.
- Per-run event views, delivery metrics and dashboard UI.
- A pull or feed API, a test-ping endpoint and a local `listen` helper.
- Events for jobs, cancel requests and queued runs.
- Argument and flag names in the event payload. The prior design captured
  invocation names at intake, which costs a lot for little value in v1.

## 2. Data model and API

### Event: CloudEvents 1.0, structured JSON

Use CloudEvents. It costs one struct, it gives receivers a standard envelope
and SDKs, and it costs nothing to keep later. The body is sent as
`Content-Type: application/cloudevents+json`.

```json
{
  "specversion": "1.0",
  "id": "run_01JD7.../2",
  "source": "urn:sparkwing:<store-id>:team:<team>",
  "type": "dev.sparkwing.run.finished.v1",
  "subject": "runs/run_01JD7...",
  "time": "2026-11-13T18:04:11.123Z",
  "datacontenttype": "application/json",
  "sparkwingstateseq": 2,
  "data": {
    "run_id": "run_01JD7...", "run_number": 412, "pipeline": "test",
    "status": "failed", "reason": "job_failed",
    "repository": {"key": "github.com/acme/app", "display": "acme/app"},
    "git": {"branch": "main", "sha": "4f1c..."},
    "trigger": "push",
    "created_at": "...", "started_at": "...", "finished_at": "...",
    "lineage": {"root_run_id": "...", "parent_run_id": null, "retry_of": null},
    "url": "https://<dashboard>/runs/run_01JD7..."
  }
}
```

- `id` is `<run_id>/<state_seq>`. It is deterministic, unique within `source`,
  and the same across retries and redeliveries. Receivers deduplicate on
  `(source, id)`.
- `source` uses an identity that survives restarts and failover: the store ID
  (the `run_event_source_id` setting, created on first use and kept by
  restore) plus the team's immutable name, never its display name.
- `sparkwingstateseq` is a CloudEvents extension attribute. It lets a receiver
  drop a `run.started` that arrives after `run.finished` for the same run.
- `data` comes only from `runs` columns on an allowlist. It never includes
  argument or flag values, environment, secrets, error text, logs, outputs,
  local paths or webhook URLs. `reason` is the stored reason code, not
  `MaskedError`. Missing facts are left out rather than guessed. `url` appears
  only when a dashboard URL is configured.
- `started_at` is null for a run that finished without starting. Queue
  timeout, credit refusal and launch rejection produce `run.finished` with no
  `run.started` before it.
- The type carries a `.v1` suffix. A breaking change to `data` gets a new type.

### Store (schema step 39)

```sql
CREATE TABLE run_event_subscriptions (
  team TEXT NOT NULL REFERENCES teams(name),
  id TEXT NOT NULL,                       -- sub_<random>
  url TEXT NOT NULL,
  event_types TEXT NOT NULL,              -- JSON array; non-empty
  statuses TEXT NOT NULL DEFAULT '[]',    -- JSON array; empty = all (finished only)
  pipelines TEXT NOT NULL DEFAULT '[]',   -- exact names; empty = all
  repositories TEXT NOT NULL DEFAULT '[]',-- repo keys; empty = all
  secret TEXT NOT NULL,                   -- BoundCipher envelope
  description TEXT NOT NULL DEFAULT '',
  created_principal TEXT NOT NULL,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  PRIMARY KEY(team,id)
);
CREATE TABLE run_event_deliveries (
  team TEXT NOT NULL REFERENCES teams(name),
  subscription_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  body BLOB NOT NULL,                     -- exact CloudEvent bytes sent on every attempt
  state TEXT NOT NULL CHECK(state IN ('pending','sending','succeeded','failed')),
  attempts BIGINT NOT NULL DEFAULT 0 CHECK(attempts >= 0),
  next_at BIGINT NOT NULL,
  send_until BIGINT,                      -- lease while state='sending'
  deadline_at BIGINT NOT NULL,            -- created_at + 24h, reset by redeliver
  last_status BIGINT,                     -- HTTP status, NULL on transport error
  last_error TEXT NOT NULL DEFAULT '',    -- safe code: timeout|dns|refused|tls|policy|http_status
  created_at BIGINT NOT NULL, finished_at BIGINT,
  PRIMARY KEY(team,subscription_id,event_id),
  FOREIGN KEY(team,subscription_id) REFERENCES run_event_subscriptions(team,id) ON DELETE CASCADE,
  FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE
);
CREATE INDEX idx_run_event_deliveries_due ON run_event_deliveries(next_at) WHERE state IN ('pending','sending');
CREATE INDEX idx_run_event_deliveries_run ON run_event_deliveries(team,run_id);
```

Each delivery row stores its own copy of the body. There is no separate events
table, because a subscription count S per event is small and one fewer table
means one fewer join. An event with no matching subscription writes nothing,
so teams without subscriptions pay one indexed read per run transition.

### Writing the outbox

Add `writeRunEventDeliveriesTx(ctx, tx, team, row, kind, now)` to
`transitionRunTx`, next to `writeRunEffectsTx`. It runs only when `kind` is
`run_started` or a terminal kind, never for `run_cancel_requested`. It reads
the team's subscriptions, matches each one in Go against type, status,
pipeline and repository, builds the body once, and inserts one row per match.
The replay path, which returns early when the digest matches, writes nothing,
so idempotent finishes do not duplicate events. If the write fails, the whole
transition rolls back. Matching uses the subscriptions that exist at commit
time, and retries never rematch.

### Routes (team-owned, `OwnerTeam`)

| Route | Grant | Retry |
| --- | --- | --- |
| `POST /api/v1/subscriptions` | `TeamAdmin` | `RetryIdempotencyKey` |
| `GET /api/v1/subscriptions` | `TeamAdmin` | `RetrySafe` |
| `GET /api/v1/subscriptions/{subscription}` | `TeamAdmin` | `RetrySafe` |
| `DELETE /api/v1/subscriptions/{subscription}` | `TeamAdmin` | `RetrySafe` |
| `POST /api/v1/subscriptions/{subscription}/rotate-secret` | `TeamAdmin` | `RetryNever` |
| `GET /api/v1/subscriptions/{subscription}/deliveries?state=&cursor=` | `TeamAdmin` | `RetrySafe` |
| `POST /api/v1/subscriptions/{subscription}/deliveries/{event}/redeliver` | `TeamAdmin` | `RetrySafe` |

- Create validates the URL and filters. Omitted event types mean
  `run.finished` when statuses are given and both events otherwise. Create
  generates a 32-byte secret and returns it once as `whsec_<base64>`. It is
  never readable again; rotating it returns a new one.
- Every route is `TeamAdmin`-only, so v1 needs no URL redaction. A URL can
  carry a credential in its path or query, so `RunsRead` members do not see
  subscriptions. A later per-run delivery view for `RunsRead` would show only
  the host.
- Creating, deleting and rotating a subscription each append an audit row in
  the same transaction, following the existing privileged-mutation pattern.
- CLI: `sparkwing subscriptions create|list|show|delete|rotate-secret|deliveries|redeliver`,
  calling the same routes. This works the same locally and against a
  controller.

### Team isolation

- Every row is keyed by `team`, and store methods hang off `*store.Tenant`.
  A subscription ID from another team returns 404, the same as a missing one.
- The secret is sealed with `BoundCipher` under a new purpose,
  `secrets.PurposeEventSigning`, bound to the team and subscription ID. A row
  copied into another team fails to open.
- The worker processes deliveries per tenant through `Operator.EachTeam`, as
  `ReconcileRunEffects` does.
- Deleting a run or a subscription cascades to its deliveries. Deleting a team
  follows the existing team-deletion ownership. Add both tables to the reset
  and preserve list in `STORE-DATA.md`.
- The operator secret-rotation path (`/api/v1/operator/secrets/rotate`) must
  re-seal `run_event_subscriptions.secret`. List the table wherever that path
  enumerates sealed rows.

## 3. Delivery semantics

- **At-least-once.** Any 2xx response means delivered. A lost acknowledgement
  causes a redelivery of the same body, which carries the same `id` and the
  same `webhook-id`.
- **Ordering:** none guaranteed, across runs, subscriptions or retries.
  `time` and `sparkwingstateseq` let a receiver drop a stale event. A
  per-subscription ordering queue is deferred, because one slow receiver would
  stall its whole subscription.
- **Claim.** `ClaimDueRunEventDeliveries(limit)` is a conditional UPDATE. It
  moves rows to `state='sending'`, `attempts=attempts+1` and
  `send_until=now+2m` when they are either `pending` with `next_at<=now`, or
  `sending` with an expired lease. A crash mid-send therefore produces a resend,
  which at-least-once delivery allows. Completion is fenced on
  `(state='sending', attempts=<claimed>)`, so a stale replica's result is
  dropped. The outbox `next_at` index serves this query on both dialects.
  PostgreSQL may add `FOR UPDATE SKIP LOCKED` as an optimization.
- **Request.** `POST` with the stored body. The headers are fixed and owned by
  Sparkwing; the subscription cannot add any. Attempts time out after 10
  seconds, redirects are not followed, ambient proxies and credentials are not
  used, and at most 64 KiB of the response is read and then discarded.
  `User-Agent: Sparkwing-Webhooks/<version>`.
- **Retries.** Retry every non-2xx response and every transport error,
  including 3xx and 4xx. It is one rule, and it rides out receiver deploys and
  secret rotation. The schedule is exponential backoff with full jitter: about
  10s, 30s, 2m, 10m, 30m, then hourly, capped at one hour. A valid
  `Retry-After` is honoured up to the cap. Retries stop at `deadline_at`
  (24 hours), which works out to about 28 attempts.
- **Dead letter.** After the deadline the row moves to `state='failed'` with
  the last safe error code and HTTP status. Failed rows stay visible in
  `deliveries?state=failed` until their run or subscription is deleted.
  `redeliver` sets `pending`, `next_at=now` and `deadline_at=now+24h`. It
  keeps the body and event ID, and it also works on a `succeeded` row, for a
  receiver that lost data.
- **Signing: [Standard Webhooks](https://www.standardwebhooks.com/).** The
  headers are `webhook-id: <event id>`, `webhook-timestamp: <unix seconds>`,
  and `webhook-signature: v1,<base64(HMAC-SHA256(secret, id + "." + ts + "." + body))>`.
  The timestamp and signature are fresh on each attempt. This is a published
  scheme with verification libraries in most languages, so receivers do not
  need Sparkwing-specific code. Receivers should compare in constant time and
  reject timestamps more than 5 minutes off. No `ce-` headers, because
  structured mode carries the attributes in the body.
- **Secrets.** The secret is generated server-side and returned once. Only
  the sealed envelope is stored. It is opened per attempt and never logged or
  shown again. Rotation replaces the secret immediately. Attempts in the
  window before the receiver updates fail verification, get a non-2xx, and are
  retried, so the 24-hour retry window makes v1 dual-signing unnecessary.
- **Destination policy.** Validate at create time and enforce again on every
  dial. Validation means a strict URL parse with no userinfo and no fragment.
  - Local (wingd): `http` or `https` to any address, loopback included.
  - Controller (self-hosted and Cloud): `https` only. The dialer resolves the
    host, rejects addresses that `sourceurl` treats as unroutable (loopback,
    private, link-local, metadata, IPv4-mapped and similar), and pins the
    checked address while verifying the certificate against the hostname.
    One operator setting, a CIDR and host allowlist for private destinations,
    lets self-hosted controllers reach internal receivers. Its default is
    empty.

## 4. Local SQLite and Cloud Postgres

There is one code path. Everything is neutral SQL in `schema.sql`, plus a
forward step that `schemaUpgrades` applies in the open transaction (under the
migration advisory lock on PostgreSQL). The outbox write rides the existing
`transitionRunTx` transaction on both dialects, so atomicity comes free.

- **Controller.** A dedicated `runEventDelivery` goroutine in
  `internal/controller/server.go` ticks every 1 second and runs up to 8 sends
  concurrently. It is not part of the reaper tick, because a 10-second webhook
  must never delay reaping. Replicas coordinate only through the claim lease.
- **Local.** `localcontroller` starts the same worker as a fourth goroutine
  beside queue, activity and maintenance. It is not part of `maintain`,
  because a slow receiver must not stall the one-second `ownRuns` and `sweep`
  loop. Deliveries are sent while the local daemon runs. A run that finishes
  while no daemon is up leaves its rows pending until the daemon starts. It
  loses nothing, because the rows are durable. Local mode allows plain `http://localhost` receivers, which is
  the development loop.
- The host-snapshot and object-store layers (filesystem and S3) are not
  involved. They hold bytes, not lifecycle state, so they cannot emit events.
