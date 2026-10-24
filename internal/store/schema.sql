-- safety: this is the only baseline. BLOB translates to BYTEA on PostgreSQL.
-- safety: every timestamp is Unix nanoseconds; persisted JSON envelopes carry a version.
-- safety: financial rows retain plain run IDs because run retention must not refund spent credit.

CREATE TABLE teams (
    name TEXT NOT NULL PRIMARY KEY,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    members_can_add_machines BIGINT NOT NULL DEFAULT 1 CHECK(members_can_add_machines IN (0,1)),
    claim_ranking TEXT NOT NULL DEFAULT '',
    max_children_per_root BIGINT NOT NULL DEFAULT 50 CHECK(max_children_per_root BETWEEN 1 AND 1000),
    claim_award_ordinal BIGINT NOT NULL DEFAULT 0 CHECK(claim_award_ordinal >= 0),
    billing_trust_at BIGINT NOT NULL DEFAULT 0,
    billing_trust_reason TEXT NOT NULL DEFAULT '',
    billing_trust TEXT NOT NULL DEFAULT '' CHECK(billing_trust IN ('','granted','revoked')),
    billing_trust_by TEXT NOT NULL DEFAULT '',
    card_customer TEXT NOT NULL DEFAULT '',
    card_payment_method TEXT NOT NULL DEFAULT '',
    card_fingerprint TEXT NOT NULL DEFAULT '',
    card_brand TEXT NOT NULL DEFAULT '',
    card_added_at BIGINT NOT NULL DEFAULT 0,
    card_last4 TEXT NOT NULL DEFAULT '',
    refill_enabled BIGINT NOT NULL DEFAULT 0 CHECK(refill_enabled IN (0,1)),
    refill_threshold_cents BIGINT NOT NULL DEFAULT 0 CHECK(refill_threshold_cents >= 0),
    refill_amount_cents BIGINT NOT NULL DEFAULT 0 CHECK(refill_amount_cents = 0 OR refill_amount_cents >= 1000),
    refill_week_limit_cents BIGINT NOT NULL DEFAULT 0 CHECK(refill_week_limit_cents >= 0),
    refill_month_limit_cents BIGINT NOT NULL DEFAULT 0 CHECK(refill_month_limit_cents >= 0),
    next_run_number BIGINT NOT NULL DEFAULT 1 CHECK(next_run_number > 0),
    CHECK((card_payment_method = '') = (card_fingerprint = '') AND (card_payment_method = '') = (card_customer = '')),
    CHECK(refill_enabled = 0 OR (refill_amount_cents >= 1000 AND card_payment_method <> ''))
);

CREATE TABLE claim_awards (
    singleton BIGINT PRIMARY KEY CHECK(singleton=1),
    ordinal BIGINT NOT NULL DEFAULT 0 CHECK(ordinal >= 0)
);
INSERT INTO claim_awards(singleton,ordinal) VALUES (1,0);
CREATE TABLE team_first_success (
    team TEXT NOT NULL PRIMARY KEY REFERENCES teams(name),
    run_id TEXT NOT NULL CHECK(run_id <> '')
);

CREATE TABLE runs (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL PRIMARY KEY,
    number BIGINT NOT NULL CHECK(number > 0),
    pipeline TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending','running','success','failed','cancelled')),
    error TEXT NOT NULL DEFAULT '',
    request_json BLOB NOT NULL,
    request_hash TEXT NOT NULL,
    submitted_env TEXT NOT NULL DEFAULT '',
    idempotency_scope TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    idempotency_digest TEXT NOT NULL DEFAULT '',
    request_sealed_at BIGINT NOT NULL,
    spec_json BLOB,
    spec_hash TEXT NOT NULL DEFAULT '',
    sealed_at BIGINT,
    trigger_source TEXT NOT NULL DEFAULT '',
    git_branch TEXT NOT NULL DEFAULT '',
    git_sha TEXT NOT NULL DEFAULT '',
    repo_key TEXT NOT NULL,
    repo_display TEXT NOT NULL DEFAULT '',
    repo_identity TEXT NOT NULL DEFAULT '',
    retry_of TEXT NOT NULL DEFAULT '',
    lineage_json BLOB,
    replay_of_run_id TEXT NOT NULL DEFAULT '',
    replay_of_node_id TEXT NOT NULL DEFAULT '',
    parent_run_id TEXT,
    parent_node_id TEXT NOT NULL DEFAULT '',
    plan_json BLOB,
    plan_hash TEXT NOT NULL DEFAULT '',
    plan_accepted_generation BIGINT NOT NULL DEFAULT 0,
    dispatch TEXT NOT NULL CHECK(dispatch IN ('host','controller')),
    intake_mode TEXT NOT NULL CHECK(intake_mode IN ('execute','publish')),
    admission TEXT NOT NULL DEFAULT '',
    allowed_pools BLOB NOT NULL,
    created_subject_kind TEXT NOT NULL DEFAULT '',
    created_principal TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    started_at BIGINT,
    finished_at BIGINT,
    cancel_requested_at BIGINT,
    drain_deadline_at BIGINT GENERATED ALWAYS AS (cancel_requested_at + 300000000000) STORED,
    cancel_reason TEXT NOT NULL DEFAULT '',
    claim_wait_ns BIGINT NOT NULL DEFAULT 0,
    state_seq BIGINT NOT NULL DEFAULT 0 CHECK(state_seq >= 0),
    state_digest TEXT NOT NULL DEFAULT '',
    state_actor BLOB,
    event_count BIGINT NOT NULL DEFAULT 0,
    event_bytes BIGINT NOT NULL DEFAULT 0,
    annotations_json BLOB,
    annotation_count BIGINT NOT NULL DEFAULT 0,
    top_annotation TEXT NOT NULL DEFAULT '',
    log_retention_class TEXT NOT NULL,
    log_retention_days BIGINT NOT NULL CHECK(log_retention_days > 0),
    UNIQUE(team,id),
    UNIQUE(team,number),
    CHECK((idempotency_key = '' AND idempotency_scope = '' AND idempotency_digest = '') OR (idempotency_key <> '' AND idempotency_scope <> '' AND idempotency_digest <> '')),
    CHECK((status IN ('success','failed','cancelled')) = (finished_at IS NOT NULL)),
    CHECK((sealed_at IS NULL AND spec_json IS NULL AND spec_hash = '' AND plan_json IS NULL AND plan_hash = '') OR (sealed_at IS NOT NULL AND spec_json IS NOT NULL AND spec_hash <> '' AND plan_json IS NOT NULL AND plan_hash <> '')),
    CHECK(claim_wait_ns >= 0),
    CHECK(event_count >= 0),
    CHECK(event_bytes >= 0),
    CHECK(annotation_count >= 0),
    CHECK(plan_accepted_generation >= 0)
);

CREATE TABLE claim_requirements (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL CHECK(id <> ''),
    PRIMARY KEY(team,id)
);

CREATE TABLE claim_requirement_terms (
    team TEXT NOT NULL REFERENCES teams(name),
    requirements TEXT NOT NULL,
    term BIGINT NOT NULL CHECK(term >= 0),
    label TEXT NOT NULL,
    PRIMARY KEY(team,requirements,term,label),
    FOREIGN KEY(team,requirements) REFERENCES claim_requirements(team,id) ON DELETE CASCADE
);

CREATE TABLE claim_contexts (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL CHECK(id <> ''),
    requirements TEXT NOT NULL DEFAULT '',
    requirements_ref TEXT GENERATED ALWAYS AS (NULLIF(requirements,'')) STORED,
    unit_kind TEXT NOT NULL CHECK(unit_kind IN ('node','plan','trigger')),
    PRIMARY KEY(team,id),
    FOREIGN KEY(team,requirements_ref) REFERENCES claim_requirements(team,id)
);
CREATE INDEX idx_claim_contexts_requirements ON claim_contexts(team,requirements,unit_kind,id);

CREATE TABLE nodes (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending','running','done','approval_pending')),
    outcome TEXT NOT NULL DEFAULT '' CHECK(outcome IN ('','success','failed','cancelled','skipped','superseded','memoized')),
    deps_json BLOB,
    started_at BIGINT,
    finished_at BIGINT,
    error TEXT NOT NULL DEFAULT '',
    ready_at BIGINT,
    queue_started_at BIGINT,
    claimed_by TEXT,
    claim_principal TEXT NOT NULL DEFAULT '',
    claim_context TEXT NOT NULL DEFAULT '',
    claim_context_ref TEXT GENERATED ALWAYS AS (NULLIF(claim_context,'')) STORED,
    claim_requirements TEXT NOT NULL DEFAULT '',
    claim_requirements_ref TEXT GENERATED ALWAYS AS (NULLIF(claim_requirements,'')) STORED,
    capability_prefix TEXT NOT NULL DEFAULT '',
    owner_capability_prefix TEXT NOT NULL DEFAULT '',
    lease_expires_at BIGINT,
    executor_kind TEXT NOT NULL DEFAULT '',
    executor_id TEXT NOT NULL DEFAULT '',
    execution_started_at BIGINT,
    generation BIGINT NOT NULL DEFAULT 0 CHECK(generation >= 0),
    executor_location TEXT NOT NULL DEFAULT 'unknown',
    needs_labels BLOB,
    prefers_labels BLOB,
    requested_cores DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK(requested_cores >= 0),
    requested_memory_bytes BIGINT NOT NULL DEFAULT 0 CHECK(requested_memory_bytes >= 0),
    requested_slots BIGINT NOT NULL DEFAULT 1 CHECK(requested_slots >= 0),
    offer_started_at BIGINT,
    placement_reason TEXT NOT NULL DEFAULT '',
    placement_hold_from BIGINT,
    status_detail TEXT NOT NULL DEFAULT '',
    last_heartbeat BIGINT,
    failure_reason TEXT NOT NULL DEFAULT '',
    exit_code BIGINT,
    seq BIGINT NOT NULL DEFAULT 0,
    annotations_json BLOB,
    summary TEXT NOT NULL DEFAULT '',
    max_rss_bytes BIGINT NOT NULL DEFAULT 0 CHECK(max_rss_bytes >= 0),
    process_wall_nanos BIGINT NOT NULL DEFAULT 0 CHECK(process_wall_nanos >= 0),
    cpu_nanos BIGINT NOT NULL DEFAULT 0 CHECK(cpu_nanos >= 0),
    team TEXT NOT NULL REFERENCES teams(name),
    optional BIGINT NOT NULL DEFAULT 0 CHECK(optional IN (0,1)),
    retry_budget BIGINT NOT NULL DEFAULT 0 CHECK(retry_budget >= 0),
    retry_backoff_ms BIGINT NOT NULL DEFAULT 0 CHECK(retry_backoff_ms >= 0),
    failure_json BLOB,
    output_record BLOB,
    manifest_record BLOB,
    kind TEXT NOT NULL DEFAULT '',
    spec_hash TEXT NOT NULL DEFAULT '',
    on_failure_of TEXT NOT NULL DEFAULT '',
    continue_on_error BIGINT NOT NULL DEFAULT 0 CHECK(continue_on_error IN (0,1)),
    cancel_requested_at BIGINT,
    drain_deadline_at BIGINT GENERATED ALWAYS AS (cancel_requested_at + 300000000000) STORED,
    cancel_reason TEXT NOT NULL DEFAULT '',
    approval_json BLOB,
    attention_reason TEXT NOT NULL DEFAULT '',
    timeout_ms BIGINT NOT NULL DEFAULT 0 CHECK(timeout_ms >= 0),
    generated_json BLOB,
    state_seq BIGINT NOT NULL DEFAULT 0 CHECK(state_seq >= 0),
    state_digest TEXT NOT NULL DEFAULT '',
    state_actor BLOB,
    disruptions BIGINT NOT NULL DEFAULT 0 CHECK(disruptions BETWEEN 0 AND 3),
    attempt BIGINT NOT NULL DEFAULT 0 CHECK(attempt >= 0),
    expansion TEXT NOT NULL DEFAULT '' CHECK(expansion IN ('','expected','accepted','failed','none')),
    spec_json BLOB NOT NULL,
    execution_json BLOB NOT NULL,
    expansion_digest TEXT NOT NULL DEFAULT '',
    expansion_body BLOB,
    expansion_registration BLOB,
    expansion_failure BLOB,
    PRIMARY KEY (team, run_id, node_id),
    CHECK((claimed_by IS NULL AND capability_prefix = '' AND lease_expires_at IS NULL) OR (claimed_by IS NOT NULL AND claimed_by <> '' AND capability_prefix <> '' AND lease_expires_at IS NOT NULL AND generation > 0)),
    CHECK(status <> 'running' OR (claimed_by IS NOT NULL AND started_at IS NOT NULL AND attempt > 0)),
    CHECK(status <> 'approval_pending' OR claimed_by IS NULL),
    CHECK(owner_capability_prefix = '' OR (claimed_by IS NOT NULL AND capability_prefix <> '')),
    CHECK((status = 'done') = (outcome <> '')),
    CHECK((status = 'done') = (finished_at IS NOT NULL)),
    CHECK((output_record IS NULL AND manifest_record IS NULL) OR status = 'done'),
    CHECK(status <> 'done' OR (claimed_by IS NULL AND lease_expires_at IS NULL AND capability_prefix = '')),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,claim_context_ref) REFERENCES claim_contexts(team,id),
    FOREIGN KEY(team,claim_requirements_ref) REFERENCES claim_requirements(team,id)
);

CREATE TABLE sparkwing_schema_version (
    version BIGINT NOT NULL,
    applied_at BIGINT NOT NULL,
    PRIMARY KEY (version)
);

CREATE TABLE events (
    run_id TEXT NOT NULL,
    seq BIGINT NOT NULL,
    node_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    ts BIGINT NOT NULL,
    payload BLOB,
    team TEXT NOT NULL REFERENCES teams(name),
    state_seq BIGINT NOT NULL DEFAULT 0 CHECK(state_seq >= 0),
    PRIMARY KEY (team, run_id, seq),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE
);

CREATE TABLE triggers (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','claimed','done','failed')),
    unit_kind TEXT NOT NULL CHECK(unit_kind IN ('trigger','plan')),
    available_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    claimed_at BIGINT,
    started_at BIGINT,
    finished_at BIGINT,
    lease_expires_at BIGINT,
    expires_at BIGINT,
    generation BIGINT NOT NULL DEFAULT 0 CHECK(generation >= 0),
    claimed_by TEXT,
    capability_prefix TEXT NOT NULL DEFAULT '',
    claim_principal TEXT NOT NULL DEFAULT '',
    claim_context TEXT NOT NULL DEFAULT '',
    claim_context_ref TEXT GENERATED ALWAYS AS (NULLIF(claim_context,'')) STORED,
    claim_requirements TEXT NOT NULL DEFAULT '',
    claim_requirements_ref TEXT GENERATED ALWAYS AS (NULLIF(claim_requirements,'')) STORED,
    needs_labels BLOB NOT NULL,
    prefers_labels BLOB NOT NULL,
    disruptions BIGINT NOT NULL DEFAULT 0 CHECK(disruptions BETWEEN 0 AND 3),
    cancel_requested_at BIGINT,
    drain_deadline_at BIGINT GENERATED ALWAYS AS (cancel_requested_at + 300000000000) STORED,
    webhook_delivery TEXT NOT NULL DEFAULT '',
    webhook_replay_key TEXT NOT NULL DEFAULT '',
    github_check_run_id BIGINT NOT NULL DEFAULT 0,
    github_status_fallback BIGINT NOT NULL DEFAULT 0 CHECK(github_status_fallback IN (0,1)),
    UNIQUE(team,id),
    CHECK((status = 'claimed') = (claimed_by IS NOT NULL)),
    CHECK((claimed_by IS NULL AND capability_prefix = '' AND lease_expires_at IS NULL AND claimed_at IS NULL) OR (claimed_by IS NOT NULL AND claimed_by <> '' AND capability_prefix <> '' AND lease_expires_at IS NOT NULL AND claimed_at IS NOT NULL AND generation > 0)),
    CHECK((status IN ('done','failed')) = (finished_at IS NOT NULL)),
    CHECK(status <> 'pending' OR started_at IS NULL),
    FOREIGN KEY(team,id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,claim_context_ref) REFERENCES claim_contexts(team,id),
    FOREIGN KEY(team,claim_requirements_ref) REFERENCES claim_requirements(team,id)
);

CREATE TABLE node_attempts (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    attempt BIGINT NOT NULL CHECK(attempt > 0),
    generation BIGINT NOT NULL CHECK(generation > 0),
    holder TEXT NOT NULL CHECK(holder <> ''),
    capability_prefix TEXT NOT NULL CHECK(capability_prefix <> ''),
    executor_kind TEXT NOT NULL,
    executor_name TEXT NOT NULL,
    executor_location TEXT NOT NULL,
    started_at BIGINT NOT NULL,
    finished_at BIGINT,
    outcome TEXT NOT NULL DEFAULT '' CHECK(outcome IN ('','success','failed','cancelled','skipped','superseded','memoized')),
    failure_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(team,run_id,node_id,attempt),
    UNIQUE(team,run_id,node_id,generation,attempt),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id,node_id) REFERENCES nodes(team,run_id,node_id) ON DELETE CASCADE,
    CHECK((finished_at IS NOT NULL) = (outcome <> '')),
    CHECK(finished_at IS NULL OR finished_at >= started_at)
);

CREATE UNIQUE INDEX idx_node_attempts_open ON node_attempts(team,run_id,node_id) WHERE finished_at IS NULL;

CREATE TABLE node_steps (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    attempt BIGINT NOT NULL CHECK(attempt > 0),
    generation BIGINT NOT NULL CHECK(generation > 0),
    step_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('running','passed','failed','cancelled','skipped')),
    error TEXT NOT NULL DEFAULT '',
    started_at BIGINT NOT NULL,
    finished_at BIGINT,
    source_started_at BIGINT,
    source_finished_at BIGINT,
    annotations_json BLOB,
    summary TEXT NOT NULL DEFAULT '',
    team TEXT NOT NULL REFERENCES teams(name),
    PRIMARY KEY (team, run_id, node_id, attempt, step_id),
    CHECK((status <> 'running') = (finished_at IS NOT NULL)),
    CHECK(finished_at IS NULL OR finished_at >= started_at),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id,node_id,generation,attempt) REFERENCES node_attempts(team,run_id,node_id,generation,attempt) ON DELETE CASCADE
);

CREATE TABLE node_metrics (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    ts BIGINT NOT NULL,
    cpu_millicores BIGINT NOT NULL,
    memory_bytes BIGINT NOT NULL,
    cpu_time_nanos BIGINT NOT NULL DEFAULT 0,
    team TEXT NOT NULL REFERENCES teams(name),
    kind TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team, run_id, node_id, ts),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    CHECK(cpu_millicores >= 0),
    CHECK(memory_bytes >= 0),
    CHECK(cpu_time_nanos >= 0),
    FOREIGN KEY(team,run_id,node_id) REFERENCES nodes(team,run_id,node_id) ON DELETE CASCADE
);

CREATE TABLE accounts (
    id TEXT NOT NULL PRIMARY KEY,
    email TEXT NOT NULL,
    email_verified BIGINT NOT NULL DEFAULT 0,
    name TEXT NOT NULL DEFAULT '',
    active_team TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    teams_created BIGINT NOT NULL DEFAULT 0,
    waitlist_reason TEXT NOT NULL DEFAULT '',
    waitlisted_at BIGINT NOT NULL DEFAULT 0,
    CHECK(email_verified IN (0,1)),
    CHECK(teams_created >= 0)
);

CREATE TABLE sessions (
    credential_epoch TEXT NOT NULL DEFAULT '',
    hash TEXT NOT NULL PRIMARY KEY,
    principal TEXT NOT NULL,
    scopes TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    last_used_at BIGINT,
    team TEXT REFERENCES teams(name),
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    capability_prefix TEXT NOT NULL DEFAULT '',
    revoked_at BIGINT,
    auth_generation BIGINT NOT NULL DEFAULT 0 CHECK(auth_generation >= 0),
    CHECK(team IS NOT NULL OR (account_id <> '' AND capability_prefix = ''))
);

CREATE TABLE session_membership_epochs (
    session_hash TEXT NOT NULL REFERENCES sessions(hash) ON DELETE CASCADE,
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    epoch BIGINT NOT NULL CHECK(epoch >= 0),
    PRIMARY KEY (session_hash, team)
);

CREATE TABLE run_control_requests (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    subject_kind TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    operation TEXT NOT NULL CHECK(operation IN ('pause','approval')),
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    result_json BLOB NOT NULL,
    PRIMARY KEY(team,run_id,subject_kind,subject_id,operation,request_key),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE
);

CREATE TABLE debug_pauses (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    paused_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    released_at BIGINT,
    released_by TEXT NOT NULL DEFAULT '',
    release_kind TEXT NOT NULL DEFAULT '',
    team TEXT NOT NULL REFERENCES teams(name),
    PRIMARY KEY (team, run_id, node_id, reason),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id,node_id) REFERENCES nodes(team,run_id,node_id) ON DELETE CASCADE
);

CREATE TABLE approvals (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    requested_at BIGINT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    timeout_ms BIGINT NOT NULL DEFAULT 0 CHECK(timeout_ms >= 0),
    on_timeout TEXT NOT NULL DEFAULT 'fail' CHECK(on_timeout IN ('fail','deny','approve')),
    approver TEXT NOT NULL DEFAULT '',
    resolved_at BIGINT,
    resolution TEXT NOT NULL DEFAULT '' CHECK(resolution IN ('','approved','denied','timed_out')),
    comment TEXT NOT NULL DEFAULT '',
    team TEXT NOT NULL REFERENCES teams(name),
    PRIMARY KEY (team, run_id, node_id),
    CHECK((resolved_at IS NOT NULL) = (resolution <> '')),
    FOREIGN KEY(team,run_id,node_id) REFERENCES nodes(team,run_id,node_id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE
);

CREATE TABLE cron_schedules (
    id TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    github_installation_id BIGINT NOT NULL DEFAULT 0,
    github_repository_id BIGINT NOT NULL DEFAULT 0,
    pipeline TEXT NOT NULL,
    schedule_name TEXT NOT NULL DEFAULT 'default',
    cron TEXT NOT NULL,
    tz TEXT NOT NULL,
    overlap TEXT NOT NULL CHECK(overlap IN ('skip','queue')),
    catch_up_ns BIGINT NOT NULL,
    where_ TEXT NOT NULL DEFAULT 'local' CHECK(where_ IN ('local','controller')),
    args TEXT NOT NULL DEFAULT '{}',
    locked_ref TEXT NOT NULL DEFAULT '',
    locked_binary TEXT NOT NULL DEFAULT '',
    locked_digest TEXT NOT NULL DEFAULT '',
    paused BIGINT NOT NULL DEFAULT 0,
    declared BIGINT NOT NULL DEFAULT 1,
    armed_at BIGINT NOT NULL,
    armed_by TEXT NOT NULL DEFAULT '',
    consent_json TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL,
    cursor_at BIGINT NOT NULL,
    last_fired_at BIGINT,
    last_run_id TEXT NOT NULL DEFAULT '',
    last_outcome TEXT NOT NULL DEFAULT '',
    next_due_at BIGINT,
    override_cron TEXT,
    override_tz TEXT,
    override_overlap TEXT,
    override_catch_up_ns BIGINT,
    override_args TEXT,
    override_base TEXT,
    override_set_at BIGINT,
    git_branch TEXT NOT NULL DEFAULT '',
    team TEXT NOT NULL REFERENCES teams(name),
    repo_key TEXT NOT NULL,
    PRIMARY KEY(team,id),
    CHECK(paused IN (0,1)),
    CHECK(declared IN (0,1)),
    CHECK(catch_up_ns >= 0),
    CHECK(override_catch_up_ns >= 0),
    CHECK(override_overlap IS NULL OR override_overlap IN ('skip','queue'))
);

CREATE TABLE cron_fires (
    id TEXT NOT NULL PRIMARY KEY,
    schedule_id TEXT NOT NULL,
    due_at BIGINT NOT NULL,
    decided_at BIGINT NOT NULL,
    outcome TEXT NOT NULL CHECK(outcome IN ('fired','skipped_overlap','missed','failed')),
    run_id TEXT,
    detail TEXT NOT NULL DEFAULT '',
    args TEXT NOT NULL DEFAULT '{}',
    team TEXT NOT NULL REFERENCES teams(name),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE
);

CREATE TABLE credit_grants (
    payment_mode TEXT NOT NULL DEFAULT 'test' CHECK(payment_mode IN ('test','live')),
    id TEXT NOT NULL PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('free','paid','reversal')),
    amount_micro BIGINT NOT NULL,
    reference TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    reverses TEXT NOT NULL DEFAULT '',
    team TEXT NOT NULL REFERENCES teams(name),
    grant_reference TEXT GENERATED ALWAYS AS (NULLIF(reference,'')) STORED,
    reversal_reference TEXT GENERATED ALWAYS AS (NULLIF(reverses,'')) STORED,
    reverses_kind TEXT NOT NULL DEFAULT 'paid' CHECK(reverses_kind IN ('free','paid')),
    UNIQUE(team,id),
    UNIQUE(team,payment_mode,kind,grant_reference),
    FOREIGN KEY(team,payment_mode,reverses_kind,reversal_reference) REFERENCES credit_grants(team,payment_mode,kind,grant_reference),
    CHECK((kind IN ('free','paid') AND amount_micro > 0 AND reverses = '')
       OR (kind = 'reversal' AND amount_micro < 0 AND reverses <> ''))
);

CREATE TABLE credit_charges (
    payment_mode TEXT NOT NULL DEFAULT 'test' CHECK(payment_mode IN ('test','live')),
    id TEXT NOT NULL PRIMARY KEY,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    capability_prefix TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'usage' CHECK(kind IN ('reservation','slice','usage','refund','storage')),
    seconds BIGINT NOT NULL,
    amount_micro BIGINT NOT NULL,
    principal TEXT NOT NULL DEFAULT '',
    storage_bytes BIGINT NOT NULL DEFAULT 0,
    cpu_class BIGINT NOT NULL DEFAULT 0,
    rate_micro_per_second BIGINT NOT NULL DEFAULT 0,
    charged_at BIGINT NOT NULL,
    team TEXT NOT NULL REFERENCES teams(name),
    CHECK((kind = 'refund' AND amount_micro <= 0 AND seconds <= 0) OR (kind <> 'refund' AND amount_micro >= 0 AND seconds >= 0))
);

CREATE TABLE storage_quotas (
    tier TEXT NOT NULL DEFAULT '',
    max_bytes_per_run BIGINT NOT NULL DEFAULT 0 CHECK(max_bytes_per_run >= 0),
    max_bytes_per_month BIGINT NOT NULL DEFAULT 0 CHECK(max_bytes_per_month >= 0),
    max_objects_per_run BIGINT NOT NULL DEFAULT 0 CHECK(max_objects_per_run >= 0),
    storage_allowance_bytes BIGINT NOT NULL DEFAULT 0 CHECK(storage_allowance_bytes >= 0),
    updated_at BIGINT NOT NULL,
    team TEXT NOT NULL REFERENCES teams(name),
    PRIMARY KEY(team)
);

CREATE TABLE storage_run_usage (
    run_id TEXT NOT NULL,
    bytes BIGINT NOT NULL DEFAULT 0 CHECK(bytes >= 0),
    objects BIGINT NOT NULL DEFAULT 0 CHECK(objects >= 0),
    updated_at BIGINT NOT NULL,
    team TEXT NOT NULL REFERENCES teams(name),
    period TEXT NOT NULL,
    replica_id TEXT NOT NULL,
    PRIMARY KEY(team,period,replica_id,run_id)
);

CREATE TABLE storage_month_usage (
    period TEXT NOT NULL,
    bytes BIGINT NOT NULL DEFAULT 0 CHECK(bytes >= 0),
    objects BIGINT NOT NULL DEFAULT 0 CHECK(objects >= 0),
    updated_at BIGINT NOT NULL,
    team TEXT NOT NULL REFERENCES teams(name),
    replica_id TEXT NOT NULL,
    PRIMARY KEY(team,period,replica_id)
);

CREATE TABLE egress_usage (
    period TEXT NOT NULL,
    bytes BIGINT NOT NULL DEFAULT 0 CHECK(bytes >= 0),
    updated_at BIGINT NOT NULL,
    team TEXT NOT NULL REFERENCES teams(name),
    replica_id TEXT NOT NULL,
    PRIMARY KEY(team,period,replica_id)
);

CREATE TABLE secrets (
    name TEXT NOT NULL,
    value TEXT NOT NULL,
    principal TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    masked BIGINT NOT NULL DEFAULT 1,
    pipeline TEXT NOT NULL DEFAULT '',
    shared BIGINT NOT NULL DEFAULT 0,
    team TEXT NOT NULL REFERENCES teams(name),
    only_on TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team, name, pipeline),
    CHECK(masked IN (0,1)),
    CHECK(shared IN (0,1)),
    CHECK(name <> '')
);

CREATE TABLE pipeline_profiles (
    pipeline TEXT NOT NULL,
    node_id TEXT NOT NULL,
    p50_duration_ms BIGINT NOT NULL,
    p99_duration_ms BIGINT NOT NULL,
    peak_cores DOUBLE PRECISION NOT NULL,
    peak_memory_bytes BIGINT NOT NULL,
    sample_count BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    samples_json BLOB,
    pinned_cores DOUBLE PRECISION NOT NULL DEFAULT 0,
    pinned_memory_bytes BIGINT NOT NULL DEFAULT 0,
    cpu_measured BIGINT NOT NULL DEFAULT 0,
    wait_samples_json BLOB,
    wait_p50_ms BIGINT NOT NULL DEFAULT 0,
    wait_p99_ms BIGINT NOT NULL DEFAULT 0,
    wait_sample_count BIGINT NOT NULL DEFAULT 0,
    contended_count BIGINT NOT NULL DEFAULT 0,
    prev_peak_memory_bytes BIGINT NOT NULL DEFAULT 0,
    plan_hash TEXT NOT NULL DEFAULT '',
    floor_cores DOUBLE PRECISION NOT NULL DEFAULT 0,
    floor_memory_bytes BIGINT NOT NULL DEFAULT 0,
    prev_peak_cores DOUBLE PRECISION NOT NULL DEFAULT 0,
    sustained_cores DOUBLE PRECISION NOT NULL DEFAULT 0,
    prev_sustained_cores DOUBLE PRECISION NOT NULL DEFAULT 0,
    team TEXT NOT NULL REFERENCES teams(name),
    PRIMARY KEY (team, pipeline, node_id),
    CHECK(cpu_measured IN (0,1)),
    CHECK(p50_duration_ms >= 0),
    CHECK(p99_duration_ms >= 0),
    CHECK(peak_cores >= 0),
    CHECK(peak_memory_bytes >= 0),
    CHECK(sample_count >= 0),
    CHECK(pinned_cores >= 0),
    CHECK(pinned_memory_bytes >= 0),
    CHECK(wait_p50_ms >= 0),
    CHECK(wait_p99_ms >= 0),
    CHECK(wait_sample_count >= 0),
    CHECK(contended_count >= 0),
    CHECK(prev_peak_memory_bytes >= 0),
    CHECK(floor_cores >= 0),
    CHECK(floor_memory_bytes >= 0),
    CHECK(prev_peak_cores >= 0),
    CHECK(sustained_cores >= 0),
    CHECK(prev_sustained_cores >= 0)
);

CREATE TABLE concurrency_entries (
    key TEXT NOT NULL CHECK(key <> ''),
    capacity BIGINT NOT NULL DEFAULT 1,
    previous_capacity BIGINT,
    last_write_run_id TEXT NOT NULL DEFAULT '',
    last_write_node_id TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL,
    team TEXT NOT NULL REFERENCES teams(name),
    PRIMARY KEY (team, key),
    CHECK(capacity >= 0),
    CHECK(previous_capacity >= 0)
);

CREATE TABLE concurrency_holders (
    key TEXT NOT NULL,
    holder_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL DEFAULT '',
    claimed_at BIGINT NOT NULL,
    queue_arrived_at BIGINT NOT NULL DEFAULT 0,
    lease_expires_at BIGINT NOT NULL,
    superseded BIGINT NOT NULL DEFAULT 0,
    cost BIGINT NOT NULL DEFAULT 1,
    declared_capacity BIGINT NOT NULL DEFAULT 0,
    team TEXT NOT NULL REFERENCES teams(name),
    generation BIGINT NOT NULL DEFAULT 0 CHECK(generation >= 0),
    PRIMARY KEY (team, key, holder_id),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    CHECK(superseded IN (0,1)),
    CHECK(cost >= 0),
    CHECK(declared_capacity >= 0)
);

CREATE TABLE concurrency_waiters (
    key TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL DEFAULT '',
    holder_id TEXT NOT NULL DEFAULT '',
    arrived_at BIGINT NOT NULL,
    policy TEXT NOT NULL CHECK(policy IN ('queue','skip','cancel_others')),
    cost BIGINT NOT NULL DEFAULT 1,
    declared_capacity BIGINT NOT NULL DEFAULT 0,
    team TEXT NOT NULL REFERENCES teams(name),
    generation BIGINT NOT NULL DEFAULT 0 CHECK(generation >= 0),
    lease_expires_at BIGINT NOT NULL,
    PRIMARY KEY (team, key, run_id, node_id),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    CHECK(cost >= 0),
    CHECK(declared_capacity >= 0)
);


CREATE TABLE identities (
    provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    email TEXT NOT NULL DEFAULT '',
    email_verified BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    linked BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, subject),
    CHECK(email_verified IN (0,1)),
    CHECK(linked IN (0,1)),
    CHECK(subject <> '')
);

CREATE TABLE overflow_controls (
    team TEXT NOT NULL REFERENCES teams(name),
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    monthly_minutes BIGINT NOT NULL CHECK (monthly_minutes >= 0),
    PRIMARY KEY (team,account_id)
);

CREATE TABLE memberships (
    team TEXT NOT NULL REFERENCES teams(name),
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'reader')),
    created_at BIGINT NOT NULL,
    PRIMARY KEY (team, account_id)
);

CREATE TABLE membership_epochs (
    team TEXT NOT NULL REFERENCES teams(name),
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL CHECK(epoch > 0),
    PRIMARY KEY (team, account_id)
);

CREATE TABLE invitations (
    id TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL REFERENCES teams(name),
    email TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'reader')),
    invited_by TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    accepted_at BIGINT,
    accepted_by TEXT NOT NULL DEFAULT '',
    withdrawn_at BIGINT,
    emailed_at BIGINT,
    CHECK((accepted_at IS NOT NULL) = (accepted_by <> '')),
    CHECK(accepted_at IS NULL OR withdrawn_at IS NULL),
    CHECK(expires_at >= created_at)
);

CREATE TABLE github_actions_exchanges (
    team TEXT NOT NULL REFERENCES teams(name),
    repository_key TEXT NOT NULL,
    run_id TEXT NOT NULL,
    run_attempt TEXT NOT NULL,
    PRIMARY KEY (team, repository_key, run_id, run_attempt)
);

CREATE TABLE github_runner_bindings (
    team TEXT NOT NULL REFERENCES teams(name),
    repository_id BIGINT NOT NULL,
    repository_owner_id BIGINT NOT NULL,
    repository TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    repo_key TEXT NOT NULL,
    PRIMARY KEY (team, repository_id)
);

CREATE TABLE github_app_installations (
    connection_generation TEXT NOT NULL DEFAULT '',
    installation_id BIGINT NOT NULL PRIMARY KEY CHECK(installation_id > 0),
    team TEXT NOT NULL REFERENCES teams(name),
    account_id BIGINT NOT NULL,
    account_login TEXT NOT NULL,
    account_type TEXT NOT NULL,
    suspended BIGINT NOT NULL DEFAULT 0 CHECK(suspended IN (0,1)),
    connected_by TEXT NOT NULL DEFAULT '',
    github_user_id BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    UNIQUE(team,installation_id)
);

CREATE TABLE github_app_triggers (
    team TEXT NOT NULL REFERENCES teams(name),
    repository_id BIGINT NOT NULL,
    pipeline TEXT NOT NULL,
    repository TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    on_push BIGINT NOT NULL DEFAULT 0,
    on_tags BIGINT NOT NULL DEFAULT 0,
    tag_patterns TEXT NOT NULL DEFAULT '[]',
    on_pull_request BIGINT NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    base_branches TEXT NOT NULL DEFAULT '[]',
    branches TEXT NOT NULL DEFAULT '[]',
    on_pr_closed BIGINT NOT NULL DEFAULT 0,
    on_pr_labeled BIGINT NOT NULL DEFAULT 0,
    pr_labels TEXT NOT NULL DEFAULT '[]',
    on_pr_ready_for_review BIGINT NOT NULL DEFAULT 0,
    on_release_published BIGINT NOT NULL DEFAULT 0,
    on_release_prereleased BIGINT NOT NULL DEFAULT 0,
    release_tag_patterns TEXT NOT NULL DEFAULT '[]',
    on_branch_create BIGINT NOT NULL DEFAULT 0,
    on_branch_delete BIGINT NOT NULL DEFAULT 0,
    runner TEXT NOT NULL DEFAULT 'anywhere',
    repo_key TEXT NOT NULL,
    PRIMARY KEY (team, repository_id, pipeline),
    FOREIGN KEY(team,installation_id) REFERENCES github_app_installations(team,installation_id) ON DELETE CASCADE,
    CHECK(on_push IN (0,1)),
    CHECK(on_tags IN (0,1)),
    CHECK(on_pull_request IN (0,1)),
    CHECK(on_pr_closed IN (0,1)),
    CHECK(on_pr_labeled IN (0,1)),
    CHECK(on_pr_ready_for_review IN (0,1)),
    CHECK(on_release_published IN (0,1)),
    CHECK(on_release_prereleased IN (0,1)),
    CHECK(on_branch_create IN (0,1)),
    CHECK(on_branch_delete IN (0,1))
);

CREATE TABLE github_app_deliveries (
    installation_id BIGINT NOT NULL DEFAULT 0,
    connection_generation TEXT NOT NULL DEFAULT '',
    queue_team TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL PRIMARY KEY,
    delivery_id TEXT NOT NULL UNIQUE,
    received_at BIGINT NOT NULL,
    event TEXT NOT NULL DEFAULT '',
    body BLOB,
    state TEXT NOT NULL DEFAULT 'done' CHECK(state IN ('pending','leased','done','failed')),
    lease_until BIGINT NOT NULL DEFAULT 0,
    lease BIGINT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    CHECK(state != 'done' OR body IS NULL),
    CHECK(state = 'done' OR body IS NOT NULL)
);

CREATE INDEX idx_github_app_deliveries_ready ON github_app_deliveries(state,received_at);
CREATE INDEX idx_github_app_deliveries_team ON github_app_deliveries(queue_team,received_at,digest) WHERE state!='done';

CREATE TABLE github_app_delivery_ids (
    delivery_id TEXT NOT NULL PRIMARY KEY,
    digest TEXT NOT NULL REFERENCES github_app_deliveries(digest) ON DELETE CASCADE
);

CREATE TABLE github_app_delivery_effects (
    digest TEXT NOT NULL REFERENCES github_app_deliveries(digest) ON DELETE CASCADE,
    effect TEXT NOT NULL CHECK(effect <> ''),
    result TEXT NOT NULL DEFAULT 'null',
    PRIMARY KEY (digest, effect)
);

CREATE TABLE github_app_publications (
    team TEXT NOT NULL REFERENCES teams(name),
    repository_id BIGINT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0 CHECK(generation >= 0),
    PRIMARY KEY(team,repository_id)
);

CREATE TABLE team_deletions (
    slug TEXT NOT NULL PRIMARY KEY,
    requested_by TEXT NOT NULL DEFAULT '',
    requested_at BIGINT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'done')),
    attempts BIGINT NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    finished_at BIGINT,
    recheck_at BIGINT,
    lease_holder TEXT NOT NULL DEFAULT '',
    lease_until BIGINT
);

CREATE TABLE invitation_email_log (
    address TEXT NOT NULL,
    sent_at BIGINT NOT NULL
);

CREATE TABLE free_slots (
    team TEXT NOT NULL REFERENCES teams(name) PRIMARY KEY,
    taken_at BIGINT NOT NULL,
    empty_since BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE signup_gate (
    id BIGINT NOT NULL PRIMARY KEY CHECK (id = 1),
    mode TEXT NOT NULL CHECK (mode IN ('open', 'waitlist')),
    source TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    set_by TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL,
    hourly_limit BIGINT NOT NULL,
    daily_limit BIGINT NOT NULL,
    hourly_warn BIGINT NOT NULL,
    github_min_account_days BIGINT NOT NULL,
    free_team_members BIGINT NOT NULL
);

CREATE TABLE signup_admissions (
    account_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('signup', 'invitation')),
    at BIGINT NOT NULL,
    PRIMARY KEY (account_id, kind, at)
);

CREATE TABLE credit_checkouts (
    id TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL REFERENCES teams(name),
    session_id TEXT NOT NULL,
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    payment_id TEXT NOT NULL DEFAULT '',
    currency TEXT NOT NULL,
    amount_minor BIGINT NOT NULL CHECK(amount_minor > 0),
    amount_micro BIGINT NOT NULL CHECK(amount_micro > 0),
    opened_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    paid_at BIGINT,
    closed_at BIGINT,
    grant_id TEXT UNIQUE,
    three_d_secure TEXT NOT NULL DEFAULT '',
    liability_shift INTEGER CHECK(liability_shift IN (0,1)),
    kind TEXT NOT NULL DEFAULT 'checkout' CHECK(kind IN ('checkout','refill')),
    customer_id TEXT NOT NULL DEFAULT '',
    payment_method TEXT NOT NULL DEFAULT '',
    receipt_email TEXT NOT NULL DEFAULT '',
    failure TEXT NOT NULL DEFAULT '',
    -- Foreign-payment warnings retain the refused payer's hold after its card is forgotten.
    warning_id TEXT NOT NULL DEFAULT '',
    refill_blocked INTEGER NOT NULL DEFAULT 0 CHECK(refill_blocked IN (0,1)),
    UNIQUE(team,id,payment_mode),
    UNIQUE(payment_mode,session_id),
    FOREIGN KEY(team,grant_id) REFERENCES credit_grants(team,id),
    CHECK((paid_at IS NULL) = (grant_id IS NULL)),
    CHECK(paid_at IS NULL OR (closed_at IS NOT NULL AND payment_id <> '')),
    CHECK(failure = '' OR (closed_at IS NOT NULL AND paid_at IS NULL)),
    CHECK(refill_blocked = 0 OR (kind = 'refill' AND failure <> '')),
    CHECK(kind = 'checkout' OR (session_id = id AND customer_id <> '' AND payment_method <> ''))
);

-- safety: provider-event deduplication and credit settlement survive operational run pruning.
CREATE TABLE payment_events (
    team TEXT REFERENCES teams(name),
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    event_id TEXT NOT NULL,
    checkout_id TEXT,
    payment_id TEXT NOT NULL DEFAULT '',
    event_kind TEXT NOT NULL CHECK(event_kind IN ('checkout_paid','checkout_expired','checkout_failed','card_setup','card_paid','card_failed','refund','warning','dispute_opened','dispute_closed','page_completed')),
    event_digest TEXT NOT NULL,
    grant_id TEXT,
    processed_at BIGINT NOT NULL,
    object_id TEXT NOT NULL DEFAULT '',
    event_json TEXT,
    settlement_json TEXT,
    PRIMARY KEY(payment_mode,event_id),
    FOREIGN KEY(team,checkout_id,payment_mode) REFERENCES credit_checkouts(team,id,payment_mode),
    FOREIGN KEY(team,grant_id) REFERENCES credit_grants(team,id),
    CHECK(team IS NOT NULL OR event_kind IN ('warning','refund','dispute_opened','dispute_closed','card_setup','card_paid','card_failed')),
    CHECK((event_kind IN ('checkout_paid','checkout_expired','checkout_failed') AND checkout_id IS NOT NULL AND team IS NOT NULL)
       OR (event_kind NOT IN ('checkout_paid','checkout_expired','checkout_failed') AND checkout_id IS NULL AND object_id <> '' AND event_json IS NOT NULL AND settlement_json IS NOT NULL))
);

CREATE INDEX idx_payment_events_object ON payment_events(payment_mode,object_id);

CREATE TABLE billing_pages (
    id TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL REFERENCES teams(name),
    account_id TEXT NOT NULL,
    request_key TEXT NOT NULL CHECK(request_key <> ''),
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    page_json BLOB NOT NULL,
    failed_at BIGINT,
    UNIQUE(team,account_id,request_key)
);

CREATE TABLE payment_correlations (
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    provider_id TEXT NOT NULL CHECK(provider_id <> ''),
    kind TEXT NOT NULL CHECK(kind IN ('setup','setup_session')),
    team TEXT NOT NULL REFERENCES teams(name),
    customer_id TEXT NOT NULL CHECK(customer_id <> ''),
    PRIMARY KEY(payment_mode,provider_id)
);

CREATE TABLE credit_freezes (
    payment_mode TEXT NOT NULL DEFAULT 'test' CHECK(payment_mode IN ('test','live')),
    dispute_id TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL REFERENCES teams(name),
    payment_id TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    released_at BIGINT,
    cause TEXT NOT NULL CHECK(cause IN ('operator','dispute','early_fraud_warning')),
    no_fault BIGINT NOT NULL DEFAULT 0 CHECK(no_fault IN (0,1)),
    placed_by TEXT NOT NULL DEFAULT '',
    CHECK(released_at IS NULL OR released_at >= created_at),
    CHECK(no_fault = 0 OR (cause = 'operator' AND released_at IS NOT NULL)),
    CHECK((cause = 'operator') = (placed_by <> ''))
);

CREATE TABLE standing_requests (
    id TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL REFERENCES teams(name),
    tier TEXT NOT NULL CHECK(tier IN ('High')),
    message TEXT NOT NULL,
    requested_by TEXT NOT NULL,
    requested_at BIGINT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('open','approved','declined')),
    decided_by TEXT NOT NULL DEFAULT '',
    decided_at BIGINT NOT NULL DEFAULT 0,
    decision_reason TEXT NOT NULL DEFAULT ''
);

CREATE TABLE team_storage (
    team TEXT NOT NULL REFERENCES teams(name),
    store TEXT NOT NULL,
    used_bytes BIGINT NOT NULL DEFAULT 0,
    reserved_bytes BIGINT NOT NULL DEFAULT 0,
    committed_bytes BIGINT NOT NULL DEFAULT 0,
    reconciled_at BIGINT NOT NULL DEFAULT 0,
    updated_at BIGINT NOT NULL,
    storage_charged_through BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (team, store)
);

CREATE TABLE team_download_day (
    team TEXT NOT NULL REFERENCES teams(name),
    period TEXT NOT NULL,
    bytes BIGINT NOT NULL DEFAULT 0 CHECK(bytes >= 0),
    updated_at BIGINT NOT NULL,
    replica_id TEXT NOT NULL,
    PRIMARY KEY(team,period,replica_id)
);

CREATE TABLE git_credentials (
    id TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL REFERENCES teams(name),
    host TEXT NOT NULL,
    repository_key TEXT NOT NULL DEFAULT '',
    host_wide BIGINT NOT NULL DEFAULT 0 CHECK(host_wide IN (0,1)),
    kind TEXT NOT NULL CHECK(kind IN ('ssh','https')),
    username TEXT NOT NULL DEFAULT '',
    secret TEXT NOT NULL,
    known_hosts TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    confirmed_at BIGINT,
    created_by TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    CHECK ((host_wide=0 AND repository_key<>'') OR (host_wide=1 AND repository_key=''))
);

CREATE TABLE github_app_extra_repos (
    team TEXT NOT NULL REFERENCES teams(name),
    repository_id BIGINT NOT NULL CHECK(repository_id > 0),
    repository TEXT NOT NULL,
    extra_repo TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    extra_repo_id BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (team, repository, extra_repo)
);

CREATE TABLE identity_unlinks (
    provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    account_id TEXT NOT NULL,
    unlinked_at BIGINT NOT NULL,
    PRIMARY KEY (provider, subject, account_id)
);

CREATE TABLE uploads (
    id TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    store TEXT NOT NULL,
    size BIGINT NOT NULL,
    provenance TEXT NOT NULL CHECK(provenance IN ('cloud','local')),
    expires_at BIGINT NOT NULL,
    committed_at BIGINT NOT NULL DEFAULT 0,
    abandoned_at BIGINT,
    staging_deleted_at BIGINT,
    uploader_kind TEXT NOT NULL CHECK(uploader_kind IN ('job','owner_unit','publication')),
    unit_kind TEXT CHECK(unit_kind IN ('trigger','plan','eval')),
    unit_id TEXT,
    unit_generation BIGINT,
    unit_source_json BLOB,
    digest TEXT NOT NULL,
    node_id TEXT,
    attempt BIGINT,
    generation BIGINT,
    uploader_principal TEXT NOT NULL,
    capability_prefix TEXT NOT NULL,
    repo_key TEXT NOT NULL,
    ref_scope TEXT NOT NULL,
    CHECK(
        (uploader_kind = 'job' AND node_id IS NOT NULL AND attempt IS NOT NULL AND generation IS NOT NULL AND unit_kind IS NULL AND unit_id IS NULL AND unit_generation IS NULL)
        OR (uploader_kind = 'owner_unit' AND node_id IS NULL AND attempt IS NULL AND generation IS NULL AND unit_kind IS NOT NULL AND unit_id IS NOT NULL AND unit_generation IS NOT NULL)
        OR (uploader_kind = 'publication' AND unit_kind IS NULL AND unit_id IS NULL AND unit_generation IS NULL AND ((node_id IS NULL AND attempt IS NULL AND generation IS NULL) OR (node_id IS NOT NULL AND attempt IS NOT NULL AND generation IS NOT NULL)))
    )
);

CREATE TABLE team_build_trust (
    team TEXT NOT NULL REFERENCES teams(name) PRIMARY KEY,
    trust_local_builds BIGINT NOT NULL DEFAULT 0 CHECK(trust_local_builds IN (0,1))
);

-- safety: account audit events precede team creation, so this operator table permits no team.
CREATE TABLE audit_events (
    id TEXT NOT NULL PRIMARY KEY,
    recorded_at BIGINT NOT NULL DEFAULT (DATABASE_TIMESTAMP),
    ts BIGINT NOT NULL,
    team TEXT,
    run_id TEXT NOT NULL DEFAULT '',
    account TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    subject_id TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL DEFAULT '',
    actor_is_account BIGINT NOT NULL DEFAULT 0 CHECK(actor_is_account IN (0,1)),
    subject_is_account BIGINT NOT NULL DEFAULT 0 CHECK(subject_is_account IN (0,1)),
    attrs TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE repos (
    team TEXT NOT NULL REFERENCES teams(name),
    repo_key TEXT NOT NULL,
    updated_at BIGINT NOT NULL,
    repo_display TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(team,repo_key)
);

CREATE TABLE card_refunds (
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    team TEXT NOT NULL REFERENCES teams(name),
    payment_intent TEXT NOT NULL,
    reason TEXT NOT NULL,
    amount_micro BIGINT NOT NULL DEFAULT 0 CHECK(amount_micro >= 0),
    refund_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'due' CHECK(status IN ('due','pending','succeeded','failed','canceled')),
    attempts BIGINT NOT NULL DEFAULT 0,
    next_attempt_at BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (payment_mode, team, payment_intent)
);


CREATE TABLE payment_warnings (
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    warning_id TEXT NOT NULL,
    payment_intent TEXT NOT NULL,
    fingerprint TEXT NOT NULL DEFAULT '',
    actionable BIGINT NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    PRIMARY KEY(payment_mode,warning_id)
);

CREATE TABLE payment_states (
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    payment_id TEXT NOT NULL CHECK(payment_id <> ''),
    status TEXT NOT NULL CHECK(status IN ('failed','succeeded')),
    outcome_json TEXT NOT NULL,
    PRIMARY KEY(payment_mode,payment_id)
);

CREATE TABLE payment_setups (
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    intent_id TEXT NOT NULL CHECK(intent_id <> ''),
    customer_id TEXT NOT NULL CHECK(customer_id <> ''),
    payment_method TEXT NOT NULL CHECK(payment_method <> ''),
    fingerprint TEXT NOT NULL CHECK(fingerprint <> ''),
    brand TEXT NOT NULL,
    last4 TEXT NOT NULL,
    created_at BIGINT NOT NULL CHECK(created_at > 0),
    applied BIGINT NOT NULL DEFAULT 0 CHECK(applied IN (0,1)),
    PRIMARY KEY(payment_mode,intent_id)
);

CREATE TABLE payment_refunds (
    payment_mode TEXT NOT NULL CHECK(payment_mode IN ('test','live')),
    kind TEXT NOT NULL DEFAULT 'refund' CHECK(kind IN ('refund','dispute','warning')),
    refund_id TEXT NOT NULL,
    payment_intent TEXT NOT NULL,
    amount_micro BIGINT NOT NULL CHECK(amount_micro > 0),
    status TEXT NOT NULL CHECK(status IN ('pending','requires_action','succeeded','failed','canceled')),
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (payment_mode, kind, refund_id)
);

CREATE TABLE console_refund_receipts (
    team TEXT NOT NULL REFERENCES teams(name),
    receipt_id TEXT NOT NULL CHECK(length(receipt_id)=64),
    request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
    result_json BLOB NOT NULL,
    PRIMARY KEY(team,receipt_id)
);

CREATE TABLE payment_cards (
    fingerprint TEXT NOT NULL PRIMARY KEY,
    first_team TEXT NOT NULL,
    payment_intent TEXT NOT NULL,
    pinned_at BIGINT NOT NULL
);

CREATE TABLE github_app_automation (
    team TEXT NOT NULL REFERENCES teams(name),
    repository_id BIGINT NOT NULL,
    repository TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    enabled_by TEXT NOT NULL,
    enabled_at BIGINT NOT NULL,
    runner TEXT NOT NULL DEFAULT 'anywhere',
    repo_key TEXT NOT NULL,
    PRIMARY KEY (team, repository_id),
    FOREIGN KEY (team,installation_id) REFERENCES github_app_installations(team,installation_id) ON DELETE CASCADE
);

CREATE TABLE log_streams (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    id TEXT NOT NULL,
    subject_kind TEXT NOT NULL CHECK(subject_kind IN ('job_attempt','owner_unit')),
    node_id TEXT,
    attempt BIGINT CHECK(attempt > 0),
    generation BIGINT NOT NULL CHECK(generation > 0),
    unit_kind TEXT CHECK(unit_kind IN ('trigger','plan','eval')),
    source_node_id TEXT,
    source_generation BIGINT CHECK(source_generation >= 0),
    prefix TEXT NOT NULL,
    retention_class TEXT NOT NULL,
    uploaded_through BIGINT NOT NULL DEFAULT 0 CHECK(uploaded_through >= 0),
    stored_bytes BIGINT NOT NULL DEFAULT 0 CHECK(stored_bytes >= 0),
    charged_bytes BIGINT NOT NULL DEFAULT 0 CHECK(charged_bytes >= 0),
    sealed_at BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    completeness TEXT NOT NULL DEFAULT 'open' CHECK(completeness IN ('open','complete','incomplete')),
    incomplete_reason TEXT NOT NULL DEFAULT '',
    final_commit_digest TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(team,run_id,id),
    UNIQUE(team,run_id,id,generation),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id,node_id,generation,attempt) REFERENCES node_attempts(team,run_id,node_id,generation,attempt) ON DELETE CASCADE,
    CHECK(
        (subject_kind = 'job_attempt' AND node_id IS NOT NULL AND attempt IS NOT NULL AND unit_kind IS NULL AND source_node_id IS NULL AND source_generation IS NULL)
        OR (subject_kind = 'owner_unit' AND node_id IS NULL AND attempt IS NULL AND unit_kind IS NOT NULL AND (
            (unit_kind IN ('trigger','plan') AND source_node_id IS NULL AND source_generation IS NULL)
            OR (unit_kind = 'eval' AND source_node_id IS NOT NULL AND source_generation IS NOT NULL)
        ))
    )
);

CREATE UNIQUE INDEX log_job_subject ON log_streams(team,run_id,node_id,attempt) WHERE subject_kind = 'job_attempt';
CREATE UNIQUE INDEX log_owner_subject ON log_streams(team,run_id,unit_kind,generation) WHERE unit_kind IN ('trigger','plan');
CREATE UNIQUE INDEX log_eval_subject ON log_streams(team,run_id,source_node_id,source_generation,generation) WHERE unit_kind = 'eval';

CREATE TABLE log_segments (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    stream_id TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK(generation > 0),
    n BIGINT NOT NULL CHECK(n >= 0),
    stored_bytes BIGINT NOT NULL,
    object_key TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    start_offset BIGINT NOT NULL CHECK(start_offset >= 0),
    end_offset BIGINT NOT NULL CHECK(end_offset >= start_offset),
    PRIMARY KEY(team,run_id,stream_id,n),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id,stream_id,generation) REFERENCES log_streams(team,run_id,id,generation) ON DELETE CASCADE,
    CHECK(stored_bytes >= 0)
);

CREATE TABLE run_effects (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    effect TEXT NOT NULL CHECK(effect IN ('github_check','metrics','profile_fold','notify_parent')),
    desired_state TEXT NOT NULL CHECK(desired_state IN ('pending','running','success','failed','cancelled')),
    desired_rev BIGINT NOT NULL CHECK(desired_rev > 0),
    delivered_rev BIGINT NOT NULL DEFAULT 0 CHECK(delivered_rev >= 0 AND delivered_rev <= desired_rev),
    attempts BIGINT NOT NULL DEFAULT 0,
    next_at BIGINT NOT NULL,
    delivered_at BIGINT,
    PRIMARY KEY(team,run_id,effect),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    CHECK(attempts >= 0)
);

CREATE TABLE eval_units (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    source_node_id TEXT NOT NULL,
    source_generation BIGINT NOT NULL CHECK(source_generation >= 0),
    status TEXT NOT NULL CHECK(status IN ('pending','claimed','running','done','failed','cancelled')),
    generation BIGINT NOT NULL DEFAULT 0 CHECK(generation >= 0),
    claimed_by TEXT,
    capability_prefix TEXT NOT NULL DEFAULT '',
    claimed_at BIGINT,
    lease_expires_at BIGINT,
    expires_at BIGINT,
    started_at BIGINT,
    finished_at BIGINT,
    owner_lease_name TEXT NOT NULL DEFAULT 'controller.owner',
    owner_generation BIGINT NOT NULL CHECK(owner_generation > 0),
    disruptions BIGINT NOT NULL DEFAULT 0 CHECK(disruptions BETWEEN 0 AND 3),
    PRIMARY KEY(team,run_id,source_node_id,source_generation),
    CHECK((status IN ('claimed','running')) = (claimed_by IS NOT NULL)),
    CHECK((claimed_by IS NULL AND capability_prefix = '' AND lease_expires_at IS NULL AND claimed_at IS NULL) OR (claimed_by IS NOT NULL AND claimed_by <> '' AND capability_prefix <> '' AND lease_expires_at IS NOT NULL AND claimed_at IS NOT NULL AND generation > 0)),
    CHECK((status IN ('done','failed','cancelled')) = (finished_at IS NOT NULL)),
    CHECK(status <> 'running' OR started_at IS NOT NULL),
    CHECK(status <> 'pending' OR started_at IS NULL),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id,source_node_id) REFERENCES nodes(team,run_id,node_id) ON DELETE CASCADE
);

-- safety: deployment service/operator authority has no tenant; awarded bindings always do.
-- safety: parent ancestry may cross teams for launcher awards; bindings still name a tenant run.
CREATE TABLE capabilities (
    digest TEXT NOT NULL PRIMARY KEY,
    here_proof_public_key TEXT NOT NULL DEFAULT '',
    prefix TEXT NOT NULL UNIQUE,
    team TEXT REFERENCES teams(name),
    subject_kind TEXT NOT NULL CHECK(subject_kind IN ('account','machine','service','operator','local','anonymous')),
    subject_id TEXT NOT NULL,
    audience TEXT NOT NULL CHECK(audience IN ('controller','nodehost')),
    grants BLOB NOT NULL,
    issuance TEXT NOT NULL CHECK(issuance IN ('delegate','award','exchange')),
    authority_version BIGINT NOT NULL CHECK(authority_version = 1),
    authority_json BLOB NOT NULL,
    parent_kind TEXT CHECK(parent_kind IN ('capability','session')),
    parent_id TEXT,
    award_purpose TEXT CHECK(award_purpose IN ('trigger','plan','work','eval','here')),
    delivery_purpose TEXT CHECK(delivery_purpose IN ('direct','bootstrap')),
    unit_id TEXT,
    billing_unit TEXT CHECK(billing_unit IN ('host','job','plan','eval')),
    bind_kind TEXT NOT NULL CHECK(bind_kind IN ('none','trigger','node','push','run')),
    bind_run TEXT,
    bind_node TEXT,
    bind_generation BIGINT,
    bind_holder TEXT,
    claim_by BIGINT,
    reserved_pools BLOB NOT NULL,
    advertised_labels_json BLOB,
    claim_observation_json BLOB,
    created_at BIGINT NOT NULL,
    expires_at BIGINT,
    revoked_at BIGINT,
    last_used_at BIGINT,
    created_by TEXT NOT NULL,
    metered BIGINT NOT NULL DEFAULT 0 CHECK(metered IN (0,1)),
    result_digest TEXT NOT NULL DEFAULT '',
    payer_team TEXT REFERENCES teams(name),
    executor_class TEXT,
    reservation_id TEXT,
    billing_start TEXT CHECK(billing_start IN ('at_award','at_start')),
    unit_kind TEXT CHECK(unit_kind IN ('trigger','plan','eval','node')),
    eval_unit_generation BIGINT,
    repo_key TEXT NOT NULL DEFAULT '',
    repo_scope BLOB,
    ref_scope TEXT NOT NULL DEFAULT '',
    push_sha TEXT NOT NULL DEFAULT '',
    revocation_generation BIGINT NOT NULL DEFAULT 0 CHECK(revocation_generation >= 0),
    UNIQUE(team,prefix),
    CHECK((parent_kind IS NULL) = (parent_id IS NULL)),
    CHECK((unit_kind IS NULL) = (unit_id IS NULL)),
    CHECK(unit_id IS NULL OR unit_id <> ''),
    CHECK(award_purpose IS NULL OR unit_kind IS NOT NULL),
    CHECK(delivery_purpose IS NULL OR unit_kind IS NOT NULL),
    CHECK(billing_unit IS NULL OR unit_kind IS NOT NULL),
    CHECK(parent_id IS NULL OR parent_id <> ''),
    CHECK(issuance = 'delegate' OR (parent_kind IS NOT NULL AND unit_id IS NOT NULL AND award_purpose IS NOT NULL AND delivery_purpose IS NOT NULL)),
    CHECK(delivery_purpose IS NULL OR delivery_purpose <> 'bootstrap' OR unit_kind IN ('plan','eval','node')),
    CHECK(award_purpose IS NULL OR (award_purpose = 'work' AND unit_kind = 'node') OR (award_purpose = 'here' AND unit_kind = 'trigger') OR (award_purpose IN ('trigger','plan','eval') AND award_purpose = unit_kind)),
    CHECK(billing_unit IS NULL OR (unit_kind = 'trigger' AND billing_unit = 'host') OR (unit_kind = 'node' AND billing_unit = 'job') OR (unit_kind IN ('plan','eval') AND billing_unit = unit_kind)),
    CHECK(team IS NOT NULL OR (subject_kind IN ('service','operator') AND bind_kind = 'none')),
    CHECK(issuance = 'delegate' OR team IS NOT NULL),
    CHECK(issuance = 'delegate' OR (payer_team IS NOT NULL AND executor_class IS NOT NULL AND reservation_id IS NOT NULL AND billing_start IS NOT NULL AND unit_kind IS NOT NULL AND billing_unit IS NOT NULL)),
    FOREIGN KEY(team,bind_run) REFERENCES runs(team,id) ON DELETE CASCADE
);

CREATE TABLE blobs (
    team TEXT NOT NULL REFERENCES teams(name),
    digest TEXT NOT NULL,
    size BIGINT NOT NULL CHECK(size >= 0),
    created_at BIGINT NOT NULL,
    PRIMARY KEY(team,digest)
);

-- safety: reusable cache records use NULL run_id; pending memos cascade with their producer.
-- safety: source_run_id is immutable provenance; output ownership uses the cascading run_id.
CREATE TABLE blob_records (
    team TEXT NOT NULL REFERENCES teams(name),
    kind TEXT NOT NULL CHECK(kind IN ('output','manifest','memo','depcache','binary','source_bundle')),
    scope TEXT NOT NULL,
    name TEXT NOT NULL,
    digest TEXT NOT NULL,
    manifest_digest TEXT,
    run_id TEXT,
    node_id TEXT,
    attempt BIGINT,
    generation BIGINT,
    repo_key TEXT NOT NULL,
    ref_scope TEXT NOT NULL,
    uploader_principal TEXT NOT NULL,
    provenance TEXT NOT NULL CHECK(provenance IN ('cloud','local')),
    source_identity_json BLOB NOT NULL,
    source_kind TEXT NOT NULL CHECK(source_kind IN ('job','owner_unit','publication','submission')),
    source_run_id TEXT NOT NULL,
    source_node_id TEXT,
    source_attempt BIGINT,
    source_generation BIGINT,
    verified_ref BIGINT NOT NULL CHECK(verified_ref IN (0,1)),
    verified_default_branch BIGINT NOT NULL CHECK(verified_default_branch IN (0,1)),
    source_sha TEXT NOT NULL DEFAULT '',
    ref_facts_json BLOB NOT NULL,
    pipeline TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    expires_at BIGINT,
    PRIMARY KEY(team,kind,scope,name),
    FOREIGN KEY(team,digest) REFERENCES blobs(team,digest),
    FOREIGN KEY(team,manifest_digest) REFERENCES blobs(team,digest),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    CHECK(verified_default_branch = 0 OR verified_ref = 1)
);

CREATE TABLE blob_members (
    team TEXT NOT NULL REFERENCES teams(name),
    manifest_digest TEXT NOT NULL,
    member_digest TEXT NOT NULL,
    PRIMARY KEY(team,manifest_digest,member_digest),
    FOREIGN KEY(team,manifest_digest) REFERENCES blobs(team,digest) ON DELETE CASCADE,
    FOREIGN KEY(team,member_digest) REFERENCES blobs(team,digest)
);

CREATE TABLE blob_receipts (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT,
    attempt BIGINT CHECK(attempt > 0),
    generation BIGINT CHECK(generation > 0),
    uploader_kind TEXT NOT NULL CHECK(uploader_kind IN ('job','owner_unit','publication')),
    unit_kind TEXT CHECK(unit_kind IN ('trigger','plan','eval')),
    unit_id TEXT,
    unit_generation BIGINT CHECK(unit_generation > 0),
    unit_source_json BLOB,
    digest TEXT NOT NULL,
    uploader_principal TEXT NOT NULL,
    capability_prefix TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY(team,id),
    FOREIGN KEY(team,run_id,node_id) REFERENCES nodes(team,run_id,node_id) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id,node_id,generation,attempt) REFERENCES node_attempts(team,run_id,node_id,generation,attempt) ON DELETE CASCADE,
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE,
    FOREIGN KEY(team,digest) REFERENCES blobs(team,digest) ON DELETE CASCADE,
    CHECK(
        (uploader_kind = 'job' AND node_id IS NOT NULL AND attempt IS NOT NULL AND generation IS NOT NULL AND unit_kind IS NULL AND unit_id IS NULL AND unit_generation IS NULL)
        OR (uploader_kind = 'owner_unit' AND node_id IS NULL AND attempt IS NULL AND generation IS NULL AND unit_kind IS NOT NULL AND unit_id IS NOT NULL AND unit_generation IS NOT NULL)
        OR (uploader_kind = 'publication' AND unit_kind IS NULL AND unit_id IS NULL AND unit_generation IS NULL AND ((node_id IS NULL AND attempt IS NULL AND generation IS NULL) OR (node_id IS NOT NULL AND attempt IS NOT NULL AND generation IS NOT NULL)))
    )
);

CREATE TABLE blob_tombstones (
    team TEXT NOT NULL REFERENCES teams(name),
    digest TEXT NOT NULL,
    marked_at BIGINT NOT NULL,
    collecting_at BIGINT,
    PRIMARY KEY(team,digest),
    FOREIGN KEY(team,digest) REFERENCES blobs(team,digest) ON DELETE CASCADE
);

-- safety: RESTRICT protects the newest successful run until its replacement pin commits.
CREATE TABLE pipeline_success_pins (
    team TEXT NOT NULL REFERENCES teams(name),
    pipeline TEXT NOT NULL,
    run_id TEXT NOT NULL,
    finished_at BIGINT NOT NULL,
    PRIMARY KEY(team,pipeline),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id)
);

CREATE TABLE pool_access_rules (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL,
    pool TEXT NOT NULL,
    repo_key TEXT NOT NULL,
    branch TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    route TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY(team,id),
    CHECK(repo_key NOT LIKE 'local:%')
);

CREATE TABLE settings (
    key TEXT NOT NULL PRIMARY KEY,
    value BLOB NOT NULL,
    sealed BIGINT NOT NULL DEFAULT 0 CHECK(sealed IN (0,1)),
    CHECK(key <> 'session_csrf_key' OR sealed = 1)
);

CREATE TABLE leases (
    name TEXT NOT NULL PRIMARY KEY,
    holder TEXT NOT NULL,
    until BIGINT NOT NULL,
    last_run_at BIGINT,
    generation BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE flow_nonces (
    kind TEXT NOT NULL CHECK(kind IN ('github_app_connect','identity_link')),
    nonce TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    binding TEXT NOT NULL DEFAULT '',
    completed_at BIGINT,
    PRIMARY KEY(kind,nonce)
);

CREATE TABLE team_counters (
    team TEXT NOT NULL REFERENCES teams(name) PRIMARY KEY,
    concurrent_nodes BIGINT NOT NULL DEFAULT 0 CHECK(concurrent_nodes >= 0),
    registered_agents BIGINT NOT NULL DEFAULT 0 CHECK(registered_agents >= 0),
    auth_generation BIGINT NOT NULL DEFAULT 0 CHECK(auth_generation >= 0)
);

-- safety: one locked team/quota row serializes admission; summed usage rows never admit work.
CREATE TABLE quota_admission (
    team TEXT NOT NULL REFERENCES teams(name),
    quota TEXT NOT NULL,
    period TEXT NOT NULL,
    used BIGINT NOT NULL DEFAULT 0 CHECK(used >= 0),
    reserved BIGINT NOT NULL DEFAULT 0 CHECK(reserved >= 0),
    updated_at BIGINT NOT NULL,
    PRIMARY KEY(team,quota)
);

CREATE TABLE run_rate_usage (
    team TEXT NOT NULL REFERENCES teams(name),
    period TEXT NOT NULL,
    replica_id TEXT NOT NULL,
    principal TEXT NOT NULL,
    runs BIGINT NOT NULL DEFAULT 0 CHECK(runs >= 0),
    updated_at BIGINT NOT NULL,
    PRIMARY KEY(team,period,replica_id,principal)
);

CREATE TABLE quota_reservations (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL,
    quota TEXT NOT NULL,
    period TEXT NOT NULL,
    amount BIGINT NOT NULL CHECK(amount >= 0),
    expires_at BIGINT NOT NULL,
    used BIGINT NOT NULL DEFAULT 0 CHECK(used >= 0 AND used <= amount),
    state TEXT NOT NULL CHECK(state IN ('held','committed','released')),
    PRIMARY KEY(team,id),
    FOREIGN KEY(team,quota) REFERENCES quota_admission(team,quota)
);

-- safety: settlement cannot depend on capabilities or runs surviving retention.
CREATE TABLE billing_awards (
    team TEXT NOT NULL REFERENCES teams(name),
    reservation_id TEXT NOT NULL,
    capability_prefix TEXT NOT NULL,
    run_id TEXT NOT NULL,
    requester_account TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    generation BIGINT NOT NULL,
    payer_team TEXT NOT NULL REFERENCES teams(name),
    metered BIGINT NOT NULL CHECK(metered IN (0,1)),
    executor_class TEXT NOT NULL,
    billing_start TEXT NOT NULL CHECK(billing_start IN ('at_award','at_start')),
    unit_kind TEXT NOT NULL CHECK(unit_kind IN ('trigger','plan','eval','node')),
    billing_unit TEXT NOT NULL CHECK(billing_unit IN ('host','job','plan','eval')),
    awarded_at BIGINT NOT NULL,
    settled_at BIGINT,
    stop_reason TEXT NOT NULL DEFAULT '',
    drain_deadline_at BIGINT,
    CHECK((stop_reason = '') = (drain_deadline_at IS NULL)),
    CHECK((unit_kind = 'trigger' AND billing_unit = 'host') OR (unit_kind = 'node' AND billing_unit = 'job') OR (unit_kind IN ('plan','eval') AND billing_unit = unit_kind)),
    PRIMARY KEY(team,reservation_id)
);

CREATE UNIQUE INDEX idx_billing_awards_capability ON billing_awards(team,capability_prefix);
CREATE INDEX idx_billing_awards_requester ON billing_awards(team,requester_account);

CREATE UNIQUE INDEX idx_billing_awards_unit_generation ON billing_awards(team,run_id,node_id,unit_kind,generation);

CREATE TABLE billable_intervals (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL PRIMARY KEY,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL DEFAULT '',
    attempt BIGINT NOT NULL DEFAULT 0,
    generation BIGINT NOT NULL,
    capability_prefix TEXT NOT NULL,
    reservation_id TEXT NOT NULL,
    payer_team TEXT NOT NULL REFERENCES teams(name),
    metered BIGINT NOT NULL CHECK(metered IN (0,1)),
    executor_class TEXT NOT NULL,
    unit_kind TEXT NOT NULL CHECK(unit_kind IN ('trigger','plan','eval','node')),
    billing_unit TEXT NOT NULL CHECK(billing_unit IN ('host','job','plan','eval')),
    billing_start TEXT NOT NULL CHECK(billing_start IN ('at_award','at_start')),
    started_at BIGINT NOT NULL,
    finished_at BIGINT,
    charged_through BIGINT NOT NULL,
    CHECK((unit_kind = 'trigger' AND billing_unit = 'host') OR (unit_kind = 'node' AND billing_unit = 'job') OR (unit_kind IN ('plan','eval') AND billing_unit = unit_kind)),
    FOREIGN KEY(team,reservation_id) REFERENCES billing_awards(team,reservation_id),
    CHECK(finished_at IS NULL OR finished_at >= started_at),
    CHECK(charged_through >= started_at AND (finished_at IS NULL OR charged_through <= finished_at))
);

CREATE INDEX idx_runs_started ON runs(started_at DESC);

CREATE INDEX idx_runs_pipeline ON runs(team,pipeline, started_at DESC);

CREATE INDEX idx_runs_sha_started ON runs(team,git_sha, started_at DESC);

CREATE INDEX idx_runs_branch_started ON runs(team,git_branch, started_at DESC);

CREATE INDEX idx_claim_requirement_labels ON claim_requirement_terms(label,team,requirements,term);

CREATE INDEX idx_nodes_claim_requirements ON nodes(team,claim_requirements,ready_at,run_id,node_id) WHERE status='pending' AND ready_at IS NOT NULL AND claimed_by IS NULL AND capability_prefix='' AND cancel_requested_at IS NULL;

CREATE INDEX idx_triggers_claim_requirements ON triggers(team,claim_requirements,unit_kind,available_at,id) WHERE status='pending' AND claimed_by IS NULL AND capability_prefix='' AND cancel_requested_at IS NULL;

CREATE INDEX idx_nodes_claim_window ON nodes(ready_at,team) WHERE status='pending' AND ready_at IS NOT NULL AND claimed_by IS NULL AND capability_prefix='' AND cancel_requested_at IS NULL;

CREATE INDEX idx_triggers_claim_window ON triggers(available_at,team) WHERE status='pending' AND claimed_by IS NULL AND capability_prefix='' AND cancel_requested_at IS NULL;

CREATE INDEX idx_nodes_eval_window ON nodes(finished_at,team,run_id,node_id) WHERE finished_at IS NOT NULL AND outcome IN ('success','memoized') AND expansion='expected';

CREATE INDEX idx_nodes_team_eval_window ON nodes(team,finished_at,run_id,node_id) WHERE finished_at IS NOT NULL AND outcome IN ('success','memoized') AND expansion='expected';

CREATE INDEX idx_nodes_claimable ON nodes(team,ready_at) WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND status != 'done';

CREATE INDEX idx_nodes_queued ON nodes(team,queue_started_at) WHERE queue_started_at IS NOT NULL AND claimed_by IS NULL AND status = 'pending';

CREATE INDEX idx_nodes_claimed_lease ON nodes(team,lease_expires_at) WHERE claimed_by IS NOT NULL;

CREATE INDEX idx_triggers_claimed_lease ON triggers(team,status, lease_expires_at) WHERE status = 'claimed';

CREATE INDEX idx_sessions_expires ON sessions(team,expires_at);

CREATE INDEX idx_debug_pauses_open ON debug_pauses(team,run_id) WHERE released_at IS NULL;

CREATE INDEX idx_approvals_pending ON approvals(team,requested_at) WHERE resolved_at IS NULL;

CREATE INDEX idx_triggers_pending ON triggers(team,status, available_at, created_at) WHERE status = 'pending';

CREATE INDEX idx_cron_fires_schedule_decided ON cron_fires(team,schedule_id, decided_at DESC);

CREATE INDEX idx_credit_grants_created ON credit_grants(team,created_at);

CREATE INDEX idx_credit_charges_charged ON credit_charges(team,charged_at);

CREATE INDEX idx_credit_charges_node ON credit_charges(team,run_id, node_id);

CREATE INDEX idx_events_ts ON events(team,ts);

CREATE INDEX idx_node_metrics_ts ON node_metrics(team,ts);

CREATE INDEX idx_nodes_outstanding ON nodes(team,status, ready_at, claimed_by) WHERE status != 'done';

CREATE INDEX idx_credit_grants_kind_amount ON credit_grants(team,kind, amount_micro);

CREATE INDEX idx_credit_charges_kind_amount ON credit_charges(team,kind, amount_micro, seconds);

CREATE INDEX idx_runs_created ON runs(team,created_at);

CREATE INDEX idx_runs_principal_created ON runs(team,created_principal, created_at);

CREATE INDEX idx_egress_usage_month ON egress_usage(team,period);

CREATE INDEX idx_credit_grants_kind_created ON credit_grants(team,kind, created_at);

CREATE INDEX idx_runs_team_started ON runs(team, started_at DESC);

CREATE INDEX idx_concurrency_holders_key_claimed ON concurrency_holders(team, key, claimed_at);

CREATE INDEX idx_concurrency_holders_lease ON concurrency_holders(team,lease_expires_at);

CREATE INDEX idx_concurrency_waiters_arrived ON concurrency_waiters(team, key, arrived_at);

CREATE UNIQUE INDEX idx_accounts_verified_email ON accounts(email) WHERE email_verified = 1;

CREATE INDEX idx_identities_account ON identities(account_id);

CREATE INDEX idx_memberships_account ON memberships(team,account_id);

CREATE UNIQUE INDEX idx_invitations_open ON invitations(team, email) WHERE accepted_at IS NULL AND withdrawn_at IS NULL;

CREATE INDEX idx_invitations_email ON invitations(team,email);

CREATE UNIQUE INDEX idx_cron_schedules_repo_pipeline_name ON cron_schedules(team, repo_path, pipeline, schedule_name);

CREATE UNIQUE INDEX idx_triggers_webhook_replay_key ON triggers(team, webhook_replay_key) WHERE webhook_replay_key != '';


CREATE INDEX idx_github_app_installations_team ON github_app_installations(team);

CREATE INDEX idx_github_app_triggers_installation ON github_app_triggers(team,installation_id, repository_id);

CREATE INDEX idx_team_deletions_state ON team_deletions(state);

CREATE INDEX idx_invitation_email_log_address ON invitation_email_log(address, sent_at);

CREATE INDEX idx_triggers_team_created ON triggers(team, created_at);

CREATE INDEX idx_signup_admissions_at ON signup_admissions(at);

CREATE INDEX idx_accounts_created ON accounts(created_at);

CREATE INDEX idx_accounts_waitlisted ON accounts(waitlisted_at) WHERE waitlisted_at <> 0;

CREATE UNIQUE INDEX idx_credit_checkout_payment ON credit_checkouts(payment_mode,payment_id) WHERE payment_id <> '';

CREATE INDEX idx_credit_checkouts_team_expires ON credit_checkouts(team, expires_at);

CREATE INDEX idx_credit_checkouts_session ON credit_checkouts(team,session_id);

CREATE INDEX idx_credit_freezes_team ON credit_freezes(team, released_at);

CREATE UNIQUE INDEX idx_standing_requests_open ON standing_requests(team) WHERE state = 'open';

CREATE INDEX idx_standing_requests_team ON standing_requests(team, requested_at);

CREATE UNIQUE INDEX idx_git_credentials_team_repo ON git_credentials(team, repository_key) WHERE host_wide=0;
CREATE UNIQUE INDEX idx_git_credentials_team_host ON git_credentials(team, host) WHERE host_wide=1;

CREATE INDEX idx_identity_unlinks_account ON identity_unlinks(account_id);

CREATE INDEX idx_uploads_expiry ON uploads(team,expires_at);
CREATE INDEX idx_uploads_staging ON uploads(expires_at) WHERE staging_deleted_at IS NULL;

CREATE INDEX idx_cron_schedules_github_identity ON cron_schedules(team, github_installation_id, github_repository_id);

CREATE INDEX idx_credit_charges_team_amount ON credit_charges(team, amount_micro);


CREATE INDEX idx_audit_events_retention ON audit_events(recorded_at,id);

CREATE INDEX idx_audit_events_team_ts ON audit_events(team, ts, id);

CREATE INDEX idx_audit_events_kind_ts ON audit_events(team,kind, ts, id);

CREATE INDEX idx_triggers_team_pending ON triggers(team, status) WHERE status = 'pending';




CREATE INDEX idx_payment_warnings_intent ON payment_warnings(payment_intent);
CREATE INDEX idx_payment_refunds_intent ON payment_refunds(payment_intent);

CREATE INDEX idx_payment_warnings_card ON payment_warnings(fingerprint);

CREATE INDEX idx_github_app_deliveries_received ON github_app_deliveries(received_at);

CREATE INDEX idx_github_app_automation_installation ON github_app_automation(team,installation_id);

CREATE INDEX idx_log_streams_unsealed ON log_streams(team,updated_at) WHERE sealed_at IS NULL;

CREATE INDEX idx_runs_repo_started ON runs(team,repo_key,started_at DESC);

CREATE INDEX idx_runs_repo_sha ON runs(team,repo_key,git_sha);

CREATE UNIQUE INDEX idx_events_state_seq ON events(team,run_id,node_id,state_seq) WHERE state_seq > 0;

CREATE INDEX idx_capabilities_parent ON capabilities(parent_kind,parent_id);

CREATE INDEX idx_capabilities_team_expiry ON capabilities(team,expires_at);

CREATE INDEX idx_capabilities_binding ON capabilities(team,bind_run,bind_node,bind_generation);

CREATE INDEX idx_run_effects_due ON run_effects(team,next_at) WHERE delivered_rev < desired_rev;

CREATE INDEX idx_blob_records_expiry ON blob_records(team,expires_at);

CREATE INDEX idx_blob_records_digest ON blob_records(team,digest);

CREATE INDEX idx_blob_records_manifest ON blob_records(team,manifest_digest);

CREATE INDEX idx_blob_records_repo_ref ON blob_records(team,repo_key,ref_scope,kind);

CREATE INDEX idx_blob_members_member ON blob_members(team,member_digest);

CREATE INDEX idx_blob_tombstones_age ON blob_tombstones(team,marked_at);

CREATE INDEX idx_pool_access_repo ON pool_access_rules(team,repo_key,branch);

CREATE INDEX idx_concurrency_waiters_expiry ON concurrency_waiters(team,lease_expires_at);

CREATE INDEX idx_billable_intervals_reservation ON billable_intervals(team,reservation_id);

CREATE INDEX idx_billable_intervals_open ON billable_intervals(team,capability_prefix) WHERE finished_at IS NULL;

CREATE INDEX idx_flow_nonces_expiry ON flow_nonces(expires_at);

CREATE INDEX idx_eval_units_claimable ON eval_units(team,status) WHERE status = 'pending';

CREATE UNIQUE INDEX idx_runs_idempotency_key ON runs(team,idempotency_scope,idempotency_key) WHERE idempotency_key <> '';

CREATE INDEX idx_quota_reservations_due ON quota_reservations(team,expires_at) WHERE state = 'held';

CREATE UNIQUE INDEX idx_blob_receipts_attempt ON blob_receipts(team,run_id,node_id,attempt,generation,digest) WHERE node_id IS NOT NULL;

CREATE UNIQUE INDEX idx_blob_receipts_owner ON blob_receipts(team,run_id,unit_kind,unit_id,unit_generation,digest) WHERE uploader_kind = 'owner_unit';

CREATE UNIQUE INDEX idx_blob_receipts_publication ON blob_receipts(team,run_id,digest) WHERE uploader_kind = 'publication' AND node_id IS NULL;

CREATE TABLE source_uploads (
    team TEXT NOT NULL REFERENCES teams(name),
    id TEXT NOT NULL,
    authority_kind TEXT NOT NULL,
    authority_id TEXT NOT NULL,
    repo_key TEXT NOT NULL,
    replica_id TEXT NOT NULL DEFAULT '',
    period TEXT NOT NULL DEFAULT '',
    repo_display TEXT NOT NULL,
    execution_sha TEXT NOT NULL,
    pipeline_sha TEXT NOT NULL,
    charged_objects BIGINT NOT NULL DEFAULT 0 CHECK(charged_objects IN (0,1)),
    digest TEXT NOT NULL,
    size BIGINT NOT NULL CHECK(size>=0),
    expires_at BIGINT NOT NULL,
    committed_at BIGINT NOT NULL DEFAULT 0,
    bound_run_id TEXT,
    PRIMARY KEY(team,id),
    FOREIGN KEY(team,bound_run_id) REFERENCES runs(team,id) ON DELETE CASCADE
);

CREATE INDEX idx_source_uploads_expiry ON source_uploads(expires_at,team,id);
CREATE UNIQUE INDEX idx_credit_charges_one_reservation ON credit_charges(team,capability_prefix) WHERE kind = 'reservation';
CREATE UNIQUE INDEX idx_credit_checkouts_one_open_refill ON credit_checkouts(team,payment_mode) WHERE kind = 'refill' AND closed_at IS NULL;

CREATE TABLE admission_work (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    arrived_at BIGINT NOT NULL,
    lease_expires_at BIGINT NOT NULL,
    granted INTEGER NOT NULL DEFAULT 0 CHECK(granted IN (0,1)),
    generation BIGINT NOT NULL DEFAULT 0,
    holder TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(team,run_id,node_id),
    UNIQUE(team,arrived_at),
    CHECK(arrived_at>0 AND lease_expires_at>0),
    CHECK(node_id='' OR (generation>0 AND holder<>'')),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE
);

CREATE INDEX idx_nodes_claim_context ON nodes(team,claim_requirements,claim_context,ready_at,run_id,node_id) WHERE status='pending' AND ready_at IS NOT NULL AND claimed_by IS NULL AND capability_prefix='' AND cancel_requested_at IS NULL;
CREATE INDEX idx_triggers_claim_context ON triggers(team,claim_requirements,claim_context,unit_kind,available_at,id) WHERE status='pending' AND claimed_by IS NULL AND capability_prefix='' AND cancel_requested_at IS NULL;
