CREATE TABLE sparkwing_schema_version (
    version    INTEGER NOT NULL,
    applied_at BIGINT NOT NULL,
    PRIMARY KEY (version)
);
CREATE TABLE sparkwing_requirements (
    name             TEXT NOT NULL,
    added_at         BIGINT NOT NULL,
    added_by_version TEXT NOT NULL,
    PRIMARY KEY (name)
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
, "cost_currency" TEXT NOT NULL DEFAULT 'USD', "annotation_count" INTEGER NOT NULL DEFAULT 0, "parent_run_id" TEXT, "cost_settled" INTEGER NOT NULL DEFAULT 0, "annotations_json" BLOB, "receipt_sha" TEXT NOT NULL DEFAULT '', "top_annotation" TEXT NOT NULL DEFAULT '', "invocation_json" BLOB, "cost_cents" INTEGER NOT NULL DEFAULT 0, "created_principal" TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_runs_started ON runs(started_at DESC);
CREATE INDEX idx_runs_pipeline ON runs(pipeline, started_at DESC);
CREATE INDEX idx_runs_sha_started ON runs(git_sha, started_at DESC);
CREATE INDEX idx_runs_branch_started ON runs(git_branch, started_at DESC);
CREATE INDEX idx_runs_repo_slug_started ON runs(declared_repo, started_at DESC);
CREATE INDEX idx_runs_repo_sha_started ON runs(declared_repo, git_sha, started_at DESC);
CREATE INDEX idx_runs_repo_branch_started ON runs(declared_repo, git_branch, started_at DESC);
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
    seq              INTEGER NOT NULL DEFAULT 0, "annotations_json" BLOB, "summary" TEXT NOT NULL DEFAULT '', "process_wall_nanos" INTEGER NOT NULL DEFAULT 0, "cpu_nanos" INTEGER NOT NULL DEFAULT 0, "max_rss_bytes" INTEGER NOT NULL DEFAULT 0, "credit_charged_through" INTEGER NOT NULL DEFAULT 0, "credit_exhausted_anchor" INTEGER NOT NULL DEFAULT 0, "credit_cpu_class" INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_nodes_claimable
    ON nodes(ready_at)
    WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND status != 'done';
CREATE INDEX idx_nodes_claimed_lease
    ON nodes(lease_expires_at)
    WHERE claimed_by IS NOT NULL;
CREATE TABLE node_claim_offers (
    claim_token_prefix TEXT NOT NULL DEFAULT '',
    claim_principal    TEXT NOT NULL DEFAULT '',
    holder_id          TEXT NOT NULL,
    run_id             TEXT NOT NULL,
    node_id            TEXT NOT NULL,
    executor_name      TEXT NOT NULL DEFAULT '',
    membership_id      TEXT NOT NULL DEFAULT '',
    worker_id          TEXT NOT NULL,
    executor_kind      TEXT NOT NULL DEFAULT '',
    reservation_id     TEXT NOT NULL,
    resource_digest    TEXT NOT NULL DEFAULT '',
    slot                INTEGER NOT NULL DEFAULT -1,
    base_priority      INTEGER NOT NULL,
    effective_priority INTEGER NOT NULL,
    offered_at         INTEGER NOT NULL,
    last_seen_at       INTEGER NOT NULL,
    lease_ns           INTEGER NOT NULL,
    execution_policy_hash TEXT NOT NULL DEFAULT '',
    execution_policy_version INTEGER NOT NULL DEFAULT 0,
    execution_body_protocol INTEGER NOT NULL DEFAULT 0,
    execution_supervisor_requirements_hash TEXT NOT NULL DEFAULT '',
    execution_body_requirements_hash TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (claim_token_prefix, claim_principal, holder_id),
    FOREIGN KEY (run_id, node_id) REFERENCES nodes(run_id, node_id) ON DELETE CASCADE
);
CREATE INDEX idx_node_claim_offers_award
    ON node_claim_offers(run_id, node_id, effective_priority DESC, offered_at, executor_name, slot, holder_id);
CREATE UNIQUE INDEX idx_node_claim_offers_reservation
    ON node_claim_offers(reservation_id);
CREATE UNIQUE INDEX idx_node_claim_offers_executor_slot
    ON node_claim_offers(executor_name, slot);
CREATE UNIQUE INDEX idx_node_claim_offers_executor_node
    ON node_claim_offers(executor_name, run_id, node_id);
CREATE TABLE executors (
    executor_id           TEXT NOT NULL,
    name                  TEXT PRIMARY KEY,
    token_prefix          TEXT NOT NULL UNIQUE,
    kind                  TEXT NOT NULL,
    location              TEXT NOT NULL,
    capabilities_json     BLOB,
    base_priority         INTEGER NOT NULL DEFAULT 0,
    priority_ceiling      INTEGER NOT NULL DEFAULT 0,
    max_concurrent        INTEGER NOT NULL,
    budget_cores          DOUBLE PRECISION NOT NULL DEFAULT 0,
    budget_memory_bytes   INTEGER NOT NULL DEFAULT 0,
    principal             TEXT NOT NULL,
    last_seen             INTEGER NOT NULL,
    headroom_reported     INTEGER NOT NULL DEFAULT 0,
    headroom_cores        DOUBLE PRECISION NOT NULL DEFAULT 0,
    headroom_memory_bytes INTEGER NOT NULL DEFAULT 0,
    queue_depth           INTEGER NOT NULL DEFAULT 0,
    supported_body_protocol_min INTEGER NOT NULL DEFAULT 0,
    supported_body_protocol_max INTEGER NOT NULL DEFAULT 0,
    supervisor_requirements_json BLOB,
    body_runtime_requirements_json BLOB,
    runner_build_identity_json BLOB
);
CREATE UNIQUE INDEX idx_executors_executor_id ON executors(executor_id);
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
    body_requirements_hash TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (retry_run_id, node_id),
    FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE TABLE agent_loss_retry_legacy_deny_all (
    retry_run_id  TEXT PRIMARY KEY,
    source_run_id TEXT NOT NULL,
    reason        TEXT NOT NULL,
    FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE TABLE events (
    run_id   TEXT NOT NULL,
    seq      INTEGER NOT NULL,
    node_id  TEXT NOT NULL DEFAULT '',
    kind     TEXT NOT NULL,
    ts       INTEGER NOT NULL,
    payload  BLOB,
    PRIMARY KEY (run_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_events_run_ts ON events(run_id, ts);
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
, "retry_source" TEXT NOT NULL DEFAULT '', "full" INTEGER NOT NULL DEFAULT 0, "parent_run_id" TEXT);
CREATE INDEX idx_triggers_claimed_lease
    ON triggers(status, lease_expires_at) WHERE status = 'claimed';
CREATE INDEX idx_triggers_source_status_created
    ON triggers(trigger_source, status, created_at);
CREATE UNIQUE INDEX idx_triggers_idempotency_key
    ON triggers(pipeline, idempotency_key) WHERE idempotency_key != '';
CREATE UNIQUE INDEX idx_triggers_webhook_delivery
    ON triggers(webhook_delivery) WHERE webhook_delivery != '';
CREATE UNIQUE INDEX idx_triggers_webhook_replay_key
    ON triggers(webhook_replay_key) WHERE webhook_replay_key != '';
CREATE TABLE concurrency_entries (
    key                 TEXT PRIMARY KEY,
    capacity            INTEGER NOT NULL DEFAULT 1,
    previous_capacity   INTEGER,
    last_write_run_id   TEXT NOT NULL DEFAULT '',
    last_write_node_id  TEXT NOT NULL DEFAULT '',
    updated_at          INTEGER NOT NULL
);
CREATE TABLE concurrency_holders (
    key               TEXT NOT NULL,
    holder_id         TEXT NOT NULL,
    run_id            TEXT NOT NULL,
    node_id           TEXT NOT NULL DEFAULT '',
    claimed_at        INTEGER NOT NULL,
    queue_arrived_at  INTEGER NOT NULL DEFAULT 0,
    lease_expires_at  INTEGER NOT NULL,
    superseded        INTEGER NOT NULL DEFAULT 0,
    cost              INTEGER NOT NULL DEFAULT 1,
    declared_capacity INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key, holder_id)
);
CREATE INDEX idx_concurrency_holders_key_claimed
    ON concurrency_holders(key, claimed_at);
CREATE INDEX idx_concurrency_holders_lease
    ON concurrency_holders(lease_expires_at);
CREATE TABLE concurrency_waiters (
    key                TEXT NOT NULL,
    run_id             TEXT NOT NULL,
    node_id            TEXT NOT NULL DEFAULT '',
    holder_id          TEXT NOT NULL DEFAULT '',
    arrived_at         INTEGER NOT NULL,
    policy             TEXT NOT NULL,
    cache_key_hash     TEXT NOT NULL DEFAULT '',
    leader_run_id      TEXT NOT NULL DEFAULT '',
    leader_node_id     TEXT NOT NULL DEFAULT '',
    cancel_timeout_ns  INTEGER NOT NULL DEFAULT 0,
    cost               INTEGER NOT NULL DEFAULT 1,
    declared_capacity  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key, run_id, node_id)
);
CREATE INDEX idx_concurrency_waiters_arrived
    ON concurrency_waiters(key, arrived_at);
CREATE TABLE concurrency_cache (
    key             TEXT NOT NULL,
    cache_key_hash  TEXT NOT NULL,
    output_ref      TEXT NOT NULL,
    origin_run_id   TEXT NOT NULL,
    origin_node_id  TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    last_hit_at     INTEGER NOT NULL,
    PRIMARY KEY (key, cache_key_hash)
);
CREATE INDEX idx_concurrency_cache_expires
    ON concurrency_cache(expires_at);
CREATE INDEX idx_concurrency_cache_lru
    ON concurrency_cache(last_hit_at);
CREATE INDEX idx_concurrency_cache_origin_run
    ON concurrency_cache(origin_run_id);
CREATE TABLE node_steps (
    run_id      TEXT NOT NULL,
    node_id     TEXT NOT NULL,
    step_id     TEXT NOT NULL,
    status      TEXT NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER, "annotations_json" BLOB, "summary" TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id, step_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_node_steps_lookup
    ON node_steps(run_id, node_id);
CREATE TABLE node_metrics (
    run_id          TEXT NOT NULL,
    node_id         TEXT NOT NULL,
    ts              INTEGER NOT NULL,
    cpu_millicores  INTEGER NOT NULL,
    memory_bytes    INTEGER NOT NULL, "cpu_time_nanos" INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, node_id, ts),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_node_metrics_lookup
    ON node_metrics(run_id, node_id, ts);
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
, "metered" INTEGER NOT NULL DEFAULT 0);
CREATE TABLE sessions (
    hash          TEXT PRIMARY KEY,
    principal     TEXT NOT NULL,
    scopes        TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_used_at  INTEGER
);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE TABLE users (
    name          TEXT PRIMARY KEY,
    pw_hash       TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    scopes        TEXT NOT NULL DEFAULT 'admin'
);
CREATE TABLE debug_pauses (
    run_id       TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    reason       TEXT NOT NULL,
    paused_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    released_at  INTEGER,
    released_by  TEXT NOT NULL DEFAULT '',
    release_kind TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id, reason),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_debug_pauses_open
    ON debug_pauses(run_id) WHERE released_at IS NULL;
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
    comment      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_approvals_pending
    ON approvals(requested_at) WHERE resolved_at IS NULL;
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
    redacted_keys       BLOB,
    PRIMARY KEY (run_id, node_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_node_dispatches_lookup
    ON node_dispatches(run_id, node_id, seq DESC);
CREATE TABLE sparkwing_meta (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE pipeline_profiles (
    pipeline            TEXT    NOT NULL,
    node_id             TEXT    NOT NULL,
    p50_duration_ms     INTEGER NOT NULL,
    p99_duration_ms     INTEGER NOT NULL,
    peak_cores          REAL    NOT NULL,
    peak_memory_bytes   INTEGER NOT NULL,
    sample_count        INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    samples_json        BLOB,
    pinned_cores        REAL    NOT NULL DEFAULT 0,
    pinned_memory_bytes INTEGER NOT NULL DEFAULT 0,
    cpu_measured        INTEGER NOT NULL DEFAULT 0, "wait_samples_json" BLOB, "wait_p50_ms" INTEGER NOT NULL DEFAULT 0, "wait_p99_ms" INTEGER NOT NULL DEFAULT 0, "wait_sample_count" INTEGER NOT NULL DEFAULT 0, "contended_count" INTEGER NOT NULL DEFAULT 0, "floor_cores" REAL NOT NULL DEFAULT 0, "floor_memory_bytes" INTEGER NOT NULL DEFAULT 0, "prev_peak_cores" REAL NOT NULL DEFAULT 0, "prev_peak_memory_bytes" INTEGER NOT NULL DEFAULT 0, "plan_hash" TEXT NOT NULL DEFAULT '', "sustained_cores" REAL NOT NULL DEFAULT 0, "prev_sustained_cores" REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (pipeline, node_id)
);
CREATE TABLE node_bounces (
    run_id       TEXT    NOT NULL,
    node_id      TEXT    NOT NULL,
    seq          INTEGER NOT NULL,
    requested_at INTEGER NOT NULL,
    requested_by TEXT    NOT NULL DEFAULT '',
    consumed_at  INTEGER,
    outcome      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, node_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_node_bounces_pending
    ON node_bounces(run_id, node_id) WHERE consumed_at IS NULL;
CREATE TABLE "secrets" (
    name       TEXT NOT NULL,
    value      TEXT NOT NULL,
    principal  TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    masked     INTEGER NOT NULL DEFAULT 1,
    pipeline       TEXT NOT NULL DEFAULT '', "shared" INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (name, pipeline)
);
CREATE UNIQUE INDEX idx_tokens_prefix ON tokens(prefix);
CREATE INDEX idx_triggers_pending
    ON triggers(status, available_at, created_at) WHERE status = 'pending';
CREATE TABLE agent_loss_retries (
    run_id           TEXT PRIMARY KEY,
    source_run_id    TEXT NOT NULL,
    root_run_id      TEXT NOT NULL,
    cause_nodes_json BLOB NOT NULL,
    available_at     INTEGER NOT NULL,
    deadline_at      INTEGER NOT NULL,
    retry_count      INTEGER NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_agent_loss_retries_source
    ON agent_loss_retries(source_run_id);
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
    retry_run_id        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (lineage_root_run_id, node_id, attempt_ordinal),
    UNIQUE (run_id, node_id, claim_generation, attempt_ordinal),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_node_execution_attempts_run
    ON node_execution_attempts(run_id, node_id, attempt_ordinal);
CREATE TABLE run_definition_plans (
    run_id    TEXT PRIMARY KEY,
    plan_hash TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX idx_nodes_assisted_claimable
    ON nodes(execution_policy_version, ready_at, run_id, node_id)
    WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND outcome = '' AND finished_at IS NULL AND execution_policy_hash != '';
CREATE TABLE cron_schedules (
    id            TEXT PRIMARY KEY,
    repo_path     TEXT NOT NULL,
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
, "git_branch" TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX idx_cron_schedules_repo_pipeline_name
    ON cron_schedules(repo_path, pipeline, schedule_name);
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
);
CREATE INDEX idx_cron_fires_schedule_decided
    ON cron_fires(schedule_id, decided_at DESC);
CREATE TABLE credit_grants (
    id           TEXT PRIMARY KEY,
    -- free | paid | reversal; a reversal carries a negative amount
    kind         TEXT NOT NULL,
    amount_micro INTEGER NOT NULL,
    -- payment id or operator note; empty for an unreferenced grant
    reference    TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
, "reverses" TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_credit_grants_created ON credit_grants(created_at);
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
);
CREATE INDEX idx_credit_charges_charged ON credit_charges(charged_at);
CREATE INDEX idx_credit_charges_node ON credit_charges(run_id, node_id);
CREATE TABLE github_webhook_bindings (
    pipeline   TEXT NOT NULL,
    -- lowercase owner/name, so a lookup never depends on the caller's fold
    repo       TEXT NOT NULL,
    -- sealed by the controller's secrets cipher when one is configured
    secret     TEXT NOT NULL,
    -- comma-separated GitHub event names the hook subscribes to
    events     TEXT NOT NULL DEFAULT '',
    hook_id    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (pipeline, repo)
);
CREATE INDEX idx_github_webhook_bindings_repo
    ON github_webhook_bindings(repo);
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
);
CREATE TABLE storage_run_usage (
    principal  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    objects    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (principal, run_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE TABLE storage_month_usage (
    principal  TEXT NOT NULL,
    month      TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    objects    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (principal, month)
);
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
CREATE TABLE egress_usage (
    -- the principal the bytes were served to, or "anonymous"
    principal  TEXT NOT NULL,
    -- the UTC month the bytes fell in, as "2006-01"
    month      TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (principal, month)
);
CREATE INDEX idx_egress_usage_month ON egress_usage(month);
CREATE UNIQUE INDEX idx_credit_grants_reference
    ON credit_grants(kind, reference) WHERE reference != '';
CREATE INDEX idx_credit_grants_kind_created
    ON credit_grants(kind, created_at);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (1, 1790572235213872515);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (2, 1790572235214905608);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (3, 1790572235215464904);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (4, 1790572235215709602);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (5, 1790572235216290599);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (6, 1790572235216832195);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (7, 1790572235217185893);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (8, 1790572235217858388);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (9, 1790572235222243259);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (10, 1790572235223871549);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (11, 1790572235228484818);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (12, 1790572235231106101);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (13, 1790572235231492698);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (14, 1790572235234117381);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (15, 1790572235242068129);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (16, 1790572235242893723);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (17, 1790572235243112722);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (18, 1790572235243203621);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (19, 1790572235243295720);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (20, 1790572235243401220);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (21, 1790572235243489319);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (22, 1790572235247585392);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (23, 1790572235249525579);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (24, 1790572235249792878);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (25, 1790572235249964676);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (26, 1790572235251070169);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (27, 1790572235251475266);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (28, 1790572235251686165);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (29, 1790572235252036863);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (30, 1790572235255387841);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (31, 1790572235257459827);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (32, 1790572235258349521);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (33, 1790572235259466214);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (34, 1790572235260825305);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (35, 1790572235265769972);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (36, 1790572235268191156);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (37, 1790572235268748052);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (38, 1790572235269537947);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (39, 1790572235271237036);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (40, 1790572235273378922);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (41, 1790572235273808919);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (42, 1790572235275240610);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (43, 1790572235275461408);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (44, 1790572235275532908);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (45, 1790572235276839799);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (46, 1790572235278174890);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (47, 1790572235278337989);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (48, 1790572235284416849);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (49, 1790572235284918646);
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('session-token-digest', 1790572235243507619, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('repo-scoped-secrets', 1790572235247663892, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('unique-token-prefix', 1790572235251105769, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('inherited-holder-marker', 1790572235251501766, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('executor-enrollment-v1', 1790572235255459540, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('executor-offer-arbitration-v1', 1790572235255459540, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('agent-loss-attempt-fencing-v1', 1790572235255459540, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('assisted-execution-policy-v1', 1790572235257496527, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('cron-schedule-names-v1', 1790572235259497513, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('pipeline-scoped-secrets', 1790572235284486349, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('declared-run-repo', 1790572235284486349, 'v0.63.0');
INSERT INTO runs ("id", "pipeline", "status", "trigger_source", "git_branch", "git_sha", "args_json", "plan_json", "error", "created_at", "started_at", "finished_at", "declared_repo", "repo_url", "github_owner", "github_repo", "retry_of", "retried_as", "retry_source", "retry_cause_node_id", "retry_avoid_coordinator_id", "retry_avoid_executor_kind", "retry_avoid_executor_id", "retry_avoid_until", "replay_of_run_id", "replay_of_node_id", "last_heartbeat_at", "cost_currency", "annotation_count", "parent_run_id", "cost_settled", "annotations_json", "receipt_sha", "top_annotation", "invocation_json", "cost_cents", "created_principal") VALUES ('run-a', 'build', 'running', '', '', '', X'6e756c6c', NULL, '', 1790000000000000000, 1790000000000000000, NULL, '', '', '', '', '', '', '', '', '', '', '', NULL, '', '', 1790572235285311643, 'USD', 0, NULL, 0, NULL, '', '', NULL, 0, '');
INSERT INTO runs ("id", "pipeline", "status", "trigger_source", "git_branch", "git_sha", "args_json", "plan_json", "error", "created_at", "started_at", "finished_at", "declared_repo", "repo_url", "github_owner", "github_repo", "retry_of", "retried_as", "retry_source", "retry_cause_node_id", "retry_avoid_coordinator_id", "retry_avoid_executor_kind", "retry_avoid_executor_id", "retry_avoid_until", "replay_of_run_id", "replay_of_node_id", "last_heartbeat_at", "cost_currency", "annotation_count", "parent_run_id", "cost_settled", "annotations_json", "receipt_sha", "top_annotation", "invocation_json", "cost_cents", "created_principal") VALUES ('run-b', 'build', 'running', '', '', '', X'6e756c6c', NULL, '', 1790000000000000000, 1790000000000000000, NULL, '', '', '', '', '', '', '', '', '', '', '', NULL, '', '', 1790572235285937739, 'USD', 0, NULL, 0, NULL, '', '', NULL, 0, '');
INSERT INTO nodes ("run_id", "node_id", "status", "outcome", "deps_json", "started_at", "finished_at", "error", "output_json", "ready_at", "claimed_by", "claim_principal", "claim_token_prefix", "claim_executor", "claim_cores", "claim_memory_bytes", "claim_reservation", "claim_slot", "lease_expires_at", "coordinator_id", "executor_kind", "executor_id", "execution_started_at", "reservation_id", "claim_generation", "claim_membership_id", "attempts_consumed", "retry_root_run_id", "executor_location", "required_coordinator_id", "required_executor_location", "execution_policy_json", "execution_policy_hash", "execution_policy_version", "execution_body_protocol", "execution_supervisor_requirements_json", "execution_supervisor_requirements_hash", "execution_body_requirements_json", "execution_body_requirements_hash", "avoid_coordinator_id", "avoid_executor_kind", "avoid_executor_id", "avoid_until", "needs_labels", "prefers_labels", "requested_cores", "requested_memory_bytes", "requested_slots", "offer_started_at", "offer_priority_target", "claim_base_priority", "claim_priority", "claim_worker_id", "claim_executor_kind", "claim_reservation_id", "placement_reason", "placement_hold_from", "status_detail", "last_heartbeat", "failure_reason", "exit_code", "artifact_manifest", "seq", "annotations_json", "summary", "process_wall_nanos", "cpu_nanos", "max_rss_bytes", "credit_charged_through", "credit_exhausted_anchor", "credit_cpu_class") VALUES ('run-a', 'compile', 'pending', '', X'6e756c6c', NULL, NULL, '', NULL, NULL, NULL, '', 'swt_seed', '', 0, 0, '', -1, NULL, '', '', '', NULL, '', 0, '', 0, 'run-a', 'unknown', '', '', NULL, '', 0, 0, NULL, '', NULL, '', '', '', '', NULL, NULL, NULL, 0, 0, 1, NULL, 100, 0, 0, '', '', '', '', NULL, '', NULL, '', NULL, '', 1, NULL, '', 0, 0, 0, 0, 0, 0);
INSERT INTO nodes ("run_id", "node_id", "status", "outcome", "deps_json", "started_at", "finished_at", "error", "output_json", "ready_at", "claimed_by", "claim_principal", "claim_token_prefix", "claim_executor", "claim_cores", "claim_memory_bytes", "claim_reservation", "claim_slot", "lease_expires_at", "coordinator_id", "executor_kind", "executor_id", "execution_started_at", "reservation_id", "claim_generation", "claim_membership_id", "attempts_consumed", "retry_root_run_id", "executor_location", "required_coordinator_id", "required_executor_location", "execution_policy_json", "execution_policy_hash", "execution_policy_version", "execution_body_protocol", "execution_supervisor_requirements_json", "execution_supervisor_requirements_hash", "execution_body_requirements_json", "execution_body_requirements_hash", "avoid_coordinator_id", "avoid_executor_kind", "avoid_executor_id", "avoid_until", "needs_labels", "prefers_labels", "requested_cores", "requested_memory_bytes", "requested_slots", "offer_started_at", "offer_priority_target", "claim_base_priority", "claim_priority", "claim_worker_id", "claim_executor_kind", "claim_reservation_id", "placement_reason", "placement_hold_from", "status_detail", "last_heartbeat", "failure_reason", "exit_code", "artifact_manifest", "seq", "annotations_json", "summary", "process_wall_nanos", "cpu_nanos", "max_rss_bytes", "credit_charged_through", "credit_exhausted_anchor", "credit_cpu_class") VALUES ('run-a', 'test', 'pending', '', X'6e756c6c', NULL, NULL, '', NULL, NULL, NULL, '', '', '', 0, 0, '', -1, NULL, '', '', '', NULL, '', 0, '', 0, 'run-a', 'unknown', '', '', NULL, '', 0, 0, NULL, '', NULL, '', '', '', '', NULL, NULL, NULL, 0, 0, 1, NULL, 100, 0, 0, '', '', '', '', NULL, '', NULL, '', NULL, '', 2, NULL, '', 0, 0, 0, 0, 0, 0);
INSERT INTO events ("run_id", "seq", "node_id", "kind", "ts", "payload") VALUES ('run-a', 1, 'compile', 'log', 1790572235289216617, X'6c696e65');
INSERT INTO events ("run_id", "seq", "node_id", "kind", "ts", "payload") VALUES ('run-a', 2, 'compile', 'log', 1790572235290190411, X'6c696e65');
INSERT INTO events ("run_id", "seq", "node_id", "kind", "ts", "payload") VALUES ('run-a', 3, 'compile', 'log', 1790572235290448309, X'6c696e65');
INSERT INTO triggers ("id", "pipeline", "args_json", "trigger_source", "trigger_user", "trigger_env", "git_branch", "git_sha", "status", "created_at", "claimed_at", "lease_expires_at", "cancel_requested_at", "repo", "repo_url", "github_owner", "github_repo", "repo_inherited", "retry_of", "parent_node_id", "idempotency_key", "claim_seq", "claim_principal", "claim_token_prefix", "webhook_delivery", "webhook_replay_key", "available_at", "retry_source", "full", "parent_run_id") VALUES ('trig-1', 'build', X'6e756c6c', '', '', X'6e756c6c', '', '', 'pending', 1790000000000000000, NULL, NULL, NULL, '', '', '', '', 0, '', '', '', 0, '', '', '', '', 0, '', 0, NULL);
INSERT INTO concurrency_entries ("key", "capacity", "previous_capacity", "last_write_run_id", "last_write_node_id", "updated_at") VALUES ('deploy', 1, NULL, 'run-a', 'compile', 1790572235290740907);
INSERT INTO concurrency_holders ("key", "holder_id", "run_id", "node_id", "claimed_at", "queue_arrived_at", "lease_expires_at", "superseded", "cost", "declared_capacity") VALUES ('deploy', 'h1', 'run-a', 'compile', 1790572235290740907, 0, 1790575835290740907, 0, 1, 1);
INSERT INTO sparkwing_meta ("key", "value", "updated_at") VALUES ('min_binary_version', 'v0.63.0', 1790572235284962645);
INSERT INTO sparkwing_meta ("key", "value", "updated_at") VALUES ('controller_authority_id', 'swfa_ce21eab2455c35471a6d66253c013451', 1790572235285109844);
INSERT INTO sparkwing_meta ("key", "value", "updated_at") VALUES ('credit_exhausted_at', '1790000000000000000', 1790000000000000000);
INSERT INTO pipeline_profiles ("pipeline", "node_id", "p50_duration_ms", "p99_duration_ms", "peak_cores", "peak_memory_bytes", "sample_count", "updated_at", "samples_json", "pinned_cores", "pinned_memory_bytes", "cpu_measured", "wait_samples_json", "wait_p50_ms", "wait_p99_ms", "wait_sample_count", "contended_count", "floor_cores", "floor_memory_bytes", "prev_peak_cores", "prev_peak_memory_bytes", "plan_hash", "sustained_cores", "prev_sustained_cores") VALUES ('build', 'compile', 60000, 60000, 2, 1073741824, 1, 1790572235291843800, X'7b22736368656d61223a342c2273616d706c6573223a5b7b2264223a36303030303030303030302c2263223a322c226d223a313037333734313832342c2273223a317d5d7d', 0, 0, 0, NULL, 0, 0, 0, 0, 0, 0, 0, 0, '', 1, 0);
INSERT INTO secrets ("name", "value", "principal", "created_at", "updated_at", "masked", "pipeline", "shared") VALUES ('TOKEN', 'v1', '', 1790000000, 1790000000, 0, '', 0);
INSERT INTO secrets ("name", "value", "principal", "created_at", "updated_at", "masked", "pipeline", "shared") VALUES ('TOKEN', 'v2', '', 1790000000, 1790000000, 0, 'build', 0);
