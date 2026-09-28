CREATE TABLE sparkwing_schema_version (
    version integer NOT NULL,
    applied_at bigint NOT NULL,
    CONSTRAINT sparkwing_schema_version_pkey PRIMARY KEY (version)
);
CREATE TABLE sparkwing_requirements (
    name text NOT NULL,
    added_at bigint NOT NULL,
    added_by_version text NOT NULL,
    CONSTRAINT sparkwing_requirements_pkey PRIMARY KEY (name)
);
CREATE TABLE runs (
    id text NOT NULL,
    pipeline text NOT NULL,
    status text NOT NULL,
    trigger_source text NOT NULL DEFAULT ''::text,
    git_branch text NOT NULL DEFAULT ''::text,
    git_sha text NOT NULL DEFAULT ''::text,
    args_json bytea,
    plan_json bytea,
    error text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL DEFAULT 0,
    started_at bigint NOT NULL,
    finished_at bigint,
    declared_repo text NOT NULL DEFAULT ''::text,
    repo_url text NOT NULL DEFAULT ''::text,
    github_owner text NOT NULL DEFAULT ''::text,
    github_repo text NOT NULL DEFAULT ''::text,
    retry_of text NOT NULL DEFAULT ''::text,
    retried_as text NOT NULL DEFAULT ''::text,
    retry_source text NOT NULL DEFAULT ''::text,
    retry_cause_node_id text NOT NULL DEFAULT ''::text,
    retry_avoid_coordinator_id text NOT NULL DEFAULT ''::text,
    retry_avoid_executor_kind text NOT NULL DEFAULT ''::text,
    retry_avoid_executor_id text NOT NULL DEFAULT ''::text,
    retry_avoid_until bigint,
    replay_of_run_id text NOT NULL DEFAULT ''::text,
    replay_of_node_id text NOT NULL DEFAULT ''::text,
    last_heartbeat_at bigint,
    cost_cents bigint NOT NULL DEFAULT 0,
    cost_currency text NOT NULL DEFAULT 'USD'::text,
    annotation_count bigint NOT NULL DEFAULT 0,
    parent_run_id text,
    cost_settled bigint NOT NULL DEFAULT 0,
    annotations_json bytea,
    receipt_sha text NOT NULL DEFAULT ''::text,
    top_annotation text NOT NULL DEFAULT ''::text,
    invocation_json bytea,
    created_principal text NOT NULL DEFAULT ''::text,
    CONSTRAINT runs_pkey PRIMARY KEY (id)
);
CREATE TABLE nodes (
    run_id text NOT NULL,
    node_id text NOT NULL,
    status text NOT NULL,
    outcome text NOT NULL DEFAULT ''::text,
    deps_json bytea,
    started_at bigint,
    finished_at bigint,
    error text NOT NULL DEFAULT ''::text,
    output_json bytea,
    ready_at bigint,
    claimed_by text,
    claim_principal text NOT NULL DEFAULT ''::text,
    claim_token_prefix text NOT NULL DEFAULT ''::text,
    claim_executor text NOT NULL DEFAULT ''::text,
    claim_cores double precision NOT NULL DEFAULT 0,
    claim_memory_bytes bigint NOT NULL DEFAULT 0,
    claim_reservation text NOT NULL DEFAULT ''::text,
    claim_slot bigint NOT NULL DEFAULT '-1'::integer,
    lease_expires_at bigint,
    coordinator_id text NOT NULL DEFAULT ''::text,
    executor_kind text NOT NULL DEFAULT ''::text,
    executor_id text NOT NULL DEFAULT ''::text,
    execution_started_at bigint,
    reservation_id text NOT NULL DEFAULT ''::text,
    claim_generation bigint NOT NULL DEFAULT 0,
    claim_membership_id text NOT NULL DEFAULT ''::text,
    attempts_consumed bigint NOT NULL DEFAULT 0,
    retry_root_run_id text NOT NULL DEFAULT ''::text,
    executor_location text NOT NULL DEFAULT 'unknown'::text,
    required_coordinator_id text NOT NULL DEFAULT ''::text,
    required_executor_location text NOT NULL DEFAULT ''::text,
    execution_policy_json bytea,
    execution_policy_hash text NOT NULL DEFAULT ''::text,
    execution_policy_version bigint NOT NULL DEFAULT 0,
    execution_body_protocol bigint NOT NULL DEFAULT 0,
    execution_supervisor_requirements_json bytea,
    execution_supervisor_requirements_hash text NOT NULL DEFAULT ''::text,
    execution_body_requirements_json bytea,
    execution_body_requirements_hash text NOT NULL DEFAULT ''::text,
    avoid_coordinator_id text NOT NULL DEFAULT ''::text,
    avoid_executor_kind text NOT NULL DEFAULT ''::text,
    avoid_executor_id text NOT NULL DEFAULT ''::text,
    avoid_until bigint,
    needs_labels bytea,
    prefers_labels bytea,
    requested_cores double precision NOT NULL DEFAULT 0,
    requested_memory_bytes bigint NOT NULL DEFAULT 0,
    requested_slots bigint NOT NULL DEFAULT 1,
    offer_started_at bigint,
    offer_priority_target bigint NOT NULL DEFAULT 100,
    claim_base_priority bigint NOT NULL DEFAULT 0,
    claim_priority bigint NOT NULL DEFAULT 0,
    claim_worker_id text NOT NULL DEFAULT ''::text,
    claim_executor_kind text NOT NULL DEFAULT ''::text,
    claim_reservation_id text NOT NULL DEFAULT ''::text,
    placement_reason text NOT NULL DEFAULT ''::text,
    placement_hold_from bigint,
    status_detail text NOT NULL DEFAULT ''::text,
    last_heartbeat bigint,
    failure_reason text NOT NULL DEFAULT ''::text,
    exit_code bigint,
    artifact_manifest text NOT NULL DEFAULT ''::text,
    seq bigint NOT NULL DEFAULT 0,
    annotations_json bytea,
    summary text NOT NULL DEFAULT ''::text,
    cpu_nanos bigint NOT NULL DEFAULT 0,
    max_rss_bytes bigint NOT NULL DEFAULT 0,
    process_wall_nanos bigint NOT NULL DEFAULT 0,
    credit_charged_through bigint NOT NULL DEFAULT 0,
    credit_exhausted_anchor bigint NOT NULL DEFAULT 0,
    credit_cpu_class bigint NOT NULL DEFAULT 0,
    CONSTRAINT nodes_pkey PRIMARY KEY (run_id, node_id)
);
CREATE TABLE node_claim_offers (
    claim_token_prefix text NOT NULL DEFAULT ''::text,
    claim_principal text NOT NULL DEFAULT ''::text,
    holder_id text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL,
    executor_name text NOT NULL DEFAULT ''::text,
    membership_id text NOT NULL DEFAULT ''::text,
    worker_id text NOT NULL,
    executor_kind text NOT NULL DEFAULT ''::text,
    reservation_id text NOT NULL,
    resource_digest text NOT NULL DEFAULT ''::text,
    slot bigint NOT NULL DEFAULT '-1'::integer,
    base_priority bigint NOT NULL,
    effective_priority bigint NOT NULL,
    offered_at bigint NOT NULL,
    last_seen_at bigint NOT NULL,
    lease_ns bigint NOT NULL,
    execution_policy_hash text NOT NULL DEFAULT ''::text,
    execution_policy_version bigint NOT NULL DEFAULT 0,
    execution_body_protocol bigint NOT NULL DEFAULT 0,
    execution_supervisor_requirements_hash text NOT NULL DEFAULT ''::text,
    execution_body_requirements_hash text NOT NULL DEFAULT ''::text,
    CONSTRAINT node_claim_offers_pkey PRIMARY KEY (claim_token_prefix, claim_principal, holder_id)
);
CREATE TABLE executors (
    executor_id text NOT NULL,
    name text NOT NULL,
    token_prefix text NOT NULL,
    kind text NOT NULL,
    location text NOT NULL,
    capabilities_json bytea,
    base_priority bigint NOT NULL DEFAULT 0,
    priority_ceiling bigint NOT NULL DEFAULT 0,
    max_concurrent bigint NOT NULL,
    budget_cores double precision NOT NULL DEFAULT 0,
    budget_memory_bytes bigint NOT NULL DEFAULT 0,
    principal text NOT NULL,
    last_seen bigint NOT NULL,
    headroom_reported bigint NOT NULL DEFAULT 0,
    headroom_cores double precision NOT NULL DEFAULT 0,
    headroom_memory_bytes bigint NOT NULL DEFAULT 0,
    queue_depth bigint NOT NULL DEFAULT 0,
    supported_body_protocol_min bigint NOT NULL DEFAULT 0,
    supported_body_protocol_max bigint NOT NULL DEFAULT 0,
    supervisor_requirements_json bytea,
    body_runtime_requirements_json bytea,
    runner_build_identity_json bytea,
    CONSTRAINT executors_pkey PRIMARY KEY (name),
    CONSTRAINT executors_token_prefix_key UNIQUE (token_prefix)
);
CREATE TABLE agent_loss_retry_node_sources (
    retry_run_id text NOT NULL,
    source_run_id text NOT NULL,
    node_id text NOT NULL,
    deps_json bytea NOT NULL,
    needs_labels_json bytea,
    prefers_labels_json bytea,
    requested_cores double precision NOT NULL DEFAULT 0,
    requested_memory_bytes bigint NOT NULL DEFAULT 0,
    requested_slots bigint NOT NULL DEFAULT 1,
    attempts_consumed bigint NOT NULL DEFAULT 0,
    required_coordinator_id text NOT NULL DEFAULT ''::text,
    required_executor_location text NOT NULL DEFAULT ''::text,
    avoid_coordinator_id text NOT NULL DEFAULT ''::text,
    avoid_executor_kind text NOT NULL DEFAULT ''::text,
    avoid_executor_id text NOT NULL DEFAULT ''::text,
    avoid_until bigint,
    policy_json bytea,
    policy_hash text NOT NULL DEFAULT ''::text,
    policy_version bigint NOT NULL DEFAULT 0,
    body_protocol bigint NOT NULL DEFAULT 0,
    supervisor_requirements_json bytea,
    supervisor_requirements_hash text NOT NULL DEFAULT ''::text,
    body_requirements_json bytea,
    body_requirements_hash text NOT NULL DEFAULT ''::text,
    CONSTRAINT agent_loss_retry_node_sources_pkey PRIMARY KEY (retry_run_id, node_id)
);
CREATE TABLE agent_loss_retry_legacy_deny_all (
    retry_run_id text NOT NULL,
    source_run_id text NOT NULL,
    reason text NOT NULL,
    CONSTRAINT agent_loss_retry_legacy_deny_all_pkey PRIMARY KEY (retry_run_id)
);
CREATE TABLE events (
    run_id text NOT NULL,
    seq bigint NOT NULL,
    node_id text NOT NULL DEFAULT ''::text,
    kind text NOT NULL,
    ts bigint NOT NULL,
    payload bytea,
    CONSTRAINT events_pkey PRIMARY KEY (run_id, seq)
);
CREATE TABLE triggers (
    id text NOT NULL,
    pipeline text NOT NULL,
    args_json bytea,
    trigger_source text NOT NULL DEFAULT ''::text,
    trigger_user text NOT NULL DEFAULT ''::text,
    trigger_env bytea,
    git_branch text NOT NULL DEFAULT ''::text,
    git_sha text NOT NULL DEFAULT ''::text,
    status text NOT NULL DEFAULT 'pending'::text,
    created_at bigint NOT NULL,
    claimed_at bigint,
    lease_expires_at bigint,
    cancel_requested_at bigint,
    repo text NOT NULL DEFAULT ''::text,
    repo_url text NOT NULL DEFAULT ''::text,
    github_owner text NOT NULL DEFAULT ''::text,
    github_repo text NOT NULL DEFAULT ''::text,
    repo_inherited bigint NOT NULL DEFAULT 0,
    retry_of text NOT NULL DEFAULT ''::text,
    parent_node_id text NOT NULL DEFAULT ''::text,
    idempotency_key text NOT NULL DEFAULT ''::text,
    claim_seq bigint NOT NULL DEFAULT 0,
    claim_principal text NOT NULL DEFAULT ''::text,
    claim_token_prefix text NOT NULL DEFAULT ''::text,
    webhook_delivery text NOT NULL DEFAULT ''::text,
    webhook_replay_key text NOT NULL DEFAULT ''::text,
    available_at bigint NOT NULL DEFAULT 0,
    retry_source text NOT NULL DEFAULT ''::text,
    "full" bigint NOT NULL DEFAULT 0,
    parent_run_id text,
    CONSTRAINT triggers_pkey PRIMARY KEY (id)
);
CREATE TABLE concurrency_entries (
    key text NOT NULL,
    capacity bigint NOT NULL DEFAULT 1,
    previous_capacity bigint,
    last_write_run_id text NOT NULL DEFAULT ''::text,
    last_write_node_id text NOT NULL DEFAULT ''::text,
    updated_at bigint NOT NULL,
    CONSTRAINT concurrency_entries_pkey PRIMARY KEY (key)
);
CREATE TABLE concurrency_holders (
    key text NOT NULL,
    holder_id text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL DEFAULT ''::text,
    claimed_at bigint NOT NULL,
    queue_arrived_at bigint NOT NULL DEFAULT 0,
    lease_expires_at bigint NOT NULL,
    superseded bigint NOT NULL DEFAULT 0,
    cost bigint NOT NULL DEFAULT 1,
    declared_capacity bigint NOT NULL DEFAULT 0,
    CONSTRAINT concurrency_holders_pkey PRIMARY KEY (key, holder_id)
);
CREATE TABLE concurrency_waiters (
    key text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL DEFAULT ''::text,
    holder_id text NOT NULL DEFAULT ''::text,
    arrived_at bigint NOT NULL,
    policy text NOT NULL,
    cache_key_hash text NOT NULL DEFAULT ''::text,
    leader_run_id text NOT NULL DEFAULT ''::text,
    leader_node_id text NOT NULL DEFAULT ''::text,
    cancel_timeout_ns bigint NOT NULL DEFAULT 0,
    cost bigint NOT NULL DEFAULT 1,
    declared_capacity bigint NOT NULL DEFAULT 0,
    CONSTRAINT concurrency_waiters_pkey PRIMARY KEY (key, run_id, node_id)
);
CREATE TABLE concurrency_cache (
    key text NOT NULL,
    cache_key_hash text NOT NULL,
    output_ref text NOT NULL,
    origin_run_id text NOT NULL,
    origin_node_id text NOT NULL,
    created_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    last_hit_at bigint NOT NULL,
    CONSTRAINT concurrency_cache_pkey PRIMARY KEY (key, cache_key_hash)
);
CREATE TABLE node_steps (
    run_id text NOT NULL,
    node_id text NOT NULL,
    step_id text NOT NULL,
    status text NOT NULL,
    started_at bigint,
    finished_at bigint,
    annotations_json bytea,
    summary text NOT NULL DEFAULT ''::text,
    CONSTRAINT node_steps_pkey PRIMARY KEY (run_id, node_id, step_id)
);
CREATE TABLE node_metrics (
    run_id text NOT NULL,
    node_id text NOT NULL,
    ts bigint NOT NULL,
    cpu_millicores bigint NOT NULL,
    memory_bytes bigint NOT NULL,
    cpu_time_nanos bigint NOT NULL DEFAULT 0,
    CONSTRAINT node_metrics_pkey PRIMARY KEY (run_id, node_id, ts)
);
CREATE TABLE tokens (
    hash text NOT NULL,
    prefix text NOT NULL,
    principal text NOT NULL,
    kind text NOT NULL,
    scopes text NOT NULL,
    created_at bigint NOT NULL,
    expires_at bigint,
    last_used_at bigint,
    revoked_at bigint,
    replaced_by text,
    metered bigint NOT NULL DEFAULT 0,
    CONSTRAINT tokens_pkey PRIMARY KEY (hash)
);
CREATE TABLE sessions (
    hash text NOT NULL,
    principal text NOT NULL,
    scopes text NOT NULL,
    created_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    last_used_at bigint,
    CONSTRAINT sessions_pkey PRIMARY KEY (hash)
);
CREATE TABLE users (
    name text NOT NULL,
    pw_hash text NOT NULL,
    created_at bigint NOT NULL,
    last_login_at bigint,
    scopes text NOT NULL DEFAULT 'admin'::text,
    CONSTRAINT users_pkey PRIMARY KEY (name)
);
CREATE TABLE secrets (
    name text NOT NULL,
    value text NOT NULL,
    principal text NOT NULL,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    masked bigint NOT NULL DEFAULT 1,
    pipeline text NOT NULL DEFAULT ''::text,
    shared bigint NOT NULL DEFAULT 0,
    CONSTRAINT secrets_pkey PRIMARY KEY (name, pipeline)
);
CREATE TABLE debug_pauses (
    run_id text NOT NULL,
    node_id text NOT NULL,
    reason text NOT NULL,
    paused_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    released_at bigint,
    released_by text NOT NULL DEFAULT ''::text,
    release_kind text NOT NULL DEFAULT ''::text,
    CONSTRAINT debug_pauses_pkey PRIMARY KEY (run_id, node_id, reason)
);
CREATE TABLE approvals (
    run_id text NOT NULL,
    node_id text NOT NULL,
    requested_at bigint NOT NULL,
    message text NOT NULL DEFAULT ''::text,
    timeout_ms bigint NOT NULL DEFAULT 0,
    on_timeout text NOT NULL DEFAULT 'fail'::text,
    approver text NOT NULL DEFAULT ''::text,
    resolved_at bigint,
    resolution text NOT NULL DEFAULT ''::text,
    comment text NOT NULL DEFAULT ''::text,
    CONSTRAINT approvals_pkey PRIMARY KEY (run_id, node_id)
);
CREATE TABLE node_dispatches (
    run_id text NOT NULL,
    node_id text NOT NULL,
    seq bigint NOT NULL,
    dispatched_at bigint NOT NULL,
    code_version text NOT NULL DEFAULT ''::text,
    binary_hash text NOT NULL DEFAULT ''::text,
    runner_labels bytea,
    env_json bytea,
    workdir text NOT NULL DEFAULT ''::text,
    input_envelope_json bytea,
    input_size_bytes bigint NOT NULL DEFAULT 0,
    secret_redactions bigint NOT NULL DEFAULT 0,
    redacted_keys bytea,
    CONSTRAINT node_dispatches_pkey PRIMARY KEY (run_id, node_id, seq)
);
CREATE TABLE sparkwing_meta (
    key text NOT NULL,
    value text NOT NULL,
    updated_at bigint NOT NULL,
    CONSTRAINT sparkwing_meta_pkey PRIMARY KEY (key)
);
CREATE TABLE pipeline_profiles (
    pipeline text NOT NULL,
    node_id text NOT NULL,
    p50_duration_ms bigint NOT NULL,
    p99_duration_ms bigint NOT NULL,
    peak_cores real NOT NULL,
    peak_memory_bytes bigint NOT NULL,
    sample_count bigint NOT NULL,
    updated_at bigint NOT NULL,
    samples_json bytea,
    pinned_cores real NOT NULL DEFAULT 0,
    pinned_memory_bytes bigint NOT NULL DEFAULT 0,
    cpu_measured bigint NOT NULL DEFAULT 0,
    wait_samples_json bytea,
    wait_p50_ms bigint NOT NULL DEFAULT 0,
    wait_p99_ms bigint NOT NULL DEFAULT 0,
    wait_sample_count bigint NOT NULL DEFAULT 0,
    contended_count bigint NOT NULL DEFAULT 0,
    plan_hash text NOT NULL DEFAULT ''::text,
    floor_cores real NOT NULL DEFAULT 0,
    floor_memory_bytes bigint NOT NULL DEFAULT 0,
    prev_peak_cores real NOT NULL DEFAULT 0,
    prev_peak_memory_bytes bigint NOT NULL DEFAULT 0,
    prev_sustained_cores real NOT NULL DEFAULT 0,
    sustained_cores real NOT NULL DEFAULT 0,
    CONSTRAINT pipeline_profiles_pkey PRIMARY KEY (pipeline, node_id)
);
CREATE TABLE node_bounces (
    run_id text NOT NULL,
    node_id text NOT NULL,
    seq bigint NOT NULL,
    requested_at bigint NOT NULL,
    requested_by text NOT NULL DEFAULT ''::text,
    consumed_at bigint,
    outcome text NOT NULL DEFAULT ''::text,
    CONSTRAINT node_bounces_pkey PRIMARY KEY (run_id, node_id, seq)
);
CREATE TABLE agent_loss_retries (
    run_id text NOT NULL,
    source_run_id text NOT NULL,
    root_run_id text NOT NULL,
    cause_nodes_json bytea NOT NULL,
    available_at bigint NOT NULL,
    deadline_at bigint NOT NULL,
    retry_count bigint NOT NULL,
    CONSTRAINT agent_loss_retries_pkey PRIMARY KEY (run_id)
);
CREATE TABLE node_execution_attempts (
    lineage_root_run_id text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL,
    attempt_ordinal bigint NOT NULL,
    claim_generation bigint NOT NULL,
    coordinator_id text NOT NULL,
    membership_id text NOT NULL,
    executor_kind text NOT NULL,
    executor_name text NOT NULL DEFAULT ''::text,
    executor_id text NOT NULL,
    executor_location text NOT NULL,
    holder_id text NOT NULL,
    reservation_id text NOT NULL,
    started_at bigint NOT NULL,
    finished_at bigint,
    outcome text NOT NULL DEFAULT ''::text,
    failure_reason text NOT NULL DEFAULT ''::text,
    retry_run_id text NOT NULL DEFAULT ''::text,
    CONSTRAINT node_execution_attempts_pkey PRIMARY KEY (lineage_root_run_id, node_id, attempt_ordinal),
    CONSTRAINT node_execution_attempts_run_id_node_id_claim_generation_att_key UNIQUE (run_id, node_id, claim_generation, attempt_ordinal)
);
CREATE TABLE run_definition_plans (
    run_id text NOT NULL,
    plan_hash text NOT NULL,
    CONSTRAINT run_definition_plans_pkey PRIMARY KEY (run_id)
);
CREATE TABLE cron_schedules (
    id text NOT NULL,
    repo_path text NOT NULL,
    pipeline text NOT NULL,
    schedule_name text NOT NULL DEFAULT 'default'::text,
    cron text NOT NULL,
    tz text NOT NULL,
    overlap text NOT NULL,
    catch_up_ns bigint NOT NULL,
    where_ text NOT NULL DEFAULT 'local'::text,
    args text NOT NULL DEFAULT '{}'::text,
    locked_ref text NOT NULL DEFAULT ''::text,
    locked_binary text NOT NULL DEFAULT ''::text,
    locked_digest text NOT NULL DEFAULT ''::text,
    paused bigint NOT NULL DEFAULT 0,
    declared bigint NOT NULL DEFAULT 1,
    armed_at bigint NOT NULL,
    armed_by text NOT NULL DEFAULT ''::text,
    updated_at bigint NOT NULL,
    cursor_at bigint NOT NULL,
    last_fired_at bigint,
    last_run_id text NOT NULL DEFAULT ''::text,
    last_outcome text NOT NULL DEFAULT ''::text,
    next_due_at bigint,
    override_cron text,
    override_tz text,
    override_overlap text,
    override_catch_up_ns bigint,
    override_args text,
    override_base text,
    override_set_at bigint,
    git_branch text NOT NULL DEFAULT ''::text,
    CONSTRAINT cron_schedules_pkey PRIMARY KEY (id)
);
CREATE TABLE cron_fires (
    id text NOT NULL,
    schedule_id text NOT NULL,
    due_at bigint NOT NULL,
    decided_at bigint NOT NULL,
    outcome text NOT NULL,
    run_id text NOT NULL DEFAULT ''::text,
    detail text NOT NULL DEFAULT ''::text,
    args text NOT NULL DEFAULT '{}'::text,
    CONSTRAINT cron_fires_pkey PRIMARY KEY (id)
);
CREATE TABLE credit_grants (
    id text NOT NULL,
    kind text NOT NULL,
    amount_micro bigint NOT NULL,
    reference text NOT NULL DEFAULT ''::text,
    created_by text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    reverses text NOT NULL DEFAULT ''::text,
    CONSTRAINT credit_grants_pkey PRIMARY KEY (id)
);
CREATE TABLE credit_charges (
    id text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL,
    token_prefix text NOT NULL,
    kind text NOT NULL DEFAULT 'usage'::text,
    seconds bigint NOT NULL,
    amount_micro bigint NOT NULL,
    principal text NOT NULL DEFAULT ''::text,
    storage_bytes bigint NOT NULL DEFAULT 0,
    cpu_class bigint NOT NULL DEFAULT 0,
    rate_micro_per_second bigint NOT NULL DEFAULT 0,
    charged_at bigint NOT NULL,
    CONSTRAINT credit_charges_pkey PRIMARY KEY (id)
);
CREATE TABLE github_webhook_bindings (
    pipeline text NOT NULL,
    repo text NOT NULL,
    secret text NOT NULL,
    events text NOT NULL DEFAULT ''::text,
    hook_id bigint NOT NULL DEFAULT 0,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    CONSTRAINT github_webhook_bindings_pkey PRIMARY KEY (pipeline, repo)
);
CREATE TABLE storage_quotas (
    principal text NOT NULL,
    tier text NOT NULL DEFAULT ''::text,
    max_bytes_per_run bigint NOT NULL DEFAULT 0,
    max_bytes_per_month bigint NOT NULL DEFAULT 0,
    max_objects_per_run bigint NOT NULL DEFAULT 0,
    storage_allowance_bytes bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    CONSTRAINT storage_quotas_pkey PRIMARY KEY (principal)
);
CREATE TABLE storage_run_usage (
    principal text NOT NULL,
    run_id text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    objects bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    CONSTRAINT storage_run_usage_pkey PRIMARY KEY (principal, run_id)
);
CREATE TABLE storage_month_usage (
    principal text NOT NULL,
    month text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    objects bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    CONSTRAINT storage_month_usage_pkey PRIMARY KEY (principal, month)
);
CREATE TABLE egress_usage (
    principal text NOT NULL,
    month text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    CONSTRAINT egress_usage_pkey PRIMARY KEY (principal, month)
);
ALTER TABLE nodes ADD CONSTRAINT nodes_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE node_claim_offers ADD CONSTRAINT node_claim_offers_run_id_node_id_fkey FOREIGN KEY (run_id, node_id) REFERENCES nodes(run_id, node_id) ON DELETE CASCADE;
ALTER TABLE agent_loss_retry_node_sources ADD CONSTRAINT agent_loss_retry_node_sources_retry_run_id_fkey FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE agent_loss_retry_legacy_deny_all ADD CONSTRAINT agent_loss_retry_legacy_deny_all_retry_run_id_fkey FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE events ADD CONSTRAINT events_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE node_steps ADD CONSTRAINT node_steps_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE node_metrics ADD CONSTRAINT node_metrics_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE debug_pauses ADD CONSTRAINT debug_pauses_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE approvals ADD CONSTRAINT approvals_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE node_dispatches ADD CONSTRAINT node_dispatches_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE node_bounces ADD CONSTRAINT node_bounces_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE agent_loss_retries ADD CONSTRAINT agent_loss_retries_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE node_execution_attempts ADD CONSTRAINT node_execution_attempts_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE run_definition_plans ADD CONSTRAINT run_definition_plans_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE storage_run_usage ADD CONSTRAINT storage_run_usage_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
CREATE INDEX idx_agent_loss_retries_source ON agent_loss_retries USING btree (source_run_id);
CREATE INDEX idx_approvals_pending ON approvals USING btree (requested_at) WHERE (resolved_at IS NULL);
CREATE INDEX idx_concurrency_cache_expires ON concurrency_cache USING btree (expires_at);
CREATE INDEX idx_concurrency_cache_lru ON concurrency_cache USING btree (last_hit_at);
CREATE INDEX idx_concurrency_cache_origin_run ON concurrency_cache USING btree (origin_run_id);
CREATE INDEX idx_concurrency_holders_key_claimed ON concurrency_holders USING btree (key, claimed_at);
CREATE INDEX idx_concurrency_holders_lease ON concurrency_holders USING btree (lease_expires_at);
CREATE INDEX idx_concurrency_waiters_arrived ON concurrency_waiters USING btree (key, arrived_at);
CREATE INDEX idx_credit_charges_charged ON credit_charges USING btree (charged_at);
CREATE INDEX idx_credit_charges_kind_amount ON credit_charges USING btree (kind, amount_micro, seconds);
CREATE INDEX idx_credit_charges_node ON credit_charges USING btree (run_id, node_id);
CREATE INDEX idx_credit_grants_created ON credit_grants USING btree (created_at);
CREATE INDEX idx_credit_grants_kind_amount ON credit_grants USING btree (kind, amount_micro);
CREATE INDEX idx_credit_grants_kind_created ON credit_grants USING btree (kind, created_at);
CREATE INDEX idx_cron_fires_schedule_decided ON cron_fires USING btree (schedule_id, decided_at DESC);
CREATE INDEX idx_debug_pauses_open ON debug_pauses USING btree (run_id) WHERE (released_at IS NULL);
CREATE INDEX idx_egress_usage_month ON egress_usage USING btree (month);
CREATE INDEX idx_events_run_ts ON events USING btree (run_id, ts);
CREATE INDEX idx_events_ts ON events USING btree (ts);
CREATE INDEX idx_github_webhook_bindings_repo ON github_webhook_bindings USING btree (repo);
CREATE INDEX idx_node_bounces_pending ON node_bounces USING btree (run_id, node_id) WHERE (consumed_at IS NULL);
CREATE INDEX idx_node_claim_offers_award ON node_claim_offers USING btree (run_id, node_id, effective_priority DESC, offered_at, executor_name, slot, holder_id);
CREATE INDEX idx_node_dispatches_lookup ON node_dispatches USING btree (run_id, node_id, seq DESC);
CREATE INDEX idx_node_execution_attempts_run ON node_execution_attempts USING btree (run_id, node_id, attempt_ordinal);
CREATE INDEX idx_node_metrics_lookup ON node_metrics USING btree (run_id, node_id, ts);
CREATE INDEX idx_node_metrics_ts ON node_metrics USING btree (ts);
CREATE INDEX idx_node_steps_lookup ON node_steps USING btree (run_id, node_id);
CREATE INDEX idx_nodes_assisted_claimable ON nodes USING btree (execution_policy_version, ready_at, run_id, node_id) WHERE ((ready_at IS NOT NULL) AND (claimed_by IS NULL) AND (outcome = ''::text) AND (finished_at IS NULL) AND (execution_policy_hash <> ''::text));
CREATE INDEX idx_nodes_claimable ON nodes USING btree (ready_at) WHERE ((ready_at IS NOT NULL) AND (claimed_by IS NULL) AND (status <> 'done'::text));
CREATE INDEX idx_nodes_claimed_lease ON nodes USING btree (lease_expires_at) WHERE (claimed_by IS NOT NULL);
CREATE INDEX idx_nodes_credit_active ON nodes USING btree (credit_charged_through);
CREATE INDEX idx_nodes_credit_principal ON nodes USING btree (claim_principal, credit_charged_through);
CREATE INDEX idx_nodes_credit_window ON nodes USING btree (credit_charged_through) WHERE (credit_charged_through <> 0);
CREATE INDEX idx_nodes_outstanding ON nodes USING btree (status, ready_at, claimed_by) WHERE (status <> 'done'::text);
CREATE INDEX idx_runs_branch_started ON runs USING btree (git_branch, started_at DESC);
CREATE INDEX idx_runs_created ON runs USING btree (created_at);
CREATE INDEX idx_runs_pipeline ON runs USING btree (pipeline, started_at DESC);
CREATE INDEX idx_runs_principal_created ON runs USING btree (created_principal, created_at);
CREATE INDEX idx_runs_repo_branch_started ON runs USING btree (declared_repo, git_branch, started_at DESC);
CREATE INDEX idx_runs_repo_sha_started ON runs USING btree (declared_repo, git_sha, started_at DESC);
CREATE INDEX idx_runs_repo_slug_started ON runs USING btree (declared_repo, started_at DESC);
CREATE INDEX idx_runs_sha_started ON runs USING btree (git_sha, started_at DESC);
CREATE INDEX idx_runs_started ON runs USING btree (started_at DESC);
CREATE INDEX idx_sessions_expires ON sessions USING btree (expires_at);
CREATE INDEX idx_triggers_claimed_lease ON triggers USING btree (status, lease_expires_at) WHERE (status = 'claimed'::text);
CREATE INDEX idx_triggers_pending ON triggers USING btree (status, available_at, created_at) WHERE (status = 'pending'::text);
CREATE INDEX idx_triggers_source_status_created ON triggers USING btree (trigger_source, status, created_at);
CREATE UNIQUE INDEX idx_credit_grants_reference ON credit_grants USING btree (kind, reference) WHERE (reference <> ''::text);
CREATE UNIQUE INDEX idx_cron_schedules_repo_pipeline_name ON cron_schedules USING btree (repo_path, pipeline, schedule_name);
CREATE UNIQUE INDEX idx_executors_executor_id ON executors USING btree (executor_id);
CREATE UNIQUE INDEX idx_node_claim_offers_executor_node ON node_claim_offers USING btree (executor_name, run_id, node_id);
CREATE UNIQUE INDEX idx_node_claim_offers_executor_slot ON node_claim_offers USING btree (executor_name, slot);
CREATE UNIQUE INDEX idx_node_claim_offers_reservation ON node_claim_offers USING btree (reservation_id);
CREATE UNIQUE INDEX idx_tokens_prefix ON tokens USING btree (prefix);
CREATE UNIQUE INDEX idx_triggers_idempotency_key ON triggers USING btree (pipeline, idempotency_key) WHERE (idempotency_key <> ''::text);
CREATE UNIQUE INDEX idx_triggers_webhook_delivery ON triggers USING btree (webhook_delivery) WHERE (webhook_delivery <> ''::text);
CREATE UNIQUE INDEX idx_triggers_webhook_replay_key ON triggers USING btree (webhook_replay_key) WHERE (webhook_replay_key <> ''::text);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (1, 1790572235963054770);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (2, 1790572236026735149);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (3, 1790572236077823512);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (4, 1790572236080022697);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (5, 1790572236134837235);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (6, 1790572236173793877);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (7, 1790572236177365354);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (8, 1790572236178873244);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (9, 1790572236182128822);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (10, 1790572236193174349);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (11, 1790572236197844319);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (12, 1790572236198850512);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (13, 1790572236200659400);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (14, 1790572236207528655);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (15, 1790572236212116424);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (16, 1790572236215373803);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (17, 1790572236216560395);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (18, 1790572236217897486);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (19, 1790572236218648481);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (20, 1790572236219568775);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (21, 1790572236220784367);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (22, 1790572236229779208);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (23, 1790572236232146592);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (24, 1790572236233428583);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (25, 1790572236234765075);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (26, 1790572236239293145);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (27, 1790572236241111933);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (28, 1790572236247606690);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (29, 1790572236248359785);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (30, 1790572236311818566);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (31, 1790572236343682355);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (32, 1790572236350912007);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (33, 1790572236363000527);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (34, 1790572236378776723);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (35, 1790572236387605065);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (36, 1790572236388690458);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (37, 1790572236391321540);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (38, 1790572236399377187);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (39, 1790572236403479260);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (40, 1790572236411522707);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (41, 1790572236425549314);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (42, 1790572236439468022);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (43, 1790572236441599108);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (44, 1790572236442094305);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (45, 1790572236443646895);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (46, 1790572236445709781);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (47, 1790572236447466969);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (48, 1790572236453326931);
INSERT INTO sparkwing_schema_version ("version", "applied_at") VALUES (49, 1790572236458794894);
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('session-token-digest', 1790572236221096965, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('repo-scoped-secrets', 1790572236230522503, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('unique-token-prefix', 1790572236240047140, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('inherited-holder-marker', 1790572236241658929, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('executor-enrollment-v1', 1790572236312316862, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('executor-offer-arbitration-v1', 1790572236312316862, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('agent-loss-attempt-fencing-v1', 1790572236312316862, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('assisted-execution-policy-v1', 1790572236344357151, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('cron-schedule-names-v1', 1790572236363449524, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('pipeline-scoped-secrets', 1790572236453939527, 'v0.63.0');
INSERT INTO sparkwing_requirements ("name", "added_at", "added_by_version") VALUES ('declared-run-repo', 1790572236453939527, 'v0.63.0');
INSERT INTO runs ("id", "pipeline", "status", "trigger_source", "git_branch", "git_sha", "args_json", "plan_json", "error", "created_at", "started_at", "finished_at", "declared_repo", "repo_url", "github_owner", "github_repo", "retry_of", "retried_as", "retry_source", "retry_cause_node_id", "retry_avoid_coordinator_id", "retry_avoid_executor_kind", "retry_avoid_executor_id", "retry_avoid_until", "replay_of_run_id", "replay_of_node_id", "last_heartbeat_at", "cost_cents", "cost_currency", "annotation_count", "parent_run_id", "cost_settled", "annotations_json", "receipt_sha", "top_annotation", "invocation_json", "created_principal") VALUES ('run-a', 'build', 'running', '', '', '', '\x6e756c6c'::bytea, NULL, '', 1790000000000000000, 1790000000000000000, NULL, '', '', '', '', '', '', '', '', '', '', '', NULL, '', '', 1790572236481896842, 0, 'USD', 0, NULL, 0, NULL, '', '', NULL, '');
INSERT INTO runs ("id", "pipeline", "status", "trigger_source", "git_branch", "git_sha", "args_json", "plan_json", "error", "created_at", "started_at", "finished_at", "declared_repo", "repo_url", "github_owner", "github_repo", "retry_of", "retried_as", "retry_source", "retry_cause_node_id", "retry_avoid_coordinator_id", "retry_avoid_executor_kind", "retry_avoid_executor_id", "retry_avoid_until", "replay_of_run_id", "replay_of_node_id", "last_heartbeat_at", "cost_cents", "cost_currency", "annotation_count", "parent_run_id", "cost_settled", "annotations_json", "receipt_sha", "top_annotation", "invocation_json", "created_principal") VALUES ('run-b', 'build', 'running', '', '', '', '\x6e756c6c'::bytea, NULL, '', 1790000000000000000, 1790000000000000000, NULL, '', '', '', '', '', '', '', '', '', '', '', NULL, '', '', 1790572236487650904, 0, 'USD', 0, NULL, 0, NULL, '', '', NULL, '');
INSERT INTO nodes ("run_id", "node_id", "status", "outcome", "deps_json", "started_at", "finished_at", "error", "output_json", "ready_at", "claimed_by", "claim_principal", "claim_token_prefix", "claim_executor", "claim_cores", "claim_memory_bytes", "claim_reservation", "claim_slot", "lease_expires_at", "coordinator_id", "executor_kind", "executor_id", "execution_started_at", "reservation_id", "claim_generation", "claim_membership_id", "attempts_consumed", "retry_root_run_id", "executor_location", "required_coordinator_id", "required_executor_location", "execution_policy_json", "execution_policy_hash", "execution_policy_version", "execution_body_protocol", "execution_supervisor_requirements_json", "execution_supervisor_requirements_hash", "execution_body_requirements_json", "execution_body_requirements_hash", "avoid_coordinator_id", "avoid_executor_kind", "avoid_executor_id", "avoid_until", "needs_labels", "prefers_labels", "requested_cores", "requested_memory_bytes", "requested_slots", "offer_started_at", "offer_priority_target", "claim_base_priority", "claim_priority", "claim_worker_id", "claim_executor_kind", "claim_reservation_id", "placement_reason", "placement_hold_from", "status_detail", "last_heartbeat", "failure_reason", "exit_code", "artifact_manifest", "seq", "annotations_json", "summary", "cpu_nanos", "max_rss_bytes", "process_wall_nanos", "credit_charged_through", "credit_exhausted_anchor", "credit_cpu_class") VALUES ('run-a', 'test', 'pending', '', '\x6e756c6c'::bytea, NULL, NULL, '', NULL, NULL, NULL, '', '', '', 0, 0, '', -1, NULL, '', '', '', NULL, '', 0, '', 0, 'run-a', 'unknown', '', '', NULL, '', 0, 0, NULL, '', NULL, '', '', '', '', NULL, NULL, NULL, 0, 0, 1, NULL, 100, 0, 0, '', '', '', '', NULL, '', NULL, '', NULL, '', 2, NULL, '', 0, 0, 0, 0, 0, 0);
INSERT INTO nodes ("run_id", "node_id", "status", "outcome", "deps_json", "started_at", "finished_at", "error", "output_json", "ready_at", "claimed_by", "claim_principal", "claim_token_prefix", "claim_executor", "claim_cores", "claim_memory_bytes", "claim_reservation", "claim_slot", "lease_expires_at", "coordinator_id", "executor_kind", "executor_id", "execution_started_at", "reservation_id", "claim_generation", "claim_membership_id", "attempts_consumed", "retry_root_run_id", "executor_location", "required_coordinator_id", "required_executor_location", "execution_policy_json", "execution_policy_hash", "execution_policy_version", "execution_body_protocol", "execution_supervisor_requirements_json", "execution_supervisor_requirements_hash", "execution_body_requirements_json", "execution_body_requirements_hash", "avoid_coordinator_id", "avoid_executor_kind", "avoid_executor_id", "avoid_until", "needs_labels", "prefers_labels", "requested_cores", "requested_memory_bytes", "requested_slots", "offer_started_at", "offer_priority_target", "claim_base_priority", "claim_priority", "claim_worker_id", "claim_executor_kind", "claim_reservation_id", "placement_reason", "placement_hold_from", "status_detail", "last_heartbeat", "failure_reason", "exit_code", "artifact_manifest", "seq", "annotations_json", "summary", "cpu_nanos", "max_rss_bytes", "process_wall_nanos", "credit_charged_through", "credit_exhausted_anchor", "credit_cpu_class") VALUES ('run-a', 'compile', 'pending', '', '\x6e756c6c'::bytea, NULL, NULL, '', NULL, NULL, NULL, '', 'swt_seed', '', 0, 0, '', -1, NULL, '', '', '', NULL, '', 0, '', 0, 'run-a', 'unknown', '', '', NULL, '', 0, 0, NULL, '', NULL, '', '', '', '', NULL, NULL, NULL, 0, 0, 1, NULL, 100, 0, 0, '', '', '', '', NULL, '', NULL, '', NULL, '', 1, NULL, '', 0, 0, 0, 0, 0, 0);
INSERT INTO events ("run_id", "seq", "node_id", "kind", "ts", "payload") VALUES ('run-a', 1, 'compile', 'log', 1790572236512619739, '\x6c696e65'::bytea);
INSERT INTO events ("run_id", "seq", "node_id", "kind", "ts", "payload") VALUES ('run-a', 2, 'compile', 'log', 1790572236526656346, '\x6c696e65'::bytea);
INSERT INTO events ("run_id", "seq", "node_id", "kind", "ts", "payload") VALUES ('run-a', 3, 'compile', 'log', 1790572236537610074, '\x6c696e65'::bytea);
INSERT INTO triggers ("id", "pipeline", "args_json", "trigger_source", "trigger_user", "trigger_env", "git_branch", "git_sha", "status", "created_at", "claimed_at", "lease_expires_at", "cancel_requested_at", "repo", "repo_url", "github_owner", "github_repo", "repo_inherited", "retry_of", "parent_node_id", "idempotency_key", "claim_seq", "claim_principal", "claim_token_prefix", "webhook_delivery", "webhook_replay_key", "available_at", "retry_source", "full", "parent_run_id") VALUES ('trig-1', 'build', '\x6e756c6c'::bytea, '', '', '\x6e756c6c'::bytea, '', '', 'pending', 1790000000000000000, NULL, NULL, NULL, '', '', '', '', 0, '', '', '', 0, '', '', '', '', 0, '', 0, NULL);
INSERT INTO concurrency_entries ("key", "capacity", "previous_capacity", "last_write_run_id", "last_write_node_id", "updated_at") VALUES ('deploy', 1, NULL, 'run-a', 'compile', 1790572236553889166);
INSERT INTO concurrency_holders ("key", "holder_id", "run_id", "node_id", "claimed_at", "queue_arrived_at", "lease_expires_at", "superseded", "cost", "declared_capacity") VALUES ('deploy', 'h1', 'run-a', 'compile', 1790572236553889166, 0, 1790575836553889166, 0, 1, 1);
INSERT INTO secrets ("name", "value", "principal", "created_at", "updated_at", "masked", "pipeline", "shared") VALUES ('TOKEN', 'v1', '', 1790000000, 1790000000, 0, '', 0);
INSERT INTO secrets ("name", "value", "principal", "created_at", "updated_at", "masked", "pipeline", "shared") VALUES ('TOKEN', 'v2', '', 1790000000, 1790000000, 0, 'build', 0);
INSERT INTO sparkwing_meta ("key", "value", "updated_at") VALUES ('min_binary_version', 'v0.63.0', 1790572236459724788);
INSERT INTO sparkwing_meta ("key", "value", "updated_at") VALUES ('controller_authority_id', 'swfa_a89010c7cf2bd3c9f42a771427b4747c', 1790572236478085667);
INSERT INTO sparkwing_meta ("key", "value", "updated_at") VALUES ('credit_exhausted_at', '1790000000000000000', 1790000000000000000);
INSERT INTO pipeline_profiles ("pipeline", "node_id", "p50_duration_ms", "p99_duration_ms", "peak_cores", "peak_memory_bytes", "sample_count", "updated_at", "samples_json", "pinned_cores", "pinned_memory_bytes", "cpu_measured", "wait_samples_json", "wait_p50_ms", "wait_p99_ms", "wait_sample_count", "contended_count", "plan_hash", "floor_cores", "floor_memory_bytes", "prev_peak_cores", "prev_peak_memory_bytes", "prev_sustained_cores", "sustained_cores") VALUES ('build', 'compile', 60000, 60000, 2, 1073741824, 1, 1790572236593753003, '\x7b22736368656d61223a342c2273616d706c6573223a5b7b2264223a36303030303030303030302c2263223a322c226d223a313037333734313832342c2273223a317d5d7d'::bytea, 0, 0, 0, NULL, 0, 0, 0, 0, '', 0, 0, 0, 0, 0, 1);
