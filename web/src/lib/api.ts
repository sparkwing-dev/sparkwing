function getApiUrl(): string {
  if (typeof window !== "undefined") return "";
  return process.env.SPARKWING_CONTROLLER_URL || "";
}

const API_URL = getApiUrl();

function sessionMode(): boolean {
  if (typeof window === "undefined") return false;
  const runtime = window as unknown as Record<string, unknown>;
  return runtime.__SPARKWING_REQUIRE_LOGIN__ === "true";
}

function getSessionCSRFHeaders(method: string | undefined): HeadersInit {
  if (typeof window === "undefined" || typeof document === "undefined") {
    return {};
  }
  if (!sessionMode()) return {};
  switch ((method || "GET").toUpperCase()) {
    case "GET":
    case "HEAD":
    case "OPTIONS":
    case "TRACE":
      return {};
  }
  const cookie = document.cookie
    .split(";")
    .map((value) => value.trim())
    .find((value) => value.startsWith("sw_csrf="));
  if (!cookie) return {};
  const token = cookie.slice("sw_csrf=".length);
  try {
    return { "X-CSRF-Token": decodeURIComponent(token) };
  } catch {
    return {};
  }
}

export type ConnectionStatus =
  "ok" | "unreachable" | "unauthorized" | "session-expired";
type StatusListener = (status: ConnectionStatus) => void;

let _connectionStatus: ConnectionStatus = "ok";
const _statusListeners: StatusListener[] = [];

export function getConnectionStatus(): ConnectionStatus {
  return _connectionStatus;
}

export function onConnectionStatusChange(fn: StatusListener): () => void {
  _statusListeners.push(fn);
  return () => {
    const i = _statusListeners.indexOf(fn);
    if (i >= 0) _statusListeners.splice(i, 1);
  };
}

function setConnectionStatus(s: ConnectionStatus) {
  if (s === _connectionStatus) return;
  _connectionStatus = s;
  for (const fn of _statusListeners) fn(s);
}

let _backoffUntil = 0;
let _sessionEnded = false;

export function loginUrlFor(pathname: string, search: string): string {
  return `/login?next=${encodeURIComponent(`${pathname}${search}`)}`;
}

function endSession() {
  if (_sessionEnded) return;
  _sessionEnded = true;
  setConnectionStatus("session-expired");
  if (typeof window === "undefined" || !window.location) return;
  const { pathname, search } = window.location;
  window.location.assign(loginUrlFor(pathname || "/", search || ""));
}

function authFetch(url: string, opts: RequestInit = {}): Promise<Response> {
  if (_sessionEnded) {
    return Promise.reject(new Error("session ended -- sign in again"));
  }
  if (Date.now() < _backoffUntil) {
    return Promise.reject(new Error("rate-limited -- backing off"));
  }
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10_000);
  return fetch(url, {
    ...opts,
    headers: {
      ...opts.headers,
      ...getSessionCSRFHeaders(opts.method),
    },
    signal: controller.signal,
  })
    .then((res) => {
      if (res.status === 429) {
        _backoffUntil = Date.now() + 10_000;
        setConnectionStatus("ok");
        return res;
      }
      if (res.status === 401 && sessionMode()) {
        endSession();
      } else if (res.status === 401 || res.status === 403) {
        setConnectionStatus("unauthorized");
      } else {
        setConnectionStatus("ok");
      }
      return res;
    })
    .catch((err) => {
      setConnectionStatus("unreachable");
      throw err;
    })
    .finally(() => clearTimeout(timeout));
}

export function getControllerUrl(): string {
  return API_URL;
}

export interface Run {
  id: string;
  pipeline: string;
  status: string;
  trigger_source?: string;
  git_branch?: string;
  git_sha?: string;
  repo?: string;
  repo_url?: string;
  github_owner?: string;
  github_repo?: string;
  retry_of?: string;
  retried_as?: string;
  retry_source?: string;
  parent_run_id?: string;
  replay_of_run_id?: string;
  replay_of_node_id?: string;
  args?: Record<string, string>;
  error?: string;
  started_at: string;
  finished_at?: string;
  annotation_count?: number;
  top_annotation?: string;
  annotations?: string[];
  invocation?: RunInvocation;
}

export interface RunInvocation {
  run_id?: string;
  pipeline?: string;
  binary_source?: string;
  cwd?: string;
  log_path?: string;
  args?: Record<string, string>;
  flags?: Record<string, unknown>;
  inputs_hash?: string;
  plan_hash?: string;
  reproducer?: string;
  trigger_env_keys?: string[];
  hints?: Record<string, string>;
  secret_args?: string[];
}

export function runDurationMs(run: Run): number {
  if (!run.finished_at) return 0;
  return (
    new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()
  );
}

export interface Node {
  run_id?: string;
  id: string;
  status: string;
  outcome: string;
  deps: string[];
  error?: string;
  output?: unknown;
  started_at?: string;
  finished_at?: string;
  duration_ms: number;
  claimed?: boolean;
  executor_kind?: string;
  executor_name?: string;
  executor_location?: "local" | "cloud" | "unknown";
  execution_started_at?: string;
  execution_attempts?: ExecutionAttempt[];
  status_detail?: string;
  last_heartbeat?: string;
  failure_reason?: string;
  exit_code?: number;
  annotations?: string[];
  summary?: string;
  groups?: string[];
  dynamic?: boolean;
  approval?: boolean;
  on_failure_of?: string;
  modifiers?: NodeModifiers;
  work?: NodeWork;
  spawned_pipelines?: SpawnedPipelineRef[];
}

export interface ExecutionAttempt {
  run_id?: string;
  node_id?: string;
  attempt?: number;
  executor_kind?: string;
  executor_name?: string;
  location?: "local" | "cloud" | "unknown";
  platform?: string;
  started_at?: string;
  finished_at?: string;
  outcome?: string;
  failure_reason?: string;
  retry_run_id?: string;
}

export interface SpawnedPipelineRef {
  pipeline: string;
  child_run_id: string;
}

export interface NodeModifiers {
  retry?: number;
  retry_backoff_ms?: number;
  retry_auto?: boolean;
  timeout_ms?: number;
  no_progress_timeout_ms?: number;
  runs_on?: string[];
  cache?: boolean;
  cache_ttl_ms?: number;
  conc_group?: string;
  conc_capacity?: number;
  conc_cost?: number;
  conc_scope?: string;
  conc_on_limit?: string;
  conc_queue_timeout_ms?: number;
  conc_cancel_timeout_ms?: number;
  inline?: boolean;
  optional?: boolean;
  continue_on_error?: boolean;
  on_failure?: string;
  has_before_run?: boolean;
  has_after_run?: boolean;
  has_skip_if?: boolean;
}

export interface NodeWork {
  steps?: NodeWorkStep[];
  spawns?: NodeWorkSpawn[];
  spawn_each?: NodeWorkSpawnEach[];
  result_step?: string;
  step_groups?: NodeStepGroup[];
}

export interface NodeStepGroup {
  name?: string;
  members: string[];
}

export interface NodeWorkStep {
  id: string;
  needs?: string[];
  is_result?: boolean;
  has_skip_if?: boolean;
  status?: "running" | "passed" | "failed" | "cancelled" | "skipped";
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  annotations?: string[];
  summary?: string;
}

export interface NodeWorkSpawn {
  id: string;
  needs?: string[];
  target_job?: string;
  target_work?: NodeWork;
  has_skip_if?: boolean;
}

export interface NodeWorkSpawnEach {
  id: string;
  needs?: string[];
  target_job?: string;
  item_template_work?: NodeWork;
  note?: string;
}

export interface RunDetail {
  run: Run;
  nodes: Node[];
}

export interface RunFilter {
  limit?: number;
  pipeline?: string;
  status?: string;
  since?: string;
}

export async function getRuns(filter: RunFilter = {}): Promise<Run[]> {
  const params = new URLSearchParams();
  if (filter.limit) params.set("limit", String(filter.limit));
  if (filter.pipeline) params.set("pipeline", filter.pipeline);
  if (filter.status) params.set("status", filter.status);
  if (filter.since) params.set("since", filter.since);
  const url = `${API_URL}/api/v1/runs${params.toString() ? `?${params}` : ""}`;
  const res = await authFetch(url, { cache: "no-store" });
  if (!res.ok) return [];
  const body = await res.json();
  return body.runs || [];
}

export async function getRunAttempts(runID: string): Promise<Run[]> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}/attempts`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  const body = await res.json();
  return body.runs || [];
}

export async function getRun(runID: string): Promise<RunDetail | null> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}?include=nodes`, {
    cache: "no-store",
  });
  if (!res.ok) return null;
  const body = (await res.json()) as RunDetail;
  if (body && Array.isArray(body.nodes)) {
    for (const n of body.nodes) {
      const dec = (n as Node & { decorations?: Partial<Node> }).decorations;
      if (!dec) continue;
      if (dec.work && !n.work) n.work = dec.work;
      if (dec.modifiers && !n.modifiers) n.modifiers = dec.modifiers;
      if (dec.groups && !n.groups) n.groups = dec.groups;
      if (dec.dynamic && n.dynamic == null) n.dynamic = dec.dynamic;
      if (dec.approval && n.approval == null) n.approval = dec.approval;
      if (dec.on_failure_of && !n.on_failure_of)
        n.on_failure_of = dec.on_failure_of;
      if (dec.spawned_pipelines && !n.spawned_pipelines)
        n.spawned_pipelines = dec.spawned_pipelines;
    }
  }
  return body;
}

export async function getRunLogs(runID: string): Promise<string> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${runID}/logs?format=ndjson`,
    {
      cache: "no-store",
      headers: { Accept: "application/x-ndjson" },
    },
  );
  if (!res.ok) return "";
  return res.text();
}

export async function getNodeLogs(
  runID: string,
  nodeID: string,
): Promise<string> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${runID}/logs/${nodeID}?format=ndjson`,
    { cache: "no-store", headers: { Accept: "application/x-ndjson" } },
  );
  if (!res.ok) return "";
  return res.text();
}

export interface RunLogMatch {
  node_id: string;
  line: number;
  content: string;
}

export interface RunLogSearchResponse {
  query: string;
  results: RunLogMatch[];
  total: number;
}

export async function searchRunLogs(
  runID: string,
  query: string,
  limit = 500,
): Promise<RunLogSearchResponse> {
  const params = new URLSearchParams({ q: query, limit: String(limit) });
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${runID}/logs/search?${params}`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return { query, results: [], total: 0 };
  return res.json();
}

export function getNodeStreamUrl(runID: string, nodeID: string): string {
  return `${API_URL}/api/v1/runs/${runID}/logs/${nodeID}/stream?format=ndjson`;
}

export interface RunsGrepMatch {
  run_id: string;
  pipeline: string;
  node_id: string;
  step_id?: string;
  line: number;
  content: string;
}

export interface RunsGrepResponse {
  query: string;
  matches: RunsGrepMatch[];
  runs: Record<string, Run>;
  total: number;
  runs_scanned: number;
}

export interface RunsGrepOpts {
  pipelines?: string[];
  excludePipelines?: string[];
  statuses?: string[];
  excludeStatuses?: string[];
  branches?: string[];
  excludeBranches?: string[];
  shaPrefixes?: string[];
  excludeShaPrefixes?: string[];
  since?: string;
  limit?: number;
  maxMatches?: number;
}

export async function searchRunsGrep(
  query: string,
  opts: RunsGrepOpts = {},
): Promise<RunsGrepResponse> {
  const params = new URLSearchParams({ q: query });
  for (const p of opts.pipelines ?? []) params.append("pipeline", p);
  for (const p of opts.excludePipelines ?? []) params.append("npipeline", p);
  for (const s of opts.statuses ?? []) params.append("status", s);
  for (const s of opts.excludeStatuses ?? []) params.append("nstatus", s);
  for (const b of opts.branches ?? []) params.append("branch", b);
  for (const b of opts.excludeBranches ?? []) params.append("nbranch", b);
  for (const s of opts.shaPrefixes ?? []) params.append("sha", s);
  for (const s of opts.excludeShaPrefixes ?? []) params.append("nsha", s);
  if (opts.since) params.set("since", opts.since);
  if (opts.limit) params.set("limit", String(opts.limit));
  if (opts.maxMatches !== undefined)
    params.set("max_matches", String(opts.maxMatches));
  const res = await authFetch(`${API_URL}/api/v1/runs/grep?${params}`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) {
    return { query, matches: [], runs: {}, total: 0, runs_scanned: 0 };
  }
  return res.json();
}

export function getRunEventsStreamUrl(runID: string): string {
  return `${API_URL}/api/v1/runs/${runID}/events/stream`;
}

export async function listRunEvents(
  runID: string,
  opts?: { after?: number; limit?: number },
): Promise<RunEvent[]> {
  const params = new URLSearchParams();
  if (opts?.after) params.set("after", String(opts.after));
  if (opts?.limit) params.set("limit", String(opts.limit));
  const url = `${API_URL}/api/v1/runs/${runID}/events${
    params.toString() ? `?${params}` : ""
  }`;
  const res = await authFetch(url, { cache: "no-store" }).catch(() => null);
  if (!res || !res.ok) return [];
  const body = await res.json();
  return Array.isArray(body) ? (body as RunEvent[]) : [];
}

export interface RunEvent {
  run_id: string;
  seq: number;
  node_id?: string;
  kind: string;
  ts: string;
  payload?: unknown;
}

export async function triggerRun(
  pipeline: string,
  args?: Record<string, string>,
): Promise<{ run_id: string } | null> {
  const res = await authFetch(`${API_URL}/api/v1/triggers`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      pipeline,
      args: args || {},
      trigger: { source: "dashboard" },
    }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `trigger failed: ${res.status}`);
  }
  return res.json();
}

export async function cancelRun(runID: string): Promise<void> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}/cancel`, {
    method: "POST",
  });
  if (!res.ok && res.status !== 204) {
    throw new Error(`cancel failed: ${res.status}`);
  }
}

export async function deleteRun(runID: string): Promise<void> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}`, {
    method: "DELETE",
  });
  if (res.status === 403) {
    throw new Error("delete needs the admin scope");
  }
  if (!res.ok && res.status !== 204) {
    throw new Error(`delete failed: ${res.status}`);
  }
}

export interface Agent {
  name: string;
  type: string;
  location?: "local" | "cloud" | "unknown";
  labels: Record<string, string>;
  capabilities?: string[];
  last_seen: string;
  status: string;
  active_jobs?: string[];
  active_slots?: number;
  max_concurrent: number;
  base_priority?: number;
  priority_ceiling?: number;
  budget?: { cores: number; memory_bytes: number };
  headroom?: {
    cores: number;
    memory_bytes: number;
    queue_depth: number;
    observed_at: string;
  };
}

export async function getAgents(): Promise<Agent[]> {
  const res = await authFetch(`${API_URL}/api/v1/agents`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  const data = await res.json();
  return data.agents || [];
}

export interface PipelineArg {
  name: string;
  type: string;
  required: boolean;
  desc: string;
  default?: string;
}

export interface PipelineMeta {
  args: PipelineArg[];
  tags?: string[];
}

let _pipelinesUnavailable = false;
export async function getPipelines(): Promise<Record<string, PipelineMeta>> {
  if (_pipelinesUnavailable) return {};
  const res = await authFetch(`${API_URL}/api/v1/pipelines`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res) return {};
  if (res.status === 404) {
    _pipelinesUnavailable = true;
    return {};
  }
  if (!res.ok) return {};
  const data = await res.json();
  return data.pipelines || {};
}

export interface TrendPoint {
  bucket: string;
  total: number;
  passed: number;
  failed: number;
  cached: number;
  avg_dur_ms: number;
  p95_dur_ms: number;
  avg_wait_ms: number;
}

export interface TrendsResponse {
  points: TrendPoint[];
  pipeline?: string;
}

export async function getTrends(opts?: {
  pipeline?: string;
  hours?: number;
}): Promise<TrendsResponse> {
  const params = new URLSearchParams();
  if (opts?.pipeline) params.set("pipeline", opts.pipeline);
  if (opts?.hours) params.set("hours", String(opts.hours));
  const url = `${API_URL}/api/v1/trends${params.toString() ? `?${params}` : ""}`;
  const res = await authFetch(url, { cache: "no-store" }).catch(() => null);
  if (!res || !res.ok) return { points: [] };
  return res.json();
}

export interface ServiceStatus {
  name: string;
  url: string;
  status: string;
  latency_ms: number;
  checked_at: string;
  error?: string;
  problems?: string[];
}

export async function getServiceHealth(): Promise<ServiceStatus[]> {
  const res = await authFetch(`${API_URL}/api/v1/health/services`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  const data = await res.json();
  return data.services || [];
}

export interface LogSearchResult {
  run_id: string;
  node_id: string;
  line: number;
  content: string;
}

export interface LogSearchResponse {
  query: string;
  results: LogSearchResult[];
  total: number;
  truncated?: boolean;
}

export function getLogsUrl(): string {
  if (typeof window !== "undefined") {
    return process.env.NEXT_PUBLIC_LOGS_URL || API_URL;
  }
  return process.env.SPARKWING_LOGS_URL || "";
}

export async function searchLogs(
  query: string,
  opts?: { runID?: string; nodeID?: string; limit?: number },
): Promise<LogSearchResponse> {
  const logsUrl = getLogsUrl();
  const params = new URLSearchParams({ q: query });
  if (opts?.runID) params.set("run_id", opts.runID);
  if (opts?.nodeID) params.set("node_id", opts.nodeID);
  if (opts?.limit) params.set("limit", String(opts.limit));
  const url = `${logsUrl}/api/v1/logs/search?${params}`;
  const res = await authFetch(url, { cache: "no-store" }).catch(() => null);
  if (!res || !res.ok) return { query, results: [], total: 0 };
  return res.json();
}

export interface MetricPoint {
  ts: string;
  cpu_millicores: number;
  memory_bytes: number;
}

export interface NodeMetrics {
  points: MetricPoint[];
  memory_limit_bytes?: number;
  cpu_limit_millicores?: number;
}

export async function getNodeMetrics(
  runID: string,
  nodeID: string,
): Promise<NodeMetrics> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${runID}/nodes/${nodeID}/metrics`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return { points: [] };
  return res.json();
}

export type JobMetrics = NodeMetrics;

export async function retryRun(
  runID: string,
  opts?: { full?: boolean },
): Promise<Run | null> {
  const qs = opts?.full ? "?full=1" : "";
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}/retry${qs}`, {
    method: "POST",
  }).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export interface Job {
  id: string;
  pipeline: string;
  status: string;
  repo_url?: string;
  branch?: string;
  prefer?: string;
  require?: string;
  env?: Record<string, string>;
  parent_id?: string;
  commit?: string;
  repo_name?: string;
  agent_id?: string;
  github_owner?: string;
  github_repo?: string;
  github_sha?: string;
  logs_url?: string;
  created_at: string;
  claimed_at?: string;
  last_heartbeat?: string;
  retried_as?: string;
  retry_of?: string;
  status_detail?: string;
  result?: {
    success: boolean;
    duration: number;
    logs?: string;
    failure_reason?: string;
    exit_code?: number;
    pipeline_result?: {
      pipeline: string;
      jobs: {
        name: string;
        duration: number;
        status: string;
        parallel?: boolean;
        rollback?: boolean;
        logs?: string;
        steps?: {
          name: string;
          duration: number;
          status: string;
          logs?: string;
        }[];
      }[];
      posts?: { condition: string; name: string; duration: number }[];
      total: number;
      failed_job?: string;
    };
  };
}

export interface JobsPage {
  jobs: Job[];
  total: number;
  limit: number;
  offset: number;
}

export async function getJobs(): Promise<Job[]> {
  const runs = await getRuns({ limit: 50 });
  return runs.map((r) => ({
    id: r.id,
    pipeline: r.pipeline,
    status: mapRunStatusToJobStatus(r.status),
    created_at: r.started_at,
    result: r.finished_at
      ? {
          success: r.status === "success",
          duration: runDurationMs(r) * 1_000_000,
        }
      : undefined,
  }));
}

function mapRunStatusToJobStatus(status: string): string {
  if (status === "success") return "complete";
  if (status === "failed") return "failed";
  if (status === "cancelled") return "cancelled";
  return "running";
}

export async function getJobsPaginated(
  limit = 50,
  offset = 0,
): Promise<JobsPage> {
  const jobs = await getJobs();
  return { jobs, total: jobs.length, limit, offset };
}

export async function getJob(): Promise<Job | null> {
  return null;
}

export async function getJobMetrics(_jobId?: string): Promise<NodeMetrics> {
  void _jobId;
  return { points: [] };
}

export async function triggerJob(
  pipeline: string,
  opts?: {
    prefer?: string;
    require?: string;
    env?: Record<string, string>;
    args?: Record<string, string>;
  },
): Promise<Job> {
  const res = await triggerRun(pipeline, opts?.args);
  return {
    id: res?.run_id || "",
    pipeline,
    status: "running",
    created_at: new Date().toISOString(),
  };
}

export async function cancelJob(jobId: string): Promise<void> {
  return cancelRun(jobId);
}

export async function retryJob(jobId: string): Promise<Job | null> {
  const run = await retryRun(jobId);
  if (!run) return null;
  return {
    id: run.id,
    pipeline: run.pipeline,
    status: mapRunStatusToJobStatus(run.status),
    created_at: run.started_at,
  };
}

export async function getBreakpointStatus(): Promise<{ status: string }> {
  return { status: "" };
}

export async function continueBreakpoint(): Promise<void> {
  throw new Error("breakpoints not implemented");
}

export interface PauseState {
  run_id: string;
  node_id: string;
  reason: string;
  paused_at: string;
  expires_at: string;
  released_at?: string;
  released_by?: string;
  release_kind?: string;
}

export async function getPaused(runID: string): Promise<PauseState[]> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}/paused`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  return res.json();
}

export interface Approval {
  run_id: string;
  node_id: string;
  requested_at: string;
  message?: string;
  timeout_ms?: number;
  on_timeout?: string;
  approver?: string;
  resolved_at?: string;
  resolution?: string;
  comment?: string;
}

export async function getApproval(
  runID: string,
  nodeID: string,
): Promise<Approval | null> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${runID}/approvals/${encodeURIComponent(nodeID)}`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export async function getPendingApprovals(): Promise<Approval[]> {
  const res = await authFetch(`${API_URL}/api/v1/approvals/pending`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  const body = await res.json();
  return body.approvals || [];
}

export async function resolveApproval(
  runID: string,
  nodeID: string,
  resolution: "approved" | "denied",
  comment: string,
): Promise<Approval> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${runID}/approvals/${encodeURIComponent(nodeID)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ resolution, comment }),
    },
  );
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(text || `resolve failed: ${res.status}`);
  }
  return res.json();
}

export async function releaseNode(
  runID: string,
  nodeID: string,
): Promise<void> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${runID}/nodes/${encodeURIComponent(nodeID)}/release`,
    { method: "POST" },
  );
  if (!res.ok && res.status !== 204) {
    throw new Error(`release failed: ${res.status}`);
  }
}

export interface HostResources {
  cores?: number;
  memory_bytes?: number;
}

export interface QueueResource {
  key: string;
  capacity: number;
  held: number;
  reserved?: number;
  external?: number;
  external_source?: string;
  available?: number;
}

export interface QueueHolder {
  run_id: string;
  participant_id?: string;
  display_run_id?: string;
  pipeline?: string;
  repo?: string;
  parent?: string;
  parent_participant_id?: string;
  elapsed_ms: number;
  resources: HostResources;
  semaphores?: string[];
  connection_only?: boolean;
  cost_source?: string;
  cost_rationale?: string;
  expected_duration_ms?: number;
  drift_warning?: string;
  stalled?: boolean;
  admission_waiting?: boolean;
  active_waiter_participant_ids?: string[];
  contended?: boolean;
  contention_reason?: string;
  recovery?: string;
}

export interface QueueWaiter {
  run_id: string;
  participant_id?: string;
  display_run_id?: string;
  pipeline?: string;
  repo?: string;
  position: number;
  resources: HostResources;
  semaphores?: string[];
  waiting_on?: string[];
  blocking_reason?: string;
  waiting_ms?: number;
  cost_source?: string;
  cost_rationale?: string;
  expected_duration_ms?: number;
  drift_warning?: string;
  expected_start_ms?: number | null;
}

export interface QueueEvents {
  window_ms: number;
  runs: number;
  median_wait_ms: number;
  evictions?: { key: string; count: number }[];
  queue_timeouts?: number;
  cancellations?: number;
  contended?: number;
}

export interface QueueState {
  resources?: QueueResource[];
  holders?: QueueHolder[];
  waiters?: QueueWaiter[];
  expected_clear_ms?: number | null;
  daemon_version?: string;
  daemon_uptime_ms?: number;
  ignore_external?: boolean;
  external_sample_age_ms?: number;
  external_measurement_age_ms?: number;
  events?: QueueEvents | null;
}

export async function getQueue(): Promise<QueueState | null> {
  const res = await authFetch(`${API_URL}/api/v1/queue`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export interface CapacityCharge {
  cores: number;
  memory_bytes: number;
  source: string;
  rationale?: string;
  cores_basis: string;
  floor_applied?: boolean;
}

export interface CapacityProfile {
  pipeline: string;
  // Decoded repo/pipeline form of the stored key; absent from older
  // backends, so renderers fall back to the raw pipeline key.
  display?: string;
  charge: CapacityCharge;
  sample_count: number;
  peak_cores: number;
  sustained_cores: number;
  peak_memory_bytes: number;
  cpu_p50: number;
  cpu_p95: number;
  memory_p50_bytes: number;
  memory_p95_bytes: number;
  cpu_measured: boolean;
  p50_duration_ms: number;
  p99_duration_ms: number;
  wait_p50_ms?: number;
  wait_p99_ms?: number;
  floor_cores?: number;
  floor_memory_bytes?: number;
  pinned_cores?: number;
  pinned_memory_bytes?: number;
  drift?: string;
  drift_class?: string;
  contended_count?: number;
  plan_hash?: string;
  updated_at_ms?: number;
  node_count?: number;
}

export interface CapacityConstants {
  min_samples: number;
  charge_percentile: number;
  sustained_percentile: number;
  warm_start_multiple: number;
  safety_multiple: number;
  drift_fraction: number;
  cold_start_cores: number;
}

export interface CapacityChargeStep {
  step: string;
  label: string;
  cores?: number;
  memory_bytes?: number;
  eligible: boolean;
  applied: boolean;
  detail?: string;
}

export interface CapacityRankSelection {
  field: string;
  percentile: number;
  rank: number;
  count: number;
  index: number;
  value: number;
  stored: number;
  matches: boolean;
  unmeasured?: boolean;
}

export interface CapacitySample {
  index: number;
  duration_ms: number;
  peak_cores: number;
  sustained_cores: number;
  peak_memory_bytes: number;
}

export interface CapacityNode {
  node_id: string;
  sample_count: number;
  peak_cores: number;
  sustained_cores: number;
  peak_memory_bytes: number;
  p50_duration_ms: number;
  p99_duration_ms: number;
}

export interface CapacityProfiles {
  machine_cores: number;
  constants: CapacityConstants;
  profiles: CapacityProfile[];
  generated_at_ms: number;
}

export interface CapacityExplain {
  machine_cores: number;
  constants: CapacityConstants;
  profile: CapacityProfile;
  chain: CapacityChargeStep[];
  samples: CapacitySample[];
  selections: {
    cores: CapacityRankSelection;
    memory: CapacityRankSelection;
    duration_p50: CapacityRankSelection;
    duration_p99: CapacityRankSelection;
  };
  nodes: CapacityNode[];
  ceiling_note: string;
  generated_at_ms: number;
}

export async function getCapacityProfiles(): Promise<CapacityProfiles | null> {
  const res = await authFetch(`${API_URL}/api/v1/capacity/profiles`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export async function getCapacityExplain(
  pipeline: string,
): Promise<CapacityExplain | null> {
  const res = await authFetch(
    `${API_URL}/api/v1/capacity/profiles/explain?pipeline=${encodeURIComponent(pipeline)}`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}
