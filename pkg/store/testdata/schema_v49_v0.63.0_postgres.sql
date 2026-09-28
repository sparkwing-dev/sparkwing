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
    annotations_json bytea,
    parent_run_id text,
    annotation_count bigint NOT NULL DEFAULT 0,
    receipt_sha text NOT NULL DEFAULT ''::text,
    cost_cents bigint NOT NULL DEFAULT 0,
    cost_currency text NOT NULL DEFAULT 'USD'::text,
    invocation_json bytea,
    cost_settled bigint NOT NULL DEFAULT 0,
    top_annotation text NOT NULL DEFAULT ''::text,
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
    "full" bigint NOT NULL DEFAULT 0,
    parent_run_id text,
    retry_source text NOT NULL DEFAULT ''::text,
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
    wait_sample_count bigint NOT NULL DEFAULT 0,
    wait_samples_json bytea,
    wait_p50_ms bigint NOT NULL DEFAULT 0,
    wait_p99_ms bigint NOT NULL DEFAULT 0,
    contended_count bigint NOT NULL DEFAULT 0,
    plan_hash text NOT NULL DEFAULT ''::text,
    floor_cores real NOT NULL DEFAULT 0,
    floor_memory_bytes bigint NOT NULL DEFAULT 0,
    prev_peak_cores real NOT NULL DEFAULT 0,
    prev_peak_memory_bytes bigint NOT NULL DEFAULT 0,
    sustained_cores real NOT NULL DEFAULT 0,
    prev_sustained_cores real NOT NULL DEFAULT 0,
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
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (1, 1790569474732128591);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (2, 1790569474752586765);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (3, 1790569474772761040);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (4, 1790569474774370030);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (5, 1790569474796431894);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (6, 1790569474819206553);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (7, 1790569474821057441);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (8, 1790569474822452233);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (9, 1790569474824361921);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (10, 1790569474825100616);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (11, 1790569474827629601);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (12, 1790569474828310496);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (13, 1790569474829634388);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (14, 1790569474831292278);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (15, 1790569474833593264);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (16, 1790569474835644851);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (17, 1790569474836526545);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (18, 1790569474837699738);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (19, 1790569474838470533);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (20, 1790569474839107429);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (21, 1790569474840076824);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (22, 1790569474845600289);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (23, 1790569474847685676);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (24, 1790569474848892369);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (25, 1790569474850016762);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (26, 1790569474852353348);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (27, 1790569474853054543);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (28, 1790569474857037819);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (29, 1790569474857767314);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (30, 1790569474882420362);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (31, 1790569474892506499);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (32, 1790569474896929672);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (33, 1790569474904672724);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (34, 1790569474912249777);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (35, 1790569474917982641);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (36, 1790569474919029935);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (37, 1790569474920741024);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (38, 1790569474926555188);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (39, 1790569474928260278);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (40, 1790569474931958855);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (41, 1790569474933722644);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (42, 1790569474935976130);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (43, 1790569474936768125);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (44, 1790569474937115123);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (45, 1790569474938104917);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (46, 1790569474939809806);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (47, 1790569474941074499);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (48, 1790569474944596877);
INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (49, 1790569474945798469);
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('session-token-digest', 1790569474840382322, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('repo-scoped-secrets', 1790569474846006587, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('unique-token-prefix', 1790569474852731145, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('inherited-holder-marker', 1790569474853363241, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('executor-enrollment-v1', 1790569474882764759, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('executor-offer-arbitration-v1', 1790569474882764759, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('agent-loss-attempt-fencing-v1', 1790569474882764759, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('assisted-execution-policy-v1', 1790569474892865697, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('cron-schedule-names-v1', 1790569474905095421, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('pipeline-scoped-secrets', 1790569474944936475, 'v0.63.0');
INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES ('declared-run-repo', 1790569474944936475, 'v0.63.0');
INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('min_binary_version', 'v0.63.0', 1790569474946087368);
INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('controller_authority_id', 'swfa_e440c60379ae998f1f5daca04ea42f07', 1790569474959363885);
