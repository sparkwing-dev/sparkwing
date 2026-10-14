-- Baseline store schema (version 1) for Postgres. Keep it in step with schema_sqlite.sql.

CREATE TABLE sparkwing_schema_version (
    version integer NOT NULL,
    applied_at bigint NOT NULL,
    CONSTRAINT sparkwing_schema_version_pkey PRIMARY KEY (version)
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
    parent_run_id text,
    cost_cents bigint NOT NULL DEFAULT 0,
    cost_currency text NOT NULL DEFAULT 'USD'::text,
    cost_settled bigint NOT NULL DEFAULT 0,
    invocation_json bytea,
    receipt_sha text NOT NULL DEFAULT ''::text,
    annotations_json bytea,
    annotation_count bigint NOT NULL DEFAULT 0,
    top_annotation text NOT NULL DEFAULT ''::text,
    created_principal text NOT NULL DEFAULT ''::text,
    team text NOT NULL DEFAULT 'default'::text,
    event_bytes bigint NOT NULL DEFAULT 0,
    event_count bigint NOT NULL DEFAULT 0,
    plan_accepted_generation bigint NOT NULL DEFAULT 0,
    admission text NOT NULL DEFAULT ''::text,
    cancel_requested_at bigint,
    claim_wait_ns bigint NOT NULL DEFAULT 0,
    dispatch text NOT NULL DEFAULT ''::text,
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
    team text NOT NULL DEFAULT 'default'::text,
    credit_billing_from bigint NOT NULL DEFAULT 0,
    retry_budget bigint NOT NULL DEFAULT 0,
    retry_backoff_ms bigint NOT NULL DEFAULT 0,
    failure_json bytea,
    kind text NOT NULL DEFAULT ''::text,
    spec_hash text NOT NULL DEFAULT ''::text,
    on_failure_of text NOT NULL DEFAULT ''::text,
    continue_on_error bigint NOT NULL DEFAULT 0,
    optional bigint NOT NULL DEFAULT 0,
    cancel_requested_at bigint,
    approval_json bytea,
    attention_reason text NOT NULL DEFAULT ''::text,
    timeout_ms bigint NOT NULL DEFAULT 0,
    generated_json bytea,
    CONSTRAINT nodes_pkey PRIMARY KEY (run_id, node_id),
    CONSTRAINT nodes_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT agent_loss_retry_node_sources_pkey PRIMARY KEY (retry_run_id, node_id),
    CONSTRAINT agent_loss_retry_node_sources_retry_run_id_fkey FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE agent_loss_retry_legacy_deny_all (
    retry_run_id text NOT NULL,
    source_run_id text NOT NULL,
    reason text NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT agent_loss_retry_legacy_deny_all_pkey PRIMARY KEY (retry_run_id),
    CONSTRAINT agent_loss_retry_legacy_deny_all_retry_run_id_fkey FOREIGN KEY (retry_run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE events (
    run_id text NOT NULL,
    seq bigint NOT NULL,
    node_id text NOT NULL DEFAULT ''::text,
    kind text NOT NULL,
    ts bigint NOT NULL,
    payload bytea,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT events_pkey PRIMARY KEY (run_id, seq),
    CONSTRAINT events_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
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
    parent_run_id text,
    retry_source text NOT NULL DEFAULT ''::text,
    "full" bigint NOT NULL DEFAULT 0,
    team text NOT NULL DEFAULT 'default'::text,
    credit_reserved_at bigint NOT NULL DEFAULT 0,
    github_check_run_id bigint NOT NULL DEFAULT 0,
    github_repo_id bigint NOT NULL DEFAULT 0,
    credit_paid_seconds bigint NOT NULL DEFAULT 0,
    credit_paid_amount_micro bigint NOT NULL DEFAULT 0,
    credit_reservation_id text NOT NULL DEFAULT ''::text,
    runner_pin text NOT NULL DEFAULT ''::text,
    CONSTRAINT triggers_pkey PRIMARY KEY (id)
);

CREATE TABLE concurrency_entries (
    key text NOT NULL,
    capacity bigint NOT NULL DEFAULT 1,
    previous_capacity bigint,
    last_write_run_id text NOT NULL DEFAULT ''::text,
    last_write_node_id text NOT NULL DEFAULT ''::text,
    updated_at bigint NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT concurrency_entries_pkey PRIMARY KEY (team, key)
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT concurrency_holders_pkey PRIMARY KEY (team, key, holder_id)
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT concurrency_waiters_pkey PRIMARY KEY (team, key, run_id, node_id)
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT concurrency_cache_pkey PRIMARY KEY (team, key, cache_key_hash)
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT node_steps_pkey PRIMARY KEY (run_id, node_id, step_id),
    CONSTRAINT node_steps_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE node_metrics (
    run_id text NOT NULL,
    node_id text NOT NULL,
    ts bigint NOT NULL,
    cpu_millicores bigint NOT NULL,
    memory_bytes bigint NOT NULL,
    cpu_time_nanos bigint NOT NULL DEFAULT 0,
    team text NOT NULL DEFAULT 'default'::text,
    kind text NOT NULL DEFAULT ''::text,
    CONSTRAINT node_metrics_pkey PRIMARY KEY (run_id, node_id, ts),
    CONSTRAINT node_metrics_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
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
    team text NOT NULL DEFAULT 'default'::text,
    created_by text NOT NULL DEFAULT ''::text,
    advertised_labels_json bytea,
    CONSTRAINT tokens_pkey PRIMARY KEY (hash)
);

CREATE TABLE sessions (
    hash text NOT NULL,
    principal text NOT NULL,
    scopes text NOT NULL,
    created_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    last_used_at bigint,
    team text NOT NULL DEFAULT 'default'::text,
    account_id text NOT NULL DEFAULT ''::text,
    token_prefix text NOT NULL DEFAULT ''::text,
    CONSTRAINT sessions_pkey PRIMARY KEY (hash)
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT secrets_pkey PRIMARY KEY (team, name, pipeline)
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT debug_pauses_pkey PRIMARY KEY (run_id, node_id, reason),
    CONSTRAINT debug_pauses_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT approvals_pkey PRIMARY KEY (run_id, node_id),
    CONSTRAINT approvals_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT node_dispatches_pkey PRIMARY KEY (run_id, node_id, seq),
    CONSTRAINT node_dispatches_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT pipeline_profiles_pkey PRIMARY KEY (team, pipeline, node_id)
);

CREATE TABLE node_bounces (
    run_id text NOT NULL,
    node_id text NOT NULL,
    seq bigint NOT NULL,
    requested_at bigint NOT NULL,
    requested_by text NOT NULL DEFAULT ''::text,
    consumed_at bigint,
    outcome text NOT NULL DEFAULT ''::text,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT node_bounces_pkey PRIMARY KEY (run_id, node_id, seq),
    CONSTRAINT node_bounces_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE agent_loss_retries (
    run_id text NOT NULL,
    source_run_id text NOT NULL,
    root_run_id text NOT NULL,
    cause_nodes_json bytea NOT NULL,
    available_at bigint NOT NULL,
    deadline_at bigint NOT NULL,
    retry_count bigint NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT agent_loss_retries_pkey PRIMARY KEY (run_id),
    CONSTRAINT agent_loss_retries_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
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
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT node_execution_attempts_pkey PRIMARY KEY (lineage_root_run_id, node_id, attempt_ordinal),
    CONSTRAINT node_execution_attempts_run_id_node_id_claim_generation_att_key UNIQUE (run_id, node_id, claim_generation, attempt_ordinal),
    CONSTRAINT node_execution_attempts_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE run_definition_plans (
    run_id text NOT NULL,
    plan_hash text NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT run_definition_plans_pkey PRIMARY KEY (run_id),
    CONSTRAINT run_definition_plans_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE cron_schedules (
    id text NOT NULL,
    repo_path text NOT NULL,
    github_installation_id bigint NOT NULL DEFAULT 0,
    github_repository_id bigint NOT NULL DEFAULT 0,
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
    team text NOT NULL DEFAULT 'default'::text,
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
    team text NOT NULL DEFAULT 'default'::text,
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
    team text NOT NULL DEFAULT 'default'::text,
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
    team text NOT NULL DEFAULT 'default'::text,
    card_fingerprint text NOT NULL DEFAULT ''::text,
    CONSTRAINT credit_charges_pkey PRIMARY KEY (id)
);

CREATE TABLE storage_quotas (
    principal text NOT NULL,
    tier text NOT NULL DEFAULT ''::text,
    max_bytes_per_run bigint NOT NULL DEFAULT 0,
    max_bytes_per_month bigint NOT NULL DEFAULT 0,
    max_objects_per_run bigint NOT NULL DEFAULT 0,
    storage_allowance_bytes bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT storage_quotas_pkey PRIMARY KEY (principal)
);

CREATE TABLE storage_run_usage (
    principal text NOT NULL,
    run_id text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    objects bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT storage_run_usage_pkey PRIMARY KEY (principal, run_id),
    CONSTRAINT storage_run_usage_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE storage_month_usage (
    principal text NOT NULL,
    month text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    objects bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT storage_month_usage_pkey PRIMARY KEY (principal, month)
);

CREATE TABLE egress_usage (
    principal text NOT NULL,
    month text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    team text NOT NULL DEFAULT 'default'::text,
    CONSTRAINT egress_usage_pkey PRIMARY KEY (principal, month)
);

CREATE TABLE teams (
    name text NOT NULL,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    credit_exhausted_at bigint NOT NULL DEFAULT 0,
    created_by text NOT NULL DEFAULT ''::text,
    display_name text NOT NULL DEFAULT ''::text,
    billing_trust text NOT NULL DEFAULT ''::text,
    billing_trust_by text NOT NULL DEFAULT ''::text,
    billing_trust_at bigint NOT NULL DEFAULT 0,
    billing_trust_reason text NOT NULL DEFAULT ''::text,
    billing_purchase_limit_cents bigint NOT NULL DEFAULT 0,
    billing_budget_cents bigint NOT NULL DEFAULT 0,
    billing_runner_cap bigint NOT NULL DEFAULT 0,
    card_customer text NOT NULL DEFAULT ''::text,
    card_brand text NOT NULL DEFAULT ''::text,
    billing_rung_cents bigint NOT NULL DEFAULT 0,
    billing_daily_cap_cents bigint NOT NULL DEFAULT 0,
    card_last4 text NOT NULL DEFAULT ''::text,
    card_payment_method text NOT NULL DEFAULT ''::text,
    card_fingerprint text NOT NULL DEFAULT ''::text,
    card_added_at bigint NOT NULL DEFAULT 0,
    card_owed_since bigint NOT NULL DEFAULT 0,
    CONSTRAINT teams_pkey PRIMARY KEY (name)
);

CREATE TABLE accounts (
    id text NOT NULL,
    email text NOT NULL,
    email_verified bigint NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT ''::text,
    active_team text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    teams_created bigint NOT NULL DEFAULT 0,
    waitlist_reason text NOT NULL DEFAULT ''::text,
    waitlisted_at bigint NOT NULL DEFAULT 0,
    CONSTRAINT accounts_pkey PRIMARY KEY (id)
);

CREATE TABLE identities (
    provider text NOT NULL,
    subject text NOT NULL,
    account_id text NOT NULL,
    email text NOT NULL DEFAULT ''::text,
    email_verified bigint NOT NULL DEFAULT 0,
    created_at bigint NOT NULL,
    linked bigint NOT NULL DEFAULT 0,
    CONSTRAINT identities_pkey PRIMARY KEY (provider, subject)
);

CREATE TABLE memberships (
    team text NOT NULL,
    account_id text NOT NULL,
    role text NOT NULL,
    created_at bigint NOT NULL,
    CONSTRAINT memberships_pkey PRIMARY KEY (team, account_id),
    CONSTRAINT memberships_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'editor'::text, 'reader'::text])))
);

CREATE TABLE invitations (
    id text NOT NULL,
    team text NOT NULL,
    email text NOT NULL,
    role text NOT NULL,
    invited_by text NOT NULL,
    created_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    accepted_at bigint,
    accepted_by text NOT NULL DEFAULT ''::text,
    withdrawn_at bigint,
    emailed_at bigint,
    CONSTRAINT invitations_pkey PRIMARY KEY (id),
    CONSTRAINT invitations_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'editor'::text, 'reader'::text])))
);

CREATE TABLE github_runner_bindings (
    team text NOT NULL,
    repository_id bigint NOT NULL,
    repository_owner_id bigint NOT NULL,
    repository text NOT NULL,
    created_by text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    CONSTRAINT github_runner_bindings_pkey PRIMARY KEY (team, repository_id)
);

CREATE TABLE github_runner_credentials (
    team text NOT NULL,
    prefix text NOT NULL,
    branch text NOT NULL,
    sha text NOT NULL,
    expires_at bigint NOT NULL,
    run_id text NOT NULL DEFAULT ''::text,
    CONSTRAINT github_runner_credentials_pkey PRIMARY KEY (team, prefix)
);

CREATE TABLE github_app_installations (
    installation_id bigint NOT NULL,
    team text NOT NULL,
    account_id bigint NOT NULL,
    account_login text NOT NULL,
    account_type text NOT NULL,
    suspended bigint NOT NULL DEFAULT 0,
    connected_by text NOT NULL DEFAULT ''::text,
    github_user_id bigint NOT NULL DEFAULT 0,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    CONSTRAINT github_app_installations_pkey PRIMARY KEY (installation_id)
);

CREATE TABLE github_app_triggers (
    team text NOT NULL,
    repository_id bigint NOT NULL,
    pipeline text NOT NULL,
    repository text NOT NULL,
    installation_id bigint NOT NULL,
    on_push bigint NOT NULL DEFAULT 0,
    on_tags bigint NOT NULL DEFAULT 0,
    tag_patterns text NOT NULL DEFAULT '[]'::text,
    on_pull_request bigint NOT NULL DEFAULT 0,
    created_by text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    branches text NOT NULL DEFAULT '[]'::text,
    base_branches text NOT NULL DEFAULT '[]'::text,
    on_pr_closed bigint NOT NULL DEFAULT 0,
    on_pr_labeled bigint NOT NULL DEFAULT 0,
    pr_labels text NOT NULL DEFAULT '[]'::text,
    on_pr_ready_for_review bigint NOT NULL DEFAULT 0,
    on_release_published bigint NOT NULL DEFAULT 0,
    on_release_prereleased bigint NOT NULL DEFAULT 0,
    on_branch_create bigint NOT NULL DEFAULT 0,
    on_branch_delete bigint NOT NULL DEFAULT 0,
    CONSTRAINT github_app_triggers_pkey PRIMARY KEY (team, repository_id, pipeline)
);

CREATE TABLE github_app_connect_states (
    nonce text NOT NULL,
    expires_at bigint NOT NULL,
    CONSTRAINT github_app_connect_states_pkey PRIMARY KEY (nonce)
);

CREATE TABLE github_app_deliveries (
    digest text NOT NULL,
    delivery_id text NOT NULL,
    received_at bigint NOT NULL,
    event text NOT NULL DEFAULT ''::text,
    CONSTRAINT github_app_deliveries_pkey PRIMARY KEY (digest)
);

CREATE TABLE team_deletions (
    slug text NOT NULL,
    requested_by text NOT NULL DEFAULT ''::text,
    requested_at bigint NOT NULL,
    state text NOT NULL,
    attempts bigint NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT ''::text,
    finished_at bigint,
    recheck_at bigint,
    lease_holder text NOT NULL DEFAULT ''::text,
    lease_until bigint,
    CONSTRAINT team_deletions_pkey PRIMARY KEY (slug),
    CONSTRAINT team_deletions_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'done'::text])))
);

CREATE TABLE invitation_email_log (
    address text NOT NULL,
    sent_at bigint NOT NULL
);

CREATE TABLE free_slots (
    team text NOT NULL,
    taken_at bigint NOT NULL,
    event_bytes bigint NOT NULL DEFAULT 0,
    CONSTRAINT free_slots_pkey PRIMARY KEY (team)
);

CREATE TABLE signup_gate (
    id bigint NOT NULL,
    mode text NOT NULL,
    source text NOT NULL DEFAULT ''::text,
    reason text NOT NULL DEFAULT ''::text,
    set_by text NOT NULL DEFAULT ''::text,
    updated_at bigint NOT NULL,
    hourly_limit bigint NOT NULL,
    daily_limit bigint NOT NULL,
    hourly_warn bigint NOT NULL,
    github_min_account_days bigint NOT NULL,
    free_team_members bigint NOT NULL,
    CONSTRAINT signup_gate_pkey PRIMARY KEY (id),
    CONSTRAINT signup_gate_id_check CHECK ((id = 1)),
    CONSTRAINT signup_gate_mode_check CHECK ((mode = ANY (ARRAY['open'::text, 'waitlist'::text])))
);

CREATE TABLE signup_admissions (
    account_id text NOT NULL,
    kind text NOT NULL,
    at bigint NOT NULL,
    CONSTRAINT signup_admissions_pkey PRIMARY KEY (account_id, kind, at),
    CONSTRAINT signup_admissions_kind_check CHECK ((kind = ANY (ARRAY['signup'::text, 'invitation'::text])))
);

CREATE TABLE credit_checkouts (
    id text NOT NULL,
    team text NOT NULL,
    session_id text NOT NULL DEFAULT ''::text,
    amount_micro bigint NOT NULL,
    opened_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    paid_at bigint,
    closed_at bigint,
    CONSTRAINT credit_checkouts_pkey PRIMARY KEY (id)
);

CREATE TABLE credit_freezes (
    dispute_id text NOT NULL,
    team text NOT NULL,
    payment_id text NOT NULL DEFAULT ''::text,
    reason text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    released_at bigint,
    cause text NOT NULL DEFAULT ''::text,
    CONSTRAINT credit_freezes_pkey PRIMARY KEY (dispute_id)
);

CREATE TABLE team_storage (
    team text NOT NULL,
    store text NOT NULL,
    used_bytes bigint NOT NULL DEFAULT 0,
    reserved_bytes bigint NOT NULL DEFAULT 0,
    committed_bytes bigint NOT NULL DEFAULT 0,
    reconciled_at bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    CONSTRAINT team_storage_pkey PRIMARY KEY (team, store)
);

CREATE TABLE storage_reservations (
    id text NOT NULL,
    team text NOT NULL,
    store text NOT NULL,
    bytes bigint NOT NULL,
    created_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    CONSTRAINT storage_reservations_pkey PRIMARY KEY (id)
);

CREATE TABLE team_download_day (
    team text NOT NULL,
    day text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    CONSTRAINT team_download_day_pkey PRIMARY KEY (team, day)
);

CREATE TABLE egress_day (
    service text NOT NULL,
    period text NOT NULL,
    bytes bigint NOT NULL DEFAULT 0,
    updated_at bigint NOT NULL,
    CONSTRAINT egress_day_pkey PRIMARY KEY (service, period)
);

CREATE TABLE git_credentials (
    id text NOT NULL,
    team text NOT NULL,
    host text NOT NULL,
    kind text NOT NULL,
    username text NOT NULL DEFAULT ''::text,
    secret text NOT NULL,
    known_hosts text NOT NULL DEFAULT ''::text,
    fingerprint text NOT NULL DEFAULT ''::text,
    confirmed_at bigint,
    created_by text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    CONSTRAINT git_credentials_pkey PRIMARY KEY (id)
);

CREATE TABLE git_credential_releases (
    id text NOT NULL,
    team text NOT NULL,
    credential_id text NOT NULL,
    host text NOT NULL,
    run_id text NOT NULL,
    runner text NOT NULL,
    token_prefix text NOT NULL,
    released_at bigint NOT NULL,
    CONSTRAINT git_credential_releases_pkey PRIMARY KEY (id)
);

CREATE TABLE git_credential_machines (
    team text NOT NULL,
    token_prefix text NOT NULL,
    enabled_by text NOT NULL DEFAULT ''::text,
    enabled_at bigint NOT NULL,
    CONSTRAINT git_credential_machines_pkey PRIMARY KEY (team, token_prefix)
);

CREATE TABLE github_app_extra_repos (
    team text NOT NULL,
    repository text NOT NULL,
    extra_repo text NOT NULL,
    created_by text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    extra_repo_id bigint NOT NULL DEFAULT 0,
    CONSTRAINT github_app_extra_repos_pkey PRIMARY KEY (team, repository, extra_repo)
);

CREATE TABLE identity_link_states (
    nonce text NOT NULL,
    expires_at bigint NOT NULL,
    CONSTRAINT identity_link_states_pkey PRIMARY KEY (nonce)
);

CREATE TABLE identity_unlinks (
    provider text NOT NULL,
    subject text NOT NULL,
    account_id text NOT NULL,
    unlinked_at bigint NOT NULL,
    CONSTRAINT identity_unlinks_pkey PRIMARY KEY (provider, subject, account_id)
);

CREATE TABLE uploads (
    id text NOT NULL,
    team text NOT NULL,
    run_id text NOT NULL,
    store text NOT NULL,
    key text NOT NULL,
    size bigint NOT NULL,
    sha256 text NOT NULL,
    principal text NOT NULL,
    claim_prefix text NOT NULL,
    provenance text NOT NULL,
    expires_at bigint NOT NULL,
    committed_at bigint NOT NULL DEFAULT 0,
    repo text NOT NULL DEFAULT ''::text,
    ref text NOT NULL DEFAULT ''::text,
    CONSTRAINT uploads_pkey PRIMARY KEY (id)
);

CREATE TABLE data_objects (
    team text NOT NULL,
    key text NOT NULL,
    store text NOT NULL,
    size bigint NOT NULL,
    sha256 text NOT NULL,
    principal text NOT NULL,
    provenance text NOT NULL,
    committed_at bigint NOT NULL,
    ref text NOT NULL DEFAULT ''::text,
    repo text NOT NULL DEFAULT ''::text,
    CONSTRAINT data_objects_pkey PRIMARY KEY (team, key, provenance)
);

CREATE TABLE team_build_trust (
    team text NOT NULL,
    trust_local_builds bigint NOT NULL DEFAULT 0,
    CONSTRAINT team_build_trust_pkey PRIMARY KEY (team)
);

CREATE TABLE storage_commit_receipts (
    team text NOT NULL,
    store text NOT NULL,
    id text NOT NULL,
    bytes bigint NOT NULL,
    committed_at bigint NOT NULL,
    renew_request bytea,
    renew_status text NOT NULL DEFAULT ''::text,
    renew_result bytea,
    CONSTRAINT storage_commit_receipts_pkey PRIMARY KEY (team, store, id)
);

CREATE TABLE claim_tokens (
    digest text NOT NULL,
    prefix text NOT NULL,
    team text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL,
    claim_generation bigint NOT NULL,
    kind text NOT NULL,
    created_at bigint NOT NULL,
    expires_at bigint NOT NULL,
    result_digest text NOT NULL DEFAULT ''::text,
    source_mints bigint NOT NULL DEFAULT 0,
    CONSTRAINT claim_tokens_pkey PRIMARY KEY (digest),
    CONSTRAINT claim_tokens_team_run_id_node_id_claim_generation_key UNIQUE (team, run_id, node_id, claim_generation),
    CONSTRAINT claim_tokens_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE child_invocations (
    team text NOT NULL,
    parent_run_id text NOT NULL,
    parent_node_id text NOT NULL,
    claim_generation bigint NOT NULL,
    ordinal bigint NOT NULL,
    request_digest text NOT NULL,
    child_run_id text NOT NULL,
    created_at bigint NOT NULL,
    CONSTRAINT child_invocations_pkey PRIMARY KEY (team, parent_run_id, parent_node_id, claim_generation, ordinal),
    CONSTRAINT child_invocations_parent_run_id_fkey FOREIGN KEY (parent_run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE business_events (
    id text NOT NULL,
    ts bigint NOT NULL,
    team text NOT NULL DEFAULT ''::text,
    account text NOT NULL DEFAULT ''::text,
    kind text NOT NULL,
    subject_id text NOT NULL DEFAULT ''::text,
    actor text NOT NULL DEFAULT ''::text,
    attrs jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT business_events_pkey PRIMARY KEY (id)
);

CREATE TABLE repos (
    team text NOT NULL,
    repo text NOT NULL,
    dispatch text NOT NULL DEFAULT ''::text,
    updated_at bigint NOT NULL,
    CONSTRAINT repos_pkey PRIMARY KEY (team, repo)
);

CREATE TABLE card_charges (
    id text NOT NULL,
    team text NOT NULL,
    amount_micro bigint NOT NULL,
    cause text NOT NULL,
    state text NOT NULL,
    failures bigint NOT NULL DEFAULT 0,
    decline_code text NOT NULL DEFAULT ''::text,
    next_attempt_at bigint NOT NULL,
    payment_intent text NOT NULL DEFAULT ''::text,
    opened_at bigint NOT NULL,
    closed_at bigint,
    CONSTRAINT card_charges_pkey PRIMARY KEY (id)
);

CREATE TABLE card_attempts (
    id text NOT NULL,
    team text NOT NULL,
    charge_id text NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    payment_intent text NOT NULL DEFAULT ''::text,
    decline_code text NOT NULL DEFAULT ''::text,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    CONSTRAINT card_attempts_pkey PRIMARY KEY (id)
);

CREATE TABLE card_refunds (
    team text NOT NULL,
    payment_intent text NOT NULL,
    reason text NOT NULL,
    refund_id text NOT NULL DEFAULT ''::text,
    status text NOT NULL DEFAULT 'due'::text,
    attempts bigint NOT NULL DEFAULT 0,
    next_attempt_at bigint NOT NULL DEFAULT 0,
    created_at bigint NOT NULL,
    CONSTRAINT card_refunds_pkey PRIMARY KEY (team, payment_intent)
);

CREATE TABLE team_spend_days (
    team text NOT NULL,
    day bigint NOT NULL,
    amount_micro bigint NOT NULL,
    CONSTRAINT team_spend_days_pkey PRIMARY KEY (team, day)
);

CREATE TABLE card_spend_days (
    fingerprint text NOT NULL,
    day bigint NOT NULL,
    amount_micro bigint NOT NULL,
    CONSTRAINT card_spend_days_pkey PRIMARY KEY (fingerprint, day)
);

CREATE TABLE payment_warnings (
    warning_id text NOT NULL,
    payment_intent text NOT NULL,
    fingerprint text NOT NULL DEFAULT ''::text,
    actionable bigint NOT NULL DEFAULT 1,
    created_at bigint NOT NULL,
    CONSTRAINT payment_warnings_pkey PRIMARY KEY (warning_id)
);

CREATE TABLE node_outputs (
    team text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL,
    attempt bigint NOT NULL,
    key text NOT NULL,
    size bigint NOT NULL,
    sha256 text NOT NULL,
    CONSTRAINT node_outputs_pkey PRIMARY KEY (team, run_id, node_id)
);

CREATE TABLE output_runs (
    team text NOT NULL,
    run_id text NOT NULL,
    CONSTRAINT output_runs_pkey PRIMARY KEY (team, run_id)
);

CREATE TABLE output_tombstones (
    team text NOT NULL,
    key text NOT NULL,
    run_id text NOT NULL,
    CONSTRAINT output_tombstones_pkey PRIMARY KEY (team, key)
);

CREATE TABLE github_app_automation (
    team text NOT NULL,
    repository_id bigint NOT NULL,
    repository text NOT NULL,
    installation_id bigint NOT NULL,
    enabled_by text NOT NULL,
    enabled_at bigint NOT NULL,
    CONSTRAINT github_app_automation_pkey PRIMARY KEY (team, repository_id),
    CONSTRAINT github_app_automation_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES github_app_installations(installation_id) ON DELETE CASCADE
);

CREATE TABLE node_logs (
    team text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL,
    attempt bigint NOT NULL,
    generation bigint NOT NULL,
    prefix text NOT NULL,
    uploaded_through bigint NOT NULL DEFAULT 0,
    stored_bytes bigint NOT NULL DEFAULT 0,
    charged_bytes bigint NOT NULL DEFAULT 0,
    sealed_at bigint,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL,
    CONSTRAINT node_logs_pkey PRIMARY KEY (team, run_id, node_id, attempt, generation)
);

CREATE TABLE trigger_claim_tokens (
    digest text NOT NULL,
    prefix text NOT NULL,
    team text NOT NULL,
    trigger_id text NOT NULL,
    claim_seq bigint NOT NULL,
    created_at bigint NOT NULL,
    CONSTRAINT trigger_claim_tokens_pkey PRIMARY KEY (digest),
    CONSTRAINT trigger_claim_tokens_team_trigger_id_claim_seq_key UNIQUE (team, trigger_id, claim_seq),
    CONSTRAINT trigger_claim_tokens_trigger_id_fkey FOREIGN KEY (trigger_id) REFERENCES triggers(id) ON DELETE CASCADE
);

CREATE TABLE node_log_segments (
    team text NOT NULL,
    run_id text NOT NULL,
    node_id text NOT NULL,
    attempt bigint NOT NULL,
    generation bigint NOT NULL,
    n bigint NOT NULL,
    stored_bytes bigint NOT NULL,
    md5 text NOT NULL,
    sha256 text NOT NULL,
    CONSTRAINT node_log_segments_pkey PRIMARY KEY (team, run_id, node_id, attempt, generation, n)
);

CREATE INDEX idx_runs_started ON runs USING btree (started_at DESC);
CREATE INDEX idx_runs_pipeline ON runs USING btree (pipeline, started_at DESC);
CREATE INDEX idx_runs_sha_started ON runs USING btree (git_sha, started_at DESC);
CREATE INDEX idx_runs_branch_started ON runs USING btree (git_branch, started_at DESC);
CREATE INDEX idx_runs_repo_slug_started ON runs USING btree (declared_repo, started_at DESC);
CREATE INDEX idx_runs_repo_sha_started ON runs USING btree (declared_repo, git_sha, started_at DESC);
CREATE INDEX idx_runs_repo_branch_started ON runs USING btree (declared_repo, git_branch, started_at DESC);
CREATE INDEX idx_nodes_claimable ON nodes USING btree (ready_at) WHERE ((ready_at IS NOT NULL) AND (claimed_by IS NULL) AND (status <> 'done'::text));
CREATE INDEX idx_nodes_claimed_lease ON nodes USING btree (lease_expires_at) WHERE (claimed_by IS NOT NULL);
CREATE INDEX idx_events_run_ts ON events USING btree (run_id, ts);
CREATE INDEX idx_triggers_claimed_lease ON triggers USING btree (status, lease_expires_at) WHERE (status = 'claimed'::text);
CREATE INDEX idx_triggers_source_status_created ON triggers USING btree (trigger_source, status, created_at);
CREATE INDEX idx_concurrency_holders_lease ON concurrency_holders USING btree (lease_expires_at);
CREATE INDEX idx_concurrency_cache_expires ON concurrency_cache USING btree (expires_at);
CREATE INDEX idx_concurrency_cache_lru ON concurrency_cache USING btree (last_hit_at);
CREATE INDEX idx_concurrency_cache_origin_run ON concurrency_cache USING btree (origin_run_id);
CREATE INDEX idx_node_steps_lookup ON node_steps USING btree (run_id, node_id);
CREATE INDEX idx_node_metrics_lookup ON node_metrics USING btree (run_id, node_id, ts);
CREATE INDEX idx_sessions_expires ON sessions USING btree (expires_at);
CREATE INDEX idx_debug_pauses_open ON debug_pauses USING btree (run_id) WHERE (released_at IS NULL);
CREATE INDEX idx_approvals_pending ON approvals USING btree (requested_at) WHERE (resolved_at IS NULL);
CREATE INDEX idx_node_dispatches_lookup ON node_dispatches USING btree (run_id, node_id, seq DESC);
CREATE INDEX idx_node_bounces_pending ON node_bounces USING btree (run_id, node_id) WHERE (consumed_at IS NULL);
CREATE UNIQUE INDEX idx_tokens_prefix ON tokens USING btree (prefix);
CREATE INDEX idx_triggers_pending ON triggers USING btree (status, available_at, created_at) WHERE (status = 'pending'::text);
CREATE INDEX idx_agent_loss_retries_source ON agent_loss_retries USING btree (source_run_id);
CREATE INDEX idx_node_execution_attempts_run ON node_execution_attempts USING btree (run_id, node_id, attempt_ordinal);
CREATE INDEX idx_nodes_assisted_claimable ON nodes USING btree (execution_policy_version, ready_at, run_id, node_id) WHERE ((ready_at IS NOT NULL) AND (claimed_by IS NULL) AND (outcome = ''::text) AND (finished_at IS NULL) AND (execution_policy_hash <> ''::text));
CREATE INDEX idx_cron_fires_schedule_decided ON cron_fires USING btree (schedule_id, decided_at DESC);
CREATE INDEX idx_credit_grants_created ON credit_grants USING btree (created_at);
CREATE INDEX idx_credit_charges_charged ON credit_charges USING btree (charged_at);
CREATE INDEX idx_credit_charges_node ON credit_charges USING btree (run_id, node_id);
CREATE INDEX idx_events_ts ON events USING btree (ts);
CREATE INDEX idx_node_metrics_ts ON node_metrics USING btree (ts);
CREATE INDEX idx_nodes_outstanding ON nodes USING btree (status, ready_at, claimed_by) WHERE (status <> 'done'::text);
CREATE INDEX idx_nodes_credit_window ON nodes USING btree (credit_charged_through) WHERE (credit_charged_through <> 0);
CREATE INDEX idx_credit_grants_kind_amount ON credit_grants USING btree (kind, amount_micro);
CREATE INDEX idx_credit_charges_kind_amount ON credit_charges USING btree (kind, amount_micro, seconds);
CREATE INDEX idx_nodes_credit_active ON nodes USING btree (credit_charged_through);
CREATE INDEX idx_nodes_credit_principal ON nodes USING btree (claim_principal, credit_charged_through);
CREATE INDEX idx_runs_created ON runs USING btree (created_at);
CREATE INDEX idx_runs_principal_created ON runs USING btree (created_principal, created_at);
CREATE INDEX idx_egress_usage_month ON egress_usage USING btree (month);
CREATE INDEX idx_credit_grants_kind_created ON credit_grants USING btree (kind, created_at);
CREATE INDEX idx_runs_team_started ON runs USING btree (team, started_at DESC);
CREATE INDEX idx_concurrency_holders_key_claimed ON concurrency_holders USING btree (team, key, claimed_at);
CREATE INDEX idx_concurrency_waiters_arrived ON concurrency_waiters USING btree (team, key, arrived_at);
CREATE UNIQUE INDEX idx_accounts_verified_email ON accounts USING btree (email) WHERE (email_verified = 1);
CREATE INDEX idx_identities_account ON identities USING btree (account_id);
CREATE INDEX idx_memberships_account ON memberships USING btree (account_id);
CREATE UNIQUE INDEX idx_invitations_open ON invitations USING btree (team, email) WHERE ((accepted_at IS NULL) AND (withdrawn_at IS NULL));
CREATE INDEX idx_invitations_email ON invitations USING btree (email);
CREATE UNIQUE INDEX idx_cron_schedules_repo_pipeline_name ON cron_schedules USING btree (team, repo_path, pipeline, schedule_name);
CREATE UNIQUE INDEX idx_triggers_idempotency_key ON triggers USING btree (team, pipeline, idempotency_key) WHERE (idempotency_key <> ''::text);
CREATE UNIQUE INDEX idx_triggers_webhook_delivery ON triggers USING btree (team, webhook_delivery) WHERE (webhook_delivery <> ''::text);
CREATE UNIQUE INDEX idx_triggers_webhook_replay_key ON triggers USING btree (team, webhook_replay_key) WHERE (webhook_replay_key <> ''::text);
CREATE UNIQUE INDEX idx_credit_grants_team_reference ON credit_grants USING btree (team, kind, reference) WHERE (reference <> ''::text);
CREATE INDEX idx_github_app_installations_team ON github_app_installations USING btree (team);
CREATE INDEX idx_github_app_triggers_installation ON github_app_triggers USING btree (installation_id, repository_id);
CREATE INDEX idx_team_deletions_state ON team_deletions USING btree (state);
CREATE INDEX idx_invitation_email_log_address ON invitation_email_log USING btree (address, sent_at);
CREATE INDEX idx_triggers_team_created ON triggers USING btree (team, created_at);
CREATE INDEX idx_signup_admissions_at ON signup_admissions USING btree (at);
CREATE INDEX idx_accounts_created ON accounts USING btree (created_at);
CREATE INDEX idx_accounts_waitlisted ON accounts USING btree (waitlisted_at) WHERE (waitlisted_at <> 0);
CREATE INDEX idx_credit_checkouts_team_expires ON credit_checkouts USING btree (team, expires_at);
CREATE INDEX idx_credit_checkouts_session ON credit_checkouts USING btree (session_id);
CREATE INDEX idx_credit_freezes_team ON credit_freezes USING btree (team, released_at);
CREATE INDEX idx_storage_reservations_team ON storage_reservations USING btree (team, store, expires_at);
CREATE INDEX idx_storage_reservations_expiry ON storage_reservations USING btree (expires_at);
CREATE UNIQUE INDEX idx_git_credentials_team_host ON git_credentials USING btree (team, host);
CREATE INDEX idx_git_credential_releases_team ON git_credential_releases USING btree (team, released_at);
CREATE INDEX idx_triggers_github_commit ON triggers USING btree (github_repo_id, git_sha);
CREATE INDEX idx_identity_unlinks_account ON identity_unlinks USING btree (account_id);
CREATE INDEX idx_uploads_expiry ON uploads USING btree (expires_at);
CREATE INDEX idx_cron_schedules_github_identity ON cron_schedules USING btree (team, github_installation_id, github_repository_id);
CREATE INDEX idx_storage_commit_receipts_age ON storage_commit_receipts USING btree (committed_at);
CREATE INDEX idx_credit_charges_team_amount ON credit_charges USING btree (team, amount_micro);
CREATE UNIQUE INDEX idx_business_events_subject ON business_events USING btree (team, kind, subject_id);
CREATE INDEX idx_business_events_team_ts ON business_events USING btree (team, ts);
CREATE INDEX idx_business_events_kind_ts ON business_events USING btree (kind, ts);
CREATE INDEX idx_triggers_team_pending ON triggers USING btree (team, status) WHERE (status = 'pending'::text);
CREATE UNIQUE INDEX idx_card_charges_one_open ON card_charges USING btree (team) WHERE (state = 'open'::text);
CREATE INDEX idx_card_charges_due ON card_charges USING btree (state, next_attempt_at);
CREATE UNIQUE INDEX idx_card_attempts_one_live ON card_attempts USING btree (charge_id) WHERE (status = 'live'::text);
CREATE INDEX idx_card_attempts_team ON card_attempts USING btree (team, status, updated_at);
CREATE INDEX idx_card_attempts_intent ON card_attempts USING btree (payment_intent);
CREATE INDEX idx_payment_warnings_intent ON payment_warnings USING btree (payment_intent);
CREATE INDEX idx_payment_warnings_card ON payment_warnings USING btree (fingerprint);
CREATE INDEX idx_github_app_deliveries_received ON github_app_deliveries USING btree (received_at);
CREATE INDEX idx_github_app_automation_installation ON github_app_automation USING btree (installation_id);
CREATE INDEX idx_node_logs_unsealed ON node_logs USING btree (updated_at) WHERE (sealed_at IS NULL);
CREATE UNIQUE INDEX idx_tokens_agent_name ON tokens USING btree (team, principal) WHERE ((kind = ANY (ARRAY['runner'::text, 'team'::text])) AND (revoked_at IS NULL) AND (substr(principal, 1, 6) = 'agent:'::text));
