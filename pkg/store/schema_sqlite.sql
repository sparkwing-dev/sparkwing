-- Baseline store schema (version 1) for SQLite. Keep it in step with schema_postgres.sql.

CREATE TABLE sparkwing_schema_version (
    version    INTEGER NOT NULL,
    applied_at BIGINT NOT NULL,
    PRIMARY KEY (version)
);

CREATE TABLE runs (
    id              TEXT PRIMARY KEY,
    pipeline        TEXT NOT NULL,
    status          TEXT NOT NULL,
    trigger_source  TEXT NOT NULL DEFAULT '',
    git_branch      TEXT NOT NULL DEFAULT '',
    git_sha         TEXT NOT NULL DEFAULT '',
    args_json       BLOB,
    plan_json       BLOB,
    error           TEXT NOT NULL DEFAULT '',
    -- created_at: when the controller first saw the trigger; matches
    -- triggers.created_at for trigger-originated runs. Lets pre-claim
    -- "pending" runs have a wall-clock anchor
    -- distinct from started_at (which becomes non-NULL only when the
    -- orchestrator actually starts executing).
    created_at      INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    -- declared_repo is whatever the submitter typed. Nothing is granted on
    -- it, because nothing proves the submitter owns the repository it names.
    declared_repo   TEXT NOT NULL DEFAULT '',
    repo_url        TEXT NOT NULL DEFAULT '',
    github_owner    TEXT NOT NULL DEFAULT '',
    github_repo     TEXT NOT NULL DEFAULT '',
    -- retry_of: source run; retried_as: newest retry pointer.
    retry_of        TEXT NOT NULL DEFAULT '',
    retried_as      TEXT NOT NULL DEFAULT '',
    -- retry_source: 'manual' (operator) or 'auto' (AutoRetry modifier).
    retry_source    TEXT NOT NULL DEFAULT '',
    retry_cause_node_id TEXT NOT NULL DEFAULT '',
    retry_avoid_coordinator_id TEXT NOT NULL DEFAULT '',
    retry_avoid_executor_kind TEXT NOT NULL DEFAULT '',
    retry_avoid_executor_id TEXT NOT NULL DEFAULT '',
    retry_avoid_until INTEGER,
    -- replay_of_*: single-node replay lineage.
    replay_of_run_id  TEXT NOT NULL DEFAULT '',
    replay_of_node_id TEXT NOT NULL DEFAULT '',
    -- last_heartbeat_at: orchestrator liveness ping for the run as a
    -- whole. NULL for rows that predate the column or come from a
    -- backend whose TouchRunHeartbeat is a no-op (S3 mode, which
    -- reconciles orphans via per-node heartbeats instead). The
    -- controller's reaper and the local orphan reconciler both use it
    -- to detect an orchestrator that died between node dispatches,
    -- before any node-level heartbeat exists.
    last_heartbeat_at INTEGER
, "annotations_json" BLOB, "annotation_count" INTEGER NOT NULL DEFAULT 0, "top_annotation" TEXT NOT NULL DEFAULT '', "parent_run_id" TEXT, "cost_cents" INTEGER NOT NULL DEFAULT 0, "cost_currency" TEXT NOT NULL DEFAULT 'USD', "cost_settled" INTEGER NOT NULL DEFAULT 0, "invocation_json" BLOB, "receipt_sha" TEXT NOT NULL DEFAULT '', "created_principal" TEXT NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default', "event_bytes" BIGINT NOT NULL DEFAULT 0, "event_count" BIGINT NOT NULL DEFAULT 0, "plan_accepted_generation" INTEGER NOT NULL DEFAULT 0, "admission" TEXT NOT NULL DEFAULT '', "cancel_requested_at" INTEGER, "claim_wait_ns" INTEGER NOT NULL DEFAULT 0, "dispatch" TEXT NOT NULL DEFAULT '');

CREATE TABLE nodes (
    run_id           TEXT NOT NULL,
    node_id          TEXT NOT NULL,
    status           TEXT NOT NULL,
    outcome          TEXT NOT NULL DEFAULT '',
    deps_json        BLOB,
    started_at       INTEGER,
    finished_at      INTEGER,
    error            TEXT NOT NULL DEFAULT '',
    output_json      BLOB,
    -- Warm-pool dispatch: ready_at + claimed_by + lease_expires_at.
    -- All NULL on laptop / K8sRunner paths.
    ready_at         INTEGER,
    claimed_by       TEXT,
    -- claim_principal: the authenticated principal the claim is bound
    -- to; '' when the controller served the claim unauthenticated.
    -- Display only: two tokens may carry the same principal name.
    claim_principal  TEXT NOT NULL DEFAULT '',
    -- claim_token_prefix: the claiming token's prefix segment. Unique
    -- per token, so this is what the ownership predicates match on.
    claim_token_prefix TEXT NOT NULL DEFAULT '',
    claim_executor   TEXT NOT NULL DEFAULT '',
    claim_cores      DOUBLE PRECISION NOT NULL DEFAULT 0,
    claim_memory_bytes INTEGER NOT NULL DEFAULT 0,
    claim_reservation TEXT NOT NULL DEFAULT '',
    claim_slot       INTEGER NOT NULL DEFAULT -1,
    lease_expires_at INTEGER,
    coordinator_id    TEXT NOT NULL DEFAULT '',
    executor_kind     TEXT NOT NULL DEFAULT '',
    executor_id       TEXT NOT NULL DEFAULT '',
    execution_started_at INTEGER,
    reservation_id   TEXT NOT NULL DEFAULT '',
    claim_generation INTEGER NOT NULL DEFAULT 0,
    claim_membership_id TEXT NOT NULL DEFAULT '',
    attempts_consumed INTEGER NOT NULL DEFAULT 0,
    retry_root_run_id TEXT NOT NULL DEFAULT '',
    executor_location TEXT NOT NULL DEFAULT 'unknown',
    required_coordinator_id TEXT NOT NULL DEFAULT '',
    required_executor_location TEXT NOT NULL DEFAULT '',
    execution_policy_json BLOB,
    execution_policy_hash TEXT NOT NULL DEFAULT '',
    execution_policy_version INTEGER NOT NULL DEFAULT 0,
    execution_body_protocol INTEGER NOT NULL DEFAULT 0,
    execution_supervisor_requirements_json BLOB,
    execution_supervisor_requirements_hash TEXT NOT NULL DEFAULT '',
    execution_body_requirements_json BLOB,
    execution_body_requirements_hash TEXT NOT NULL DEFAULT '',
    avoid_coordinator_id TEXT NOT NULL DEFAULT '',
    avoid_executor_kind TEXT NOT NULL DEFAULT '',
    avoid_executor_id TEXT NOT NULL DEFAULT '',
    avoid_until       INTEGER,
    -- needs_labels: JSON []string from RunsOn; AND semantics.
    needs_labels     BLOB,
    -- prefers_labels: ordered soft executor preferences.
    prefers_labels   BLOB,
    requested_cores  DOUBLE PRECISION NOT NULL DEFAULT 0,
    requested_memory_bytes INTEGER NOT NULL DEFAULT 0,
    requested_slots  INTEGER NOT NULL DEFAULT 1,
    offer_started_at INTEGER,
    offer_priority_target INTEGER NOT NULL DEFAULT 100,
    claim_base_priority INTEGER NOT NULL DEFAULT 0,
    claim_priority    INTEGER NOT NULL DEFAULT 0,
    claim_worker_id   TEXT NOT NULL DEFAULT '',
    claim_executor_kind TEXT NOT NULL DEFAULT '',
    claim_reservation_id TEXT NOT NULL DEFAULT '',
    -- placement_reason: why the claiming runner got this node --
    -- 'preference', 'fallback', 'none', or '' for a node no claim path
    -- decided.
    placement_reason TEXT NOT NULL DEFAULT '',
    -- placement_hold_from: when the node became claimable, which the
    -- local-first hold runs from. Separate from ready_at because a
    -- label-mismatch bump moves ready_at and must not restart the hold.
    placement_hold_from INTEGER,
    -- status_detail: phase string runners write for the dashboard.
    status_detail    TEXT NOT NULL DEFAULT '',
    -- last_heartbeat: runner liveness; for UI, not lease enforcement.
    last_heartbeat   INTEGER,
    -- failure_reason: Failure* constant; empty = uncategorized.
    failure_reason   TEXT NOT NULL DEFAULT '',
    -- exit_code: process exit; NULL when not tied to a process.
    exit_code        INTEGER,
    -- artifact_manifest: content-addressed digest of the node's
    -- published-artifact manifest; empty when it produced none.
    artifact_manifest TEXT NOT NULL DEFAULT '',
    -- seq: creation order within the run, assigned by CreateNode. The
    -- physical row order is not this on Postgres, where an UPDATE moves
    -- the tuple, so the order ListNodes promises needs its own column.
    seq              INTEGER NOT NULL DEFAULT 0, "annotations_json" BLOB, "summary" TEXT NOT NULL DEFAULT '', "max_rss_bytes" INTEGER NOT NULL DEFAULT 0, "process_wall_nanos" INTEGER NOT NULL DEFAULT 0, "cpu_nanos" INTEGER NOT NULL DEFAULT 0, "credit_charged_through" INTEGER NOT NULL DEFAULT 0, "credit_exhausted_anchor" INTEGER NOT NULL DEFAULT 0, "credit_cpu_class" INTEGER NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', "credit_billing_from" INTEGER NOT NULL DEFAULT 0, "optional" INTEGER NOT NULL DEFAULT 0, "retry_budget" INTEGER NOT NULL DEFAULT 0, "retry_backoff_ms" INTEGER NOT NULL DEFAULT 0, "failure_json" BLOB, "kind" TEXT NOT NULL DEFAULT '', "spec_hash" TEXT NOT NULL DEFAULT '', "on_failure_of" TEXT NOT NULL DEFAULT '', "continue_on_error" INTEGER NOT NULL DEFAULT 0, "cancel_requested_at" INTEGER, "approval_json" BLOB, "attention_reason" TEXT NOT NULL DEFAULT '', "timeout_ms" INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE agent_loss_retry_node_sources (
    retry_run_id       TEXT NOT NULL,
    source_run_id      TEXT NOT NULL,
    node_id            TEXT NOT NULL,
    deps_json          BLOB NOT NULL,
    needs_labels_json  BLOB,
    prefers_labels_json BLOB,
    requested_cores    DOUBLE PRECISION NOT NULL DEFAULT 0,
    requested_memory_bytes INTEGER NOT NULL DEFAULT 0,
    requested_slots    INTEGER NOT NULL DEFAULT 1,
    attempts_consumed  INTEGER NOT NULL DEFAULT 0,
    required_coordinator_id TEXT NOT NULL DEFAULT '',
    required_executor_location TEXT NOT NULL DEFAULT '',
    avoid_coordinator_id TEXT NOT NULL DEFAULT '',
    avoid_executor_kind TEXT NOT NULL DEFAULT '',
    avoid_executor_id TEXT NOT NULL DEFAULT '',
    avoid_until INTEGER,
    policy_json        BLOB,
    policy_hash        TEXT NOT NULL DEFAULT '',
    policy_version     INTEGER NOT NULL DEFAULT 0,
    body_protocol      INTEGER NOT NULL DEFAULT 0,
    supervisor_requirements_json BLOB,
    supervisor_requirements_hash TEXT NOT NULL DEFAULT '',
    body_requirements_json BLOB,
    body_requirements_hash TEXT NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (retry_run_id, node_id),
    FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE agent_loss_retry_legacy_deny_all (
    retry_run_id  TEXT PRIMARY KEY,
    source_run_id TEXT NOT NULL,
    reason        TEXT NOT NULL, "team" TEXT NOT NULL DEFAULT 'default',
    FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE events (
    run_id   TEXT NOT NULL,
    seq      INTEGER NOT NULL,
    node_id  TEXT NOT NULL DEFAULT '',
    kind     TEXT NOT NULL,
    ts       INTEGER NOT NULL,
    payload  BLOB, "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (run_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE triggers (
    id                    TEXT PRIMARY KEY,
    pipeline              TEXT NOT NULL,
    args_json             BLOB,
    trigger_source        TEXT NOT NULL DEFAULT '',
    trigger_user          TEXT NOT NULL DEFAULT '',
    trigger_env           BLOB,
    git_branch            TEXT NOT NULL DEFAULT '',
    git_sha               TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'pending',
    created_at            INTEGER NOT NULL,
    claimed_at            INTEGER,
    lease_expires_at      INTEGER,
    cancel_requested_at   INTEGER,
    repo                  TEXT NOT NULL DEFAULT '',
    repo_url              TEXT NOT NULL DEFAULT '',
    github_owner          TEXT NOT NULL DEFAULT '',
    github_repo           TEXT NOT NULL DEFAULT '',
    repo_inherited        INTEGER NOT NULL DEFAULT 0,
    retry_of              TEXT NOT NULL DEFAULT '',
    parent_node_id        TEXT NOT NULL DEFAULT '',
    idempotency_key       TEXT NOT NULL DEFAULT '',
    claim_seq             INTEGER NOT NULL DEFAULT 0,
    claim_principal       TEXT NOT NULL DEFAULT '',
    claim_token_prefix    TEXT NOT NULL DEFAULT '',
    webhook_delivery      TEXT NOT NULL DEFAULT '',
    webhook_replay_key    TEXT NOT NULL DEFAULT '',
    available_at          INTEGER NOT NULL DEFAULT 0
, "parent_run_id" TEXT, "retry_source" TEXT NOT NULL DEFAULT '', "full" INTEGER NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', "credit_reserved_at" INTEGER NOT NULL DEFAULT 0, "github_repo_id" BIGINT NOT NULL DEFAULT 0, "github_check_run_id" INTEGER NOT NULL DEFAULT 0, "credit_paid_seconds" INTEGER NOT NULL DEFAULT 0, "credit_paid_amount_micro" INTEGER NOT NULL DEFAULT 0, "credit_reservation_id" TEXT NOT NULL DEFAULT '');

CREATE TABLE node_steps (
    run_id      TEXT NOT NULL,
    node_id     TEXT NOT NULL,
    step_id     TEXT NOT NULL,
    status      TEXT NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER, "annotations_json" BLOB, "summary" TEXT NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (run_id, node_id, step_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE node_metrics (
    run_id          TEXT NOT NULL,
    node_id         TEXT NOT NULL,
    ts              INTEGER NOT NULL,
    cpu_millicores  INTEGER NOT NULL,
    memory_bytes    INTEGER NOT NULL, "cpu_time_nanos" INTEGER NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', "kind" TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id, ts),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE tokens (
    hash         TEXT PRIMARY KEY,
    prefix       TEXT NOT NULL,
    principal    TEXT NOT NULL,
    kind         TEXT NOT NULL,        -- user | runner | service
    scopes       TEXT NOT NULL,        -- comma-separated set
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,              -- NULL = never expires
    last_used_at INTEGER,
    revoked_at   INTEGER,
    replaced_by  TEXT                  -- prefix of rotation successor
, "metered" INTEGER NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', "created_by" TEXT NOT NULL DEFAULT '', "advertised_labels_json" BLOB);

CREATE TABLE sessions (
    hash          TEXT PRIMARY KEY,
    principal     TEXT NOT NULL,
    scopes        TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_used_at  INTEGER
, "team" TEXT NOT NULL DEFAULT 'default', "account_id" TEXT NOT NULL DEFAULT '', "token_prefix" TEXT NOT NULL DEFAULT '');

CREATE TABLE debug_pauses (
    run_id       TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    reason       TEXT NOT NULL,
    paused_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    released_at  INTEGER,
    released_by  TEXT NOT NULL DEFAULT '',
    release_kind TEXT NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (run_id, node_id, reason),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE approvals (
    run_id       TEXT    NOT NULL,
    node_id      TEXT    NOT NULL,
    requested_at INTEGER NOT NULL,
    message      TEXT    NOT NULL DEFAULT '',
    timeout_ms   INTEGER NOT NULL DEFAULT 0,
    on_timeout   TEXT    NOT NULL DEFAULT 'fail',
    approver     TEXT    NOT NULL DEFAULT '',
    resolved_at  INTEGER,
    resolution   TEXT    NOT NULL DEFAULT '',
    comment      TEXT    NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE node_dispatches (
    run_id              TEXT NOT NULL,
    node_id             TEXT NOT NULL,
    seq                 INTEGER NOT NULL,
    dispatched_at       INTEGER NOT NULL,
    code_version        TEXT NOT NULL DEFAULT '',
    binary_hash         TEXT NOT NULL DEFAULT '',
    runner_labels       BLOB,
    env_json            BLOB,
    workdir             TEXT NOT NULL DEFAULT '',
    input_envelope_json BLOB,
    input_size_bytes    INTEGER NOT NULL DEFAULT 0,
    secret_redactions   INTEGER NOT NULL DEFAULT 0,
    redacted_keys       BLOB, "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (run_id, node_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE sparkwing_meta (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE node_bounces (
    run_id       TEXT    NOT NULL,
    node_id      TEXT    NOT NULL,
    seq          INTEGER NOT NULL,
    requested_at INTEGER NOT NULL,
    requested_by TEXT    NOT NULL DEFAULT '',
    consumed_at  INTEGER,
    outcome      TEXT    NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (run_id, node_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE agent_loss_retries (
    run_id           TEXT PRIMARY KEY,
    source_run_id    TEXT NOT NULL,
    root_run_id      TEXT NOT NULL,
    cause_nodes_json BLOB NOT NULL,
    available_at     INTEGER NOT NULL,
    deadline_at      INTEGER NOT NULL,
    retry_count      INTEGER NOT NULL, "team" TEXT NOT NULL DEFAULT 'default',
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE node_execution_attempts (
    lineage_root_run_id TEXT NOT NULL,
    run_id              TEXT NOT NULL,
    node_id             TEXT NOT NULL,
    attempt_ordinal     INTEGER NOT NULL,
    claim_generation    INTEGER NOT NULL,
    coordinator_id      TEXT NOT NULL,
    membership_id       TEXT NOT NULL,
    executor_kind       TEXT NOT NULL,
    executor_name       TEXT NOT NULL DEFAULT '',
    executor_id         TEXT NOT NULL,
    executor_location   TEXT NOT NULL,
    holder_id           TEXT NOT NULL,
    reservation_id      TEXT NOT NULL,
    started_at          INTEGER NOT NULL,
    finished_at         INTEGER,
    outcome             TEXT NOT NULL DEFAULT '',
    failure_reason      TEXT NOT NULL DEFAULT '',
    retry_run_id        TEXT NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (lineage_root_run_id, node_id, attempt_ordinal),
    UNIQUE (run_id, node_id, claim_generation, attempt_ordinal),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE run_definition_plans (
    run_id    TEXT PRIMARY KEY,
    plan_hash TEXT NOT NULL, "team" TEXT NOT NULL DEFAULT 'default',
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE cron_schedules (
    id            TEXT PRIMARY KEY,
    repo_path     TEXT NOT NULL,
    github_installation_id INTEGER NOT NULL DEFAULT 0,
    github_repository_id INTEGER NOT NULL DEFAULT 0,
    pipeline      TEXT NOT NULL,
    -- 'default' when the repository declares a single schedule for the pipeline.
    schedule_name TEXT NOT NULL DEFAULT 'default',
    cron          TEXT NOT NULL,
    tz            TEXT NOT NULL,
    -- skip | queue
    overlap       TEXT NOT NULL,
    catch_up_ns   INTEGER NOT NULL,
    -- local | controller
    where_        TEXT NOT NULL DEFAULT 'local',
    -- JSON object of CLI argument name to value, passed to the scheduled run.
    args          TEXT NOT NULL DEFAULT '{}',
    -- commit the schedule is pinned to; empty means it follows the checkout.
    locked_ref    TEXT NOT NULL DEFAULT '',
    -- the pipeline binary that pin resolved to and the cache digest it was
    -- built from, both empty while the schedule follows the checkout.
    locked_binary TEXT NOT NULL DEFAULT '',
    locked_digest TEXT NOT NULL DEFAULT '',
    paused        INTEGER NOT NULL DEFAULT 0,
    -- 0 once the repository stops declaring the schedule, which keeps the
    -- row and its history readable after the declaration is withdrawn.
    declared      INTEGER NOT NULL DEFAULT 1,
    armed_at      INTEGER NOT NULL,
    armed_by      TEXT NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL,
    -- last due instant resolved, however it resolved, so a tick never
    -- reconsiders an instant an earlier tick already decided.
    cursor_at     INTEGER NOT NULL,
    last_fired_at INTEGER,
    last_run_id   TEXT NOT NULL DEFAULT '',
    last_outcome  TEXT NOT NULL DEFAULT '',
    -- NULL when the expression never matches again.
    next_due_at   INTEGER,
    -- host overrides of the declaration, each NULL when that field is not
    -- overridden. override_base is the declaration the override was set
    -- against, so a reader can tell an override the repository has since
    -- moved under; both it and override_set_at are NULL when the schedule
    -- carries no override.
    override_cron        TEXT,
    override_tz          TEXT,
    override_overlap     TEXT,
    override_catch_up_ns INTEGER,
    override_args        TEXT,
    override_base        TEXT,
    override_set_at      INTEGER
, "git_branch" TEXT NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default');

CREATE TABLE cron_fires (
    id          TEXT PRIMARY KEY,
    schedule_id TEXT NOT NULL,
    due_at      INTEGER NOT NULL,
    decided_at  INTEGER NOT NULL,
    -- fired | skipped_overlap | missed | failed
    outcome     TEXT NOT NULL,
    run_id      TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '',
    -- JSON object of the arguments the launch was given.
    args        TEXT NOT NULL DEFAULT '{}'
, "team" TEXT NOT NULL DEFAULT 'default');

CREATE TABLE credit_grants (
    id           TEXT PRIMARY KEY,
    -- free | paid | reversal; a reversal carries a negative amount
    kind         TEXT NOT NULL,
    amount_micro INTEGER NOT NULL,
    -- payment id or operator note; empty for an unreferenced grant
    reference    TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
, "reverses" TEXT NOT NULL DEFAULT '', "team" TEXT NOT NULL DEFAULT 'default');

CREATE TABLE credit_charges (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    -- reservation | usage | refund; a refund carries negative seconds and amount
    kind         TEXT NOT NULL DEFAULT 'usage',
    seconds      INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    -- principal: the team whose storage or runner work this row billed;
    -- empty on legacy and unowned work.
    principal    TEXT NOT NULL DEFAULT '',
    -- storage_bytes: retained bytes above the free allowance a storage row
    -- billed; 0 on every other kind.
    storage_bytes INTEGER NOT NULL DEFAULT 0,
    -- cpu_class: whole cores of the class billed; 0 for a row written before
    -- the rate table, which was billed at credit_rate_micro_per_second.
    cpu_class    INTEGER NOT NULL DEFAULT 0,
    rate_micro_per_second INTEGER NOT NULL DEFAULT 0,
    charged_at   INTEGER NOT NULL
, "team" TEXT NOT NULL DEFAULT 'default', "card_fingerprint" TEXT NOT NULL DEFAULT '');

CREATE TABLE storage_quotas (
    principal           TEXT PRIMARY KEY,
    tier                TEXT    NOT NULL DEFAULT '',
    max_bytes_per_run   INTEGER NOT NULL DEFAULT 0,
    max_bytes_per_month INTEGER NOT NULL DEFAULT 0,
    max_objects_per_run INTEGER NOT NULL DEFAULT 0,
    -- storage_allowance_bytes: retained bytes the customer asked to keep;
    -- 0 keeps everything.
    storage_allowance_bytes INTEGER NOT NULL DEFAULT 0,
    updated_at          INTEGER NOT NULL
, "team" TEXT NOT NULL DEFAULT 'default');

CREATE TABLE storage_run_usage (
    principal  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    objects    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL, "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (principal, run_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE storage_month_usage (
    principal  TEXT NOT NULL,
    month      TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    objects    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL, "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (principal, month)
);

CREATE TABLE egress_usage (
    -- the principal the bytes were served to, or "anonymous"
    principal  TEXT NOT NULL,
    -- the UTC month the bytes fell in, as "2006-01"
    month      TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL, "team" TEXT NOT NULL DEFAULT 'default',
    PRIMARY KEY (principal, month)
);

CREATE TABLE teams (
    name       TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
, "credit_exhausted_at" INTEGER NOT NULL DEFAULT 0, "display_name" TEXT NOT NULL DEFAULT '', "created_by" TEXT NOT NULL DEFAULT '', "billing_trust_at" INTEGER NOT NULL DEFAULT 0, "billing_trust_reason" TEXT NOT NULL DEFAULT '', "billing_purchase_limit_cents" INTEGER NOT NULL DEFAULT 0, "billing_trust" TEXT NOT NULL DEFAULT '', "billing_trust_by" TEXT NOT NULL DEFAULT '', "billing_runner_cap" INTEGER NOT NULL DEFAULT 0, "card_customer" TEXT NOT NULL DEFAULT '', "card_payment_method" TEXT NOT NULL DEFAULT '', "card_fingerprint" TEXT NOT NULL DEFAULT '', "card_brand" TEXT NOT NULL DEFAULT '', "card_added_at" INTEGER NOT NULL DEFAULT 0, "card_owed_since" INTEGER NOT NULL DEFAULT 0, "billing_rung_cents" INTEGER NOT NULL DEFAULT 0, "billing_daily_cap_cents" INTEGER NOT NULL DEFAULT 0, "card_last4" TEXT NOT NULL DEFAULT '', "billing_budget_cents" INTEGER NOT NULL DEFAULT 0);

CREATE TABLE "secrets" ("name" TEXT NOT NULL, "value" TEXT NOT NULL, "principal" TEXT NOT NULL, "created_at" INTEGER NOT NULL, "updated_at" INTEGER NOT NULL, "masked" INTEGER NOT NULL DEFAULT 1, "pipeline" TEXT NOT NULL DEFAULT '', "shared" INTEGER NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', PRIMARY KEY ("team", "name", "pipeline"));

CREATE TABLE "pipeline_profiles" ("pipeline" TEXT NOT NULL, "node_id" TEXT NOT NULL, "p50_duration_ms" INTEGER NOT NULL, "p99_duration_ms" INTEGER NOT NULL, "peak_cores" REAL NOT NULL, "peak_memory_bytes" INTEGER NOT NULL, "sample_count" INTEGER NOT NULL, "updated_at" INTEGER NOT NULL, "samples_json" BLOB, "pinned_cores" REAL NOT NULL DEFAULT 0, "pinned_memory_bytes" INTEGER NOT NULL DEFAULT 0, "cpu_measured" INTEGER NOT NULL DEFAULT 0, "wait_samples_json" BLOB, "wait_p50_ms" INTEGER NOT NULL DEFAULT 0, "wait_p99_ms" INTEGER NOT NULL DEFAULT 0, "wait_sample_count" INTEGER NOT NULL DEFAULT 0, "contended_count" INTEGER NOT NULL DEFAULT 0, "prev_peak_memory_bytes" INTEGER NOT NULL DEFAULT 0, "plan_hash" TEXT NOT NULL DEFAULT '', "floor_cores" REAL NOT NULL DEFAULT 0, "floor_memory_bytes" INTEGER NOT NULL DEFAULT 0, "prev_peak_cores" REAL NOT NULL DEFAULT 0, "sustained_cores" REAL NOT NULL DEFAULT 0, "prev_sustained_cores" REAL NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', PRIMARY KEY ("team", "pipeline", "node_id"));

CREATE TABLE "concurrency_entries" ("key" TEXT, "capacity" INTEGER NOT NULL DEFAULT 1, "previous_capacity" INTEGER, "last_write_run_id" TEXT NOT NULL DEFAULT '', "last_write_node_id" TEXT NOT NULL DEFAULT '', "updated_at" INTEGER NOT NULL, "team" TEXT NOT NULL DEFAULT 'default', PRIMARY KEY ("team", "key"));

CREATE TABLE "concurrency_holders" ("key" TEXT NOT NULL, "holder_id" TEXT NOT NULL, "run_id" TEXT NOT NULL, "node_id" TEXT NOT NULL DEFAULT '', "claimed_at" INTEGER NOT NULL, "queue_arrived_at" INTEGER NOT NULL DEFAULT 0, "lease_expires_at" INTEGER NOT NULL, "superseded" INTEGER NOT NULL DEFAULT 0, "cost" INTEGER NOT NULL DEFAULT 1, "declared_capacity" INTEGER NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', PRIMARY KEY ("team", "key", "holder_id"));

CREATE TABLE "concurrency_waiters" ("key" TEXT NOT NULL, "run_id" TEXT NOT NULL, "node_id" TEXT NOT NULL DEFAULT '', "holder_id" TEXT NOT NULL DEFAULT '', "arrived_at" INTEGER NOT NULL, "policy" TEXT NOT NULL, "cache_key_hash" TEXT NOT NULL DEFAULT '', "leader_run_id" TEXT NOT NULL DEFAULT '', "leader_node_id" TEXT NOT NULL DEFAULT '', "cancel_timeout_ns" INTEGER NOT NULL DEFAULT 0, "cost" INTEGER NOT NULL DEFAULT 1, "declared_capacity" INTEGER NOT NULL DEFAULT 0, "team" TEXT NOT NULL DEFAULT 'default', PRIMARY KEY ("team", "key", "run_id", "node_id"));

CREATE TABLE "concurrency_cache" ("key" TEXT NOT NULL, "cache_key_hash" TEXT NOT NULL, "output_ref" TEXT NOT NULL, "origin_run_id" TEXT NOT NULL, "origin_node_id" TEXT NOT NULL, "created_at" INTEGER NOT NULL, "expires_at" INTEGER NOT NULL, "last_hit_at" INTEGER NOT NULL, "team" TEXT NOT NULL DEFAULT 'default', PRIMARY KEY ("team", "key", "cache_key_hash"));

CREATE TABLE accounts (
    id             TEXT PRIMARY KEY,
    email          TEXT NOT NULL,
    email_verified INTEGER NOT NULL DEFAULT 0,
    name           TEXT NOT NULL DEFAULT '',
    active_team    TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
, "teams_created" INTEGER NOT NULL DEFAULT 0, "waitlist_reason" TEXT NOT NULL DEFAULT '', "waitlisted_at" INTEGER NOT NULL DEFAULT 0);

CREATE TABLE identities (
    provider       TEXT NOT NULL,
    subject        TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    email          TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL, "linked" INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, subject)
);

CREATE TABLE memberships (
    team       TEXT NOT NULL,
    account_id TEXT NOT NULL,
    role       TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'reader')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (team, account_id)
);

CREATE TABLE invitations (
    id          TEXT PRIMARY KEY,
    team        TEXT NOT NULL,
    email       TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'reader')),
    invited_by  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    accepted_at INTEGER,
    accepted_by TEXT NOT NULL DEFAULT '',
    withdrawn_at INTEGER
, "emailed_at" INTEGER);

CREATE TABLE github_runner_bindings (
    team                TEXT NOT NULL,
    repository_id       INTEGER NOT NULL,
    repository_owner_id INTEGER NOT NULL,
    repository          TEXT NOT NULL,
    created_by          TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    PRIMARY KEY (team, repository_id)
);

CREATE TABLE github_runner_credentials (
    team       TEXT NOT NULL,
    prefix     TEXT NOT NULL,
    branch     TEXT NOT NULL,
    sha        TEXT NOT NULL,
    expires_at INTEGER NOT NULL, "run_id" TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team, prefix)
);

CREATE TABLE github_app_installations (
    installation_id INTEGER PRIMARY KEY,
    team            TEXT NOT NULL,
    account_id      INTEGER NOT NULL,
    account_login   TEXT NOT NULL,
    account_type    TEXT NOT NULL,
    suspended       INTEGER NOT NULL DEFAULT 0,
    connected_by    TEXT NOT NULL DEFAULT '',
    github_user_id  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE github_app_triggers (
    team               TEXT NOT NULL,
    repository_id      INTEGER NOT NULL,
    pipeline           TEXT NOT NULL,
    repository         TEXT NOT NULL,
    installation_id    INTEGER NOT NULL,
    on_push            INTEGER NOT NULL DEFAULT 0,
    on_tags            INTEGER NOT NULL DEFAULT 0,
    tag_patterns       TEXT NOT NULL DEFAULT '[]',
    on_pull_request    INTEGER NOT NULL DEFAULT 0,
    created_by         TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL, "base_branches" TEXT NOT NULL DEFAULT '[]', "branches" TEXT NOT NULL DEFAULT '[]', "on_pr_closed" INTEGER NOT NULL DEFAULT 0, "on_pr_labeled" INTEGER NOT NULL DEFAULT 0, "pr_labels" TEXT NOT NULL DEFAULT '[]', "on_pr_ready_for_review" INTEGER NOT NULL DEFAULT 0, "on_release_published" INTEGER NOT NULL DEFAULT 0, "on_release_prereleased" INTEGER NOT NULL DEFAULT 0, "on_branch_create" INTEGER NOT NULL DEFAULT 0, "on_branch_delete" INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (team, repository_id, pipeline)
);

CREATE TABLE github_app_connect_states (
    nonce      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

CREATE TABLE github_app_deliveries (
    digest      TEXT PRIMARY KEY,
    delivery_id TEXT NOT NULL,
    received_at INTEGER NOT NULL,
    event       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE team_deletions (
    slug         TEXT PRIMARY KEY,
    requested_by TEXT NOT NULL DEFAULT '',
    requested_at INTEGER NOT NULL,
    state        TEXT NOT NULL CHECK (state IN ('pending', 'done')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    finished_at  INTEGER,
    recheck_at   INTEGER,
    lease_holder TEXT NOT NULL DEFAULT '',
    lease_until  INTEGER
);

CREATE TABLE invitation_email_log (
    address TEXT NOT NULL,
    sent_at INTEGER NOT NULL
);

CREATE TABLE free_slots (
    team        TEXT PRIMARY KEY,
    taken_at    INTEGER NOT NULL,
    event_bytes INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE signup_gate (
    id                      INTEGER PRIMARY KEY CHECK (id = 1),
    mode                    TEXT NOT NULL CHECK (mode IN ('open', 'waitlist')),
    source                  TEXT NOT NULL DEFAULT '',
    reason                  TEXT NOT NULL DEFAULT '',
    set_by                  TEXT NOT NULL DEFAULT '',
    updated_at              INTEGER NOT NULL,
    hourly_limit            INTEGER NOT NULL,
    daily_limit             INTEGER NOT NULL,
    hourly_warn             INTEGER NOT NULL,
    github_min_account_days INTEGER NOT NULL,
    free_team_members       INTEGER NOT NULL
);

CREATE TABLE signup_admissions (
    account_id TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('signup', 'invitation')),
    at         INTEGER NOT NULL,
    PRIMARY KEY (account_id, kind, at)
);

CREATE TABLE credit_checkouts (
    id           TEXT PRIMARY KEY,
    team         TEXT NOT NULL,
    session_id   TEXT NOT NULL DEFAULT '',
    amount_micro INTEGER NOT NULL,
    opened_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    paid_at      INTEGER
, "closed_at" INTEGER);

CREATE TABLE credit_freezes (
    dispute_id  TEXT PRIMARY KEY,
    team        TEXT NOT NULL,
    payment_id  TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    released_at INTEGER
, "cause" TEXT NOT NULL DEFAULT '');

CREATE TABLE team_storage (
    team            TEXT NOT NULL,
    store           TEXT NOT NULL,
    used_bytes      INTEGER NOT NULL DEFAULT 0,
    reserved_bytes  INTEGER NOT NULL DEFAULT 0,
    committed_bytes INTEGER NOT NULL DEFAULT 0,
    reconciled_at   INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (team, store)
);

CREATE TABLE storage_reservations (
    id         TEXT PRIMARY KEY,
    team       TEXT NOT NULL,
    store      TEXT NOT NULL,
    bytes      INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE TABLE team_download_day (
    team       TEXT NOT NULL,
    day        TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (team, day)
);

CREATE TABLE egress_day (
    service    TEXT NOT NULL,
    period     TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (service, period)
);

CREATE TABLE git_credentials (
    id           TEXT PRIMARY KEY,
    team         TEXT NOT NULL,
    host         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    username     TEXT NOT NULL DEFAULT '',
    secret       TEXT NOT NULL,
    known_hosts  TEXT NOT NULL DEFAULT '',
    fingerprint  TEXT NOT NULL DEFAULT '',
    confirmed_at INTEGER,
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE git_credential_releases (
    id            TEXT PRIMARY KEY,
    team          TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    host          TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    runner        TEXT NOT NULL,
    token_prefix  TEXT NOT NULL,
    released_at   INTEGER NOT NULL
);

CREATE TABLE git_credential_machines (
    team         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    enabled_by   TEXT NOT NULL DEFAULT '',
    enabled_at   INTEGER NOT NULL,
    PRIMARY KEY (team, token_prefix)
);

CREATE TABLE github_app_extra_repos (
    team        TEXT NOT NULL,
    repository  TEXT NOT NULL,
    extra_repo  TEXT NOT NULL,
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL, "extra_repo_id" INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (team, repository, extra_repo)
);

CREATE TABLE identity_link_states (
    nonce      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

CREATE TABLE identity_unlinks (
    provider    TEXT NOT NULL,
    subject     TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    unlinked_at INTEGER NOT NULL,
    PRIMARY KEY (provider, subject, account_id)
);

CREATE TABLE uploads (
    id TEXT PRIMARY KEY,
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    store TEXT NOT NULL,
    key TEXT NOT NULL,
    size BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    principal TEXT NOT NULL,
    claim_prefix TEXT NOT NULL,
    provenance TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    committed_at BIGINT NOT NULL DEFAULT 0
, "ref" TEXT NOT NULL DEFAULT '', "repo" TEXT NOT NULL DEFAULT '');

CREATE TABLE data_objects (
    team TEXT NOT NULL,
    key TEXT NOT NULL,
    store TEXT NOT NULL,
    size BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    principal TEXT NOT NULL,
    provenance TEXT NOT NULL,
    committed_at BIGINT NOT NULL, "ref" TEXT NOT NULL DEFAULT '', "repo" TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team, key, provenance)
);

CREATE TABLE team_build_trust (
    team TEXT PRIMARY KEY,
    trust_local_builds BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE storage_commit_receipts (
    team          TEXT NOT NULL,
    store         TEXT NOT NULL,
    id            TEXT NOT NULL,
    bytes         BIGINT NOT NULL,
    committed_at  BIGINT NOT NULL,
    renew_request BLOB,
    renew_status  TEXT NOT NULL DEFAULT '',
    renew_result  BLOB,
    PRIMARY KEY (team, store, id)
);

CREATE TABLE claim_tokens (
    digest           TEXT PRIMARY KEY,
    prefix           TEXT NOT NULL,
    team             TEXT NOT NULL,
    run_id           TEXT NOT NULL,
    node_id          TEXT NOT NULL,
    claim_generation INTEGER NOT NULL,
    kind             TEXT NOT NULL,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    result_digest    TEXT NOT NULL DEFAULT '', "source_mints" INTEGER NOT NULL DEFAULT 0,
    UNIQUE (team, run_id, node_id, claim_generation),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE child_invocations (
    team             TEXT NOT NULL,
    parent_run_id    TEXT NOT NULL,
    parent_node_id   TEXT NOT NULL,
    claim_generation INTEGER NOT NULL,
    ordinal          INTEGER NOT NULL,
    request_digest   TEXT NOT NULL,
    child_run_id     TEXT NOT NULL,
    created_at       INTEGER NOT NULL,
    PRIMARY KEY (team, parent_run_id, parent_node_id, claim_generation, ordinal),
    FOREIGN KEY (parent_run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE business_events (
    id         TEXT PRIMARY KEY,
    ts         INTEGER NOT NULL,
    team       TEXT NOT NULL DEFAULT '',
    account    TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL,
    subject_id TEXT NOT NULL DEFAULT '',
    actor      TEXT NOT NULL DEFAULT '',
    attrs      TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE repos (
    team       TEXT NOT NULL,
    repo       TEXT NOT NULL,
    dispatch   TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (team, repo)
);

CREATE TABLE card_charges (
    id              TEXT PRIMARY KEY,
    team            TEXT NOT NULL,
    amount_micro    INTEGER NOT NULL,
    cause           TEXT NOT NULL,
    state           TEXT NOT NULL,
    failures        INTEGER NOT NULL DEFAULT 0,
    decline_code    TEXT NOT NULL DEFAULT '',
    next_attempt_at INTEGER NOT NULL,
    payment_intent  TEXT NOT NULL DEFAULT '',
    opened_at       INTEGER NOT NULL,
    closed_at       INTEGER
);

CREATE TABLE card_attempts (
    id             TEXT PRIMARY KEY,
    team           TEXT NOT NULL,
    charge_id      TEXT NOT NULL,
    kind           TEXT NOT NULL,
    status         TEXT NOT NULL,
    payment_intent TEXT NOT NULL DEFAULT '',
    decline_code   TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE TABLE card_refunds (
    team            TEXT NOT NULL,
    payment_intent  TEXT NOT NULL,
    reason          TEXT NOT NULL,
    refund_id       TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'due',
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (team, payment_intent)
);

CREATE TABLE team_spend_days (
    team         TEXT NOT NULL,
    day          INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    PRIMARY KEY (team, day)
);

CREATE TABLE card_spend_days (
    fingerprint  TEXT NOT NULL,
    day          INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    PRIMARY KEY (fingerprint, day)
);

CREATE TABLE payment_warnings (
    warning_id     TEXT PRIMARY KEY,
    payment_intent TEXT NOT NULL,
    fingerprint    TEXT NOT NULL DEFAULT '',
    actionable     INTEGER NOT NULL DEFAULT 1,
    created_at     INTEGER NOT NULL
);

CREATE TABLE node_outputs (
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    attempt BIGINT NOT NULL,
    key TEXT NOT NULL,
    size BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    PRIMARY KEY (team, run_id, node_id)
);

CREATE TABLE output_runs (
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    PRIMARY KEY (team, run_id)
);

CREATE TABLE output_tombstones (
    team TEXT NOT NULL,
    key TEXT NOT NULL,
    run_id TEXT NOT NULL,
    PRIMARY KEY (team, key)
);

CREATE TABLE github_app_automation (
    team TEXT NOT NULL,
    repository_id BIGINT NOT NULL,
    repository TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    enabled_by TEXT NOT NULL,
    enabled_at BIGINT NOT NULL,
    PRIMARY KEY (team, repository_id),
    FOREIGN KEY (installation_id) REFERENCES github_app_installations(installation_id) ON DELETE CASCADE
);

CREATE TABLE node_logs (
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    attempt BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    prefix TEXT NOT NULL,
    uploaded_through BIGINT NOT NULL DEFAULT 0,
    stored_bytes BIGINT NOT NULL DEFAULT 0,
    charged_bytes BIGINT NOT NULL DEFAULT 0,
    sealed_at BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (team, run_id, node_id, attempt, generation)
);

CREATE TABLE trigger_claim_tokens (
    digest      TEXT PRIMARY KEY,
    prefix      TEXT NOT NULL,
    team        TEXT NOT NULL,
    trigger_id  TEXT NOT NULL,
    claim_seq   INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    UNIQUE (team, trigger_id, claim_seq),
    FOREIGN KEY (trigger_id) REFERENCES triggers(id) ON DELETE CASCADE
);

CREATE TABLE node_log_segments (
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    attempt BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    n BIGINT NOT NULL,
    stored_bytes BIGINT NOT NULL,
    md5 TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    PRIMARY KEY (team, run_id, node_id, attempt, generation, n)
);

CREATE INDEX idx_runs_started ON runs(started_at DESC);

CREATE INDEX idx_runs_pipeline ON runs(pipeline, started_at DESC);

CREATE INDEX idx_runs_sha_started ON runs(git_sha, started_at DESC);

CREATE INDEX idx_runs_branch_started ON runs(git_branch, started_at DESC);

CREATE INDEX idx_runs_repo_slug_started ON runs(declared_repo, started_at DESC);

CREATE INDEX idx_runs_repo_sha_started ON runs(declared_repo, git_sha, started_at DESC);

CREATE INDEX idx_runs_repo_branch_started ON runs(declared_repo, git_branch, started_at DESC);

CREATE INDEX idx_nodes_claimable
    ON nodes(ready_at)
    WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND status != 'done';

CREATE INDEX idx_nodes_claimed_lease
    ON nodes(lease_expires_at)
    WHERE claimed_by IS NOT NULL;

CREATE INDEX idx_events_run_ts ON events(run_id, ts);

CREATE INDEX idx_triggers_claimed_lease
    ON triggers(status, lease_expires_at) WHERE status = 'claimed';

CREATE INDEX idx_triggers_source_status_created
    ON triggers(trigger_source, status, created_at);

CREATE INDEX idx_node_steps_lookup
    ON node_steps(run_id, node_id);

CREATE INDEX idx_node_metrics_lookup
    ON node_metrics(run_id, node_id, ts);

CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE INDEX idx_debug_pauses_open
    ON debug_pauses(run_id) WHERE released_at IS NULL;

CREATE INDEX idx_approvals_pending
    ON approvals(requested_at) WHERE resolved_at IS NULL;

CREATE INDEX idx_node_dispatches_lookup
    ON node_dispatches(run_id, node_id, seq DESC);

CREATE INDEX idx_node_bounces_pending
    ON node_bounces(run_id, node_id) WHERE consumed_at IS NULL;

CREATE UNIQUE INDEX idx_tokens_prefix ON tokens(prefix);

CREATE INDEX idx_triggers_pending
    ON triggers(status, available_at, created_at) WHERE status = 'pending';

CREATE INDEX idx_agent_loss_retries_source
    ON agent_loss_retries(source_run_id);

CREATE INDEX idx_node_execution_attempts_run
    ON node_execution_attempts(run_id, node_id, attempt_ordinal);

CREATE INDEX idx_nodes_assisted_claimable
    ON nodes(execution_policy_version, ready_at, run_id, node_id)
    WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND outcome = '' AND finished_at IS NULL AND execution_policy_hash != '';

CREATE INDEX idx_cron_fires_schedule_decided
    ON cron_fires(schedule_id, decided_at DESC);

CREATE INDEX idx_credit_grants_created ON credit_grants(created_at);

CREATE INDEX idx_credit_charges_charged ON credit_charges(charged_at);

CREATE INDEX idx_credit_charges_node ON credit_charges(run_id, node_id);

CREATE INDEX idx_events_ts ON events(ts);

CREATE INDEX idx_node_metrics_ts ON node_metrics(ts);

CREATE INDEX idx_nodes_outstanding
    ON nodes(status, ready_at, claimed_by)
    WHERE status != 'done';

CREATE INDEX idx_nodes_credit_window
    ON nodes(credit_charged_through)
    WHERE credit_charged_through != 0;

CREATE INDEX idx_credit_grants_kind_amount
    ON credit_grants(kind, amount_micro);

CREATE INDEX idx_credit_charges_kind_amount
    ON credit_charges(kind, amount_micro, seconds);

CREATE INDEX idx_nodes_credit_active ON nodes(credit_charged_through);

CREATE INDEX idx_nodes_credit_principal ON nodes(claim_principal, credit_charged_through);

CREATE INDEX idx_runs_created ON runs(created_at);

CREATE INDEX idx_runs_principal_created ON runs(created_principal, created_at);

CREATE INDEX idx_egress_usage_month ON egress_usage(month);

CREATE INDEX idx_credit_grants_kind_created
    ON credit_grants(kind, created_at);

CREATE INDEX idx_runs_team_started ON runs(team, started_at DESC);

CREATE INDEX idx_concurrency_holders_key_claimed
    ON concurrency_holders(team, key, claimed_at);

CREATE INDEX idx_concurrency_holders_lease
    ON concurrency_holders(lease_expires_at);

CREATE INDEX idx_concurrency_waiters_arrived
    ON concurrency_waiters(team, key, arrived_at);

CREATE INDEX idx_concurrency_cache_expires
    ON concurrency_cache(expires_at);

CREATE INDEX idx_concurrency_cache_lru
    ON concurrency_cache(last_hit_at);

CREATE INDEX idx_concurrency_cache_origin_run
    ON concurrency_cache(origin_run_id);

CREATE UNIQUE INDEX idx_accounts_verified_email ON accounts(email) WHERE email_verified = 1;

CREATE INDEX idx_identities_account ON identities(account_id);

CREATE INDEX idx_memberships_account ON memberships(account_id);

CREATE UNIQUE INDEX idx_invitations_open ON invitations(team, email)
    WHERE accepted_at IS NULL AND withdrawn_at IS NULL;

CREATE INDEX idx_invitations_email ON invitations(email);

CREATE UNIQUE INDEX idx_cron_schedules_repo_pipeline_name
    ON cron_schedules(team, repo_path, pipeline, schedule_name);

CREATE UNIQUE INDEX idx_triggers_idempotency_key
    ON triggers(team, pipeline, idempotency_key) WHERE idempotency_key != '';

CREATE UNIQUE INDEX idx_triggers_webhook_delivery
    ON triggers(team, webhook_delivery) WHERE webhook_delivery != '';

CREATE UNIQUE INDEX idx_triggers_webhook_replay_key
    ON triggers(team, webhook_replay_key) WHERE webhook_replay_key != '';

CREATE UNIQUE INDEX idx_credit_grants_team_reference
    ON credit_grants(team, kind, reference) WHERE reference != '';

CREATE INDEX idx_github_app_installations_team ON github_app_installations(team);

CREATE INDEX idx_github_app_triggers_installation ON github_app_triggers(installation_id, repository_id);

CREATE INDEX idx_team_deletions_state ON team_deletions(state);

CREATE INDEX idx_invitation_email_log_address ON invitation_email_log(address, sent_at);

CREATE INDEX idx_triggers_team_created ON triggers(team, created_at);

CREATE INDEX idx_signup_admissions_at ON signup_admissions(at);

CREATE INDEX idx_accounts_created ON accounts(created_at);

CREATE INDEX idx_accounts_waitlisted ON accounts(waitlisted_at) WHERE waitlisted_at <> 0;

CREATE INDEX idx_credit_checkouts_team_expires ON credit_checkouts(team, expires_at);

CREATE INDEX idx_credit_checkouts_session ON credit_checkouts(session_id);

CREATE INDEX idx_credit_freezes_team ON credit_freezes(team, released_at);

CREATE INDEX idx_storage_reservations_team ON storage_reservations(team, store, expires_at);

CREATE INDEX idx_storage_reservations_expiry ON storage_reservations(expires_at);

CREATE UNIQUE INDEX idx_git_credentials_team_host ON git_credentials(team, host);

CREATE INDEX idx_git_credential_releases_team ON git_credential_releases(team, released_at);

CREATE INDEX idx_triggers_github_commit
    ON triggers(github_repo_id, git_sha);

CREATE INDEX idx_identity_unlinks_account ON identity_unlinks(account_id);

CREATE INDEX idx_uploads_expiry ON uploads(expires_at);

CREATE INDEX idx_cron_schedules_github_identity
    ON cron_schedules(team, github_installation_id, github_repository_id);

CREATE INDEX idx_storage_commit_receipts_age ON storage_commit_receipts(committed_at);

CREATE INDEX idx_credit_charges_team_amount ON credit_charges(team, amount_micro);

CREATE UNIQUE INDEX idx_business_events_subject ON business_events(team, kind, subject_id);

CREATE INDEX idx_business_events_team_ts ON business_events(team, ts);

CREATE INDEX idx_business_events_kind_ts ON business_events(kind, ts);

CREATE INDEX idx_triggers_team_pending
    ON triggers(team, status) WHERE status = 'pending';

CREATE UNIQUE INDEX idx_card_charges_one_open ON card_charges(team) WHERE state = 'open';

CREATE INDEX idx_card_charges_due ON card_charges(state, next_attempt_at);

CREATE UNIQUE INDEX idx_card_attempts_one_live ON card_attempts(charge_id) WHERE status = 'live';

CREATE INDEX idx_card_attempts_team ON card_attempts(team, status, updated_at);

CREATE INDEX idx_card_attempts_intent ON card_attempts(payment_intent);

CREATE INDEX idx_payment_warnings_intent ON payment_warnings(payment_intent);

CREATE INDEX idx_payment_warnings_card ON payment_warnings(fingerprint);

CREATE INDEX idx_github_app_deliveries_received
    ON github_app_deliveries(received_at);

CREATE INDEX idx_github_app_automation_installation ON github_app_automation(installation_id);

CREATE INDEX idx_node_logs_unsealed ON node_logs(updated_at) WHERE sealed_at IS NULL;

CREATE UNIQUE INDEX idx_tokens_agent_name ON tokens(team, principal) WHERE kind IN ('runner', 'team') AND revoked_at IS NULL AND substr(principal, 1, 6) = 'agent:';
