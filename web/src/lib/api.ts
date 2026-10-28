import { readCSRFCookie } from "./csrfCookie";
import { localAuthHeaders } from "./localSession";
import type { LogCompleteness } from "./logCompleteness";
import type { TriggerGit } from "./triggerSource";

function getApiUrl(): string {
  if (typeof window !== "undefined") return "";
  return process.env.SPARKWING_CONTROLLER_URL || "";
}

const API_URL = getApiUrl();

// A browser resolves "." and ".." segments before sending, even encoded, so an
// id taken from the URL could otherwise retarget a request to another route.
function pathSegment(id: string): string {
  if (id === "" || id === "." || id === "..") {
    throw new Error(`invalid path segment ${JSON.stringify(id)}`);
  }
  return encodeURIComponent(id);
}

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
  const token = readCSRFCookie();
  if (!token) return {};
  return { "X-CSRF-Token": token };
}

export type ConnectionStatus =
  "ok" | "unreachable" | "unauthorized" | "forbidden" | "session-expired";
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

// A route whose refusal answers a question about the caller, such as /me for an
// operator signed in with a token, passes speaksForSession: false so its 401
// never reads as the dashboard session ending, which would reload in a loop.
export interface AuthFetchOptions {
  speaksForSession?: boolean;
}

export function authFetch(
  url: string,
  opts: RequestInit = {},
  { speaksForSession = true }: AuthFetchOptions = {},
): Promise<Response> {
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
      ...localAuthHeaders(),
    },
    signal: controller.signal,
  })
    .then((res) => {
      if (res.status === 429) {
        _backoffUntil = Date.now() + 10_000;
        setConnectionStatus("ok");
        return res;
      }
      if (!speaksForSession && [401, 403, 404].includes(res.status)) {
        return res;
      }
      if (res.status === 401 && sessionMode()) {
        endSession();
      } else if (res.status === 401) {
        setConnectionStatus("unauthorized");
      } else if (res.status === 403) {
        setConnectionStatus("forbidden");
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
  execution_site?: "machine" | "cloud" | "github-actions" | "cluster";
  execution_site_name?: string;
  execution_started_at?: string;
  execution_attempts?: ExecutionAttempt[];
  status_detail?: string;
  placement_reason?: string;
  claimed_by?: string;
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
  requested_cores?: number;
  requested_memory_bytes?: number;
  cpu_nanos?: number;
  max_rss_bytes?: number;
  process_wall_nanos?: number;
}

export interface ExecutionAttempt {
  run_id?: string;
  node_id?: string;
  attempt?: number;
  executor_kind?: string;
  executor_name?: string;
  location?: "local" | "cloud" | "unknown";
  execution_site?: "machine" | "cloud" | "github-actions" | "cluster";
  execution_site_name?: string;
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

export interface RunDetail {
  run: Run;
  nodes: Node[];
}

// RunDetailWire is GET /api/v1/runs/{run}: controllerwire.RunDetail, the
// RunDetail schema in api/openapi.yaml. testdata/run-detail-*.json are decoded
// strictly into the Go type by internal/controllerwire, so a field rename
// breaks that test as well as this adapter's.
export interface RunDetailWire {
  id: string;
  number: number;
  team: string;
  status: string;
  failure?: string;
  state_seq: number;
  inputs: {
    pipeline: string;
    requested_args: Record<string, string> | null;
    args: Record<string, string> | null;
    secret_args: string[] | null;
    git: {
      repository: { key: string; display: string };
      ref: string;
      sha: string;
    };
    lineage: { parent_run_id: string; parent_job_id: string; retry_of: string };
  };
  jobs: JobDetailWire[];
  attempts: AttemptDetailWire[];
  started_at?: string;
  finished_at?: string;
  cancel_requested_at?: string;
}

export interface JobDetailWire {
  id: string;
  status: string;
  outcome?: string;
  status_detail?: string;
  error?: string;
  generation: number;
  attempt: number;
  annotations?: { message?: string }[];
  started_at?: string;
  finished_at?: string;
}

export interface AttemptDetailWire {
  job_id: string;
  attempt: number;
  generation: number;
  outcome?: string;
  reason?: string;
  executor: string;
  started_at?: string;
  finished_at?: string;
}

function durationMs(started?: string, finished?: string): number {
  if (!started || !finished) return 0;
  return Math.max(0, new Date(finished).getTime() - new Date(started).getTime());
}

// runDetailView projects the controller's run detail onto the run page's view.
// The wire carries no job dependencies, so the page lays jobs out without edges.
export function runDetailView(wire: RunDetailWire): RunDetail {
  const inputs = wire.inputs;
  const run: Run = {
    id: wire.id,
    pipeline: inputs.pipeline,
    status: wire.status,
    git_branch: inputs.git.ref || undefined,
    git_sha: inputs.git.sha || undefined,
    repo: inputs.git.repository.display || undefined,
    retry_of: inputs.lineage.retry_of || undefined,
    parent_run_id: inputs.lineage.parent_run_id || undefined,
    args: inputs.args ?? inputs.requested_args ?? undefined,
    error: wire.failure || undefined,
    started_at: wire.started_at ?? "",
    finished_at: wire.finished_at,
  };
  const nodes = wire.jobs.map((job): Node => {
    const attempts = wire.attempts
      .filter((attempt) => attempt.job_id === job.id)
      .map((attempt): ExecutionAttempt => ({
        run_id: wire.id,
        node_id: job.id,
        attempt: attempt.attempt,
        executor_name: attempt.executor || undefined,
        started_at: attempt.started_at,
        finished_at: attempt.finished_at,
        outcome: attempt.outcome,
        failure_reason: attempt.reason,
      }));
    const annotations = (job.annotations ?? [])
      .map((annotation) => annotation.message ?? "")
      .filter((message) => message !== "");
    return {
      run_id: wire.id,
      id: job.id,
      status: job.status === "done" ? job.outcome || "done" : job.status,
      outcome: job.outcome ?? "",
      deps: [],
      error: job.error || undefined,
      status_detail: job.status_detail || undefined,
      started_at: job.started_at,
      finished_at: job.finished_at,
      duration_ms: durationMs(job.started_at, job.finished_at),
      execution_attempts: attempts.length ? attempts : undefined,
      annotations: annotations.length ? annotations : undefined,
    };
  });
  return { run, nodes };
}

// planFailureHint is the CLI's hint for a run that failed before planning any job.
export function planFailureHint(run: Run, nodes: Node[]): string | null {
  if (!run.error || run.status !== "failed" || nodes.length > 0) return null;
  return `The pipeline failed to plan, so no job ran. Fix the error above, check it from the repository with \`sparkwing pipeline plan --pipeline ${run.pipeline}\`, and trigger the run again.`;
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

// listRuns sends a query runListQuery built and fails loud, because an empty page read
// as "no runs" would hide a controller error behind a blank history.
export async function listRuns(params: URLSearchParams): Promise<Run[]> {
  const res = await authFetch(`${API_URL}/api/v1/runs?${params}`, {
    cache: "no-store",
  });
  if (!res.ok) {
    const detail = await res.text().catch(() => "");
    throw new Error(`Listing runs failed (${res.status})${detail ? `: ${detail.trim()}` : ""}`);
  }
  const body = await res.json();
  return body.runs || [];
}

export interface ControllerQueueState {
  resources?: Array<{ key: string; capacity: number; held: number }>;
  holders?: Array<{ run_id: string }>;
  waiters?: Array<{ run_id: string; position: number }>;
}

export async function getControllerQueueState(): Promise<ControllerQueueState | null> {
  const res = await authFetch(`${API_URL}/api/v1/queue`, { cache: "no-store" }).catch(() => null);
  return res?.ok ? res.json() : null;
}

export interface RunHistoryEntry {
  id: string;
  status: string;
  started_at?: string;
  finished_at?: string;
  inputs: { lineage: { retry_of: string } };
}

export async function getRunAttempts(runID: string): Promise<RunHistoryEntry[]> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${pathSegment(runID)}/attempts`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  const body = await res.json();
  return body.runs || [];
}

export async function getRun(runID: string): Promise<RunDetail | null> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${pathSegment(runID)}`, {
    cache: "no-store",
  });
  if (!res.ok) return null;
  return runDetailView((await res.json()) as RunDetailWire);
}

export async function getNodeLogs(
  runID: string,
  nodeID: string,
): Promise<string> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${pathSegment(runID)}/jobs/${pathSegment(nodeID)}/logs?format=ndjson`,
    { cache: "no-store", headers: { Accept: "application/x-ndjson" } },
  );
  if (!res.ok) return "";
  return res.text();
}

export async function getNodeLogCompleteness(
  runID: string,
  nodeID: string,
): Promise<LogCompleteness | null> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${pathSegment(runID)}/jobs/${pathSegment(nodeID)}/logs/completeness`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
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
    `${API_URL}/api/v1/runs/${pathSegment(runID)}/logs/search?${params}`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return { query, results: [], total: 0 };
  return res.json();
}

export function getNodeStreamUrl(runID: string, nodeID: string): string {
  return `${API_URL}/api/v1/runs/${pathSegment(runID)}/jobs/${pathSegment(nodeID)}/logs/stream?format=ndjson`;
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
  runs_matching: number;
  next_cursor?: { after_id: string; after_started_at: string };
}

export interface RunsGrepOpts {
  runIDs?: string[];
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
  after?: { after_id: string; after_started_at: string };
}

export async function searchRunsGrep(
  query: string,
  opts: RunsGrepOpts = {},
): Promise<RunsGrepResponse> {
  const params = new URLSearchParams({ q: query });
  for (const id of opts.runIDs ?? []) params.append("run_id", id);
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
  if (opts.after) {
    params.set("after_id", opts.after.after_id);
    params.set("after_started_at", opts.after.after_started_at);
  }
  const res = await authFetch(`${API_URL}/api/v1/runs/grep?${params}`, {
    cache: "no-store",
  });
  if (!res.ok) throw new Error(`Search failed (${res.status})`);
  return res.json();
}

export function getRunEventsStreamUrl(runID: string): string {
  return `${API_URL}/api/v1/runs/${pathSegment(runID)}/events/stream`;
}

export async function listRunEvents(
  runID: string,
  opts?: { after?: number; limit?: number },
): Promise<RunEvent[]> {
  const params = new URLSearchParams();
  if (opts?.after) params.set("after", String(opts.after));
  if (opts?.limit) params.set("limit", String(opts.limit));
  const url = `${API_URL}/api/v1/runs/${pathSegment(runID)}/events${
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
  git?: TriggerGit,
): Promise<{ run_id: string } | null> {
  const res = await authFetch(`${API_URL}/api/v1/runs`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      idempotency_key: crypto.randomUUID(),
      request: {
        version: 1,
        pipeline,
        mode: "execute",
        origin: "trigger",
        dispatch: "host",
        requested_args: args || {},
        ...(git ? {
          git: { repository: { display: git.repo_url }, ref: git.branch },
          source: { ref: git.branch, location: { remote_url: git.repo_url, pipeline_dir: ".sparkwing" } },
        } : {}),
      },
    }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `trigger failed: ${res.status}`);
  }
  return res.json();
}

export async function cancelRun(runID: string): Promise<void> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${pathSegment(runID)}/cancel`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ reason: "user_cancelled" }),
  });
  if (!res.ok && res.status !== 204) {
    throw new Error(`cancel failed: ${res.status}`);
  }
}

export async function deleteRun(runID: string): Promise<void> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${pathSegment(runID)}`, {
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
  max_concurrent: number;
  headroom?: {
    cores: number;
    memory_bytes: number;
    queue_depth: number;
    observed_at: string;
  };
}

export async function getAgents(): Promise<Agent[]> {
  const res = await authFetch(`${API_URL}/api/v1/runners`, {
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
  last_status?: string;
  last_run_at?: string;
}

// A team's list comes from the controller, newest activity first; the local
// dashboard answers with a name-keyed map of the working directory's pipelines.
interface TeamPipeline {
  name: string;
  last_status?: string;
  last_run_at?: string;
}

export function pipelinesByName(
  raw: Record<string, PipelineMeta> | TeamPipeline[] | undefined,
): Record<string, PipelineMeta> {
  if (!Array.isArray(raw)) return raw || {};
  const out: Record<string, PipelineMeta> = {};
  for (const p of raw) {
    out[p.name] = {
      args: [],
      last_status: p.last_status,
      last_run_at: p.last_run_at,
    };
  }
  return out;
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
  return pipelinesByName(data.pipelines);
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

export interface MetricPoint {
  ts: string;
  cpu_millicores: number;
  memory_bytes: number;
  cpu_time_nanos?: number;
}

export interface NodeMetrics {
  points: MetricPoint[];
}

// A node holds at most 10,000 samples, which is also the largest page, so
// one request normally reads them all; the loop follows next_cursor anyway.
const METRIC_PAGE = 10000;

export async function getNodeMetrics(
  runID: string,
  nodeID: string,
): Promise<NodeMetrics> {
  const points: MetricPoint[] = [];
  let cursor = "";
  for (;;) {
    const qs = new URLSearchParams({ limit: String(METRIC_PAGE) });
    if (cursor) qs.set("cursor", cursor);
    const res = await authFetch(
      `${API_URL}/api/v1/runs/${pathSegment(runID)}/jobs/${pathSegment(nodeID)}/metrics?${qs}`,
      { cache: "no-store" },
    );
    if (!res.ok) throw new Error(`metrics failed: ${res.status}`);
    const page: NodeMetrics & { next_cursor?: string } = await res.json();
    points.push(...(page.points ?? []));
    if (!page.next_cursor || page.next_cursor === cursor) return { points };
    cursor = page.next_cursor;
  }
}

export async function retryRun(
  runID: string,
  opts?: { full?: boolean },
): Promise<Run | null> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${pathSegment(runID)}/retry`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ idempotency_key: crypto.randomUUID(), mode: opts?.full ? "all" : "failed" }),
  });
  if (!res.ok) throw new Error(`retry failed: ${res.status}`);
  return res.json();
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
  const res = await authFetch(`${API_URL}/api/v1/runs/${pathSegment(runID)}/debug-pauses`, {
    cache: "no-store",
  });
  if (!res.ok) throw new Error(`debug pauses failed: ${res.status}`);
  return res.json();
}

export interface Approval {
  run_id: string;
  job_id: string;
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
    `${API_URL}/api/v1/runs/${pathSegment(runID)}/approvals/${pathSegment(nodeID)}`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export async function getPendingApprovals(): Promise<Approval[]> {
  const res = await authFetch(`${API_URL}/api/v1/approvals`, {
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
    `${API_URL}/api/v1/runs/${pathSegment(runID)}/approvals/${pathSegment(nodeID)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ resolution, comment }),
    },
  );
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(text || `resolve failed: ${res.status}`);
  }
  const body = await res.json();
  return body.approval;
}

export async function releaseNode(
  runID: string,
  nodeID: string,
): Promise<void> {
  const res = await authFetch(
    `${API_URL}/api/v1/runs/${pathSegment(runID)}/jobs/${pathSegment(nodeID)}/resume`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    },
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
  const res = await authFetch(`${API_URL}/api/v1/sizing`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export async function getCapacityExplain(
  pipeline: string,
): Promise<CapacityExplain | null> {
  const res = await authFetch(
    `${API_URL}/api/v1/sizing/explain?pipeline=${encodeURIComponent(pipeline)}`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export interface CronTimerState {
  installed: boolean;
  foreign: boolean;
  enabled: boolean;
  stale: boolean;
  path: string;
  binary: string;
  detail: string;
}

export interface CronTick {
  // Empty until the timer has ticked at least once on this host.
  at: string;
  host: string;
  version: string;
  error: string;
}

export interface CronHealth {
  timer: CronTimerState;
  last_tick: CronTick;
  tick_stale: boolean;
  schedules: number;
  armed: number;
  paused: number;
  undeclared: number;
  // Schedules running a pinned pipeline binary, and the ones compiling the
  // checkout on every fire.
  locked: number;
  following: number;
  // Pinned schedules the checkout has moved past, and pins whose binary is gone.
  ahead: number;
  missing_binary: number;
  // Overrides set against a declaration that has changed since.
  stale_override: number;
  detail: string;
  // Empty when none of the counts above wants attention.
  remedy: string;
}

export type CronState = "armed" | "paused" | "undeclared";

export type CronOutcome =
  "" | "fired" | "skipped_overlap" | "missed" | "failed";

// Where a schedule is evaluated. This dashboard reads one host's store, so
// every row it serves is local.
export type CronWhere = "local" | "controller";

export type CronLockState =
  "follows" | "pinned" | "ahead" | "dirty" | "missing";

export interface CronLock {
  // Ref, binary and digest are empty while a schedule follows the checkout.
  ref: string;
  binary: string;
  digest: string;
  state: CronLockState;
}

export interface CronOverride {
  // The declared fields this host has laid its own values over.
  fields: string[];
  stale: boolean;
  // Empty when this host has set no override.
  set_at: string;
}

// The cadence a schedule actually runs: the declaration with this host's
// override laid over it.
export interface CronEffective {
  cron: string;
  tz: string;
  overlap: string;
  catch_up_ns: number;
  args: Record<string, string>;
}

export interface CronSchedule {
  id: string;
  // The display name: <repo>/<pipeline>, or <repo>/<pipeline>/<name>.
  name: string;
  schedule_name: string;
  repo_path: string;
  pipeline: string;
  where: CronWhere;
  cron: string;
  tz: string;
  overlap: string;
  catch_up_ns: number;
  args: Record<string, string>;
  lock: CronLock;
  override: CronOverride;
  effective: CronEffective;
  paused: boolean;
  declared: boolean;
  state: CronState;
  // What a locked schedule's pin is doing, empty while it follows the checkout.
  state_detail: string;
  armed_at: string;
  updated_at: string;
  last_fired_at: string | null;
  last_run_id: string;
  last_outcome: CronOutcome;
  next_due_at: string | null;
}

export interface CronFire {
  id: string;
  schedule_id: string;
  due_at: string;
  decided_at: string;
  outcome: CronOutcome;
  run_id: string;
  detail: string;
  // The arguments this fire launched with.
  args: Record<string, string>;
  run_status: string;
}

export interface CronsOverview {
  health: CronHealth;
  schedules: CronSchedule[];
}

export interface CronDetail {
  schedule: CronSchedule;
  fires: CronFire[];
  upcoming: string[];
}

export async function getCrons(): Promise<CronsOverview | null> {
  const res = await authFetch(`${API_URL}/api/v1/schedules`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

export async function getCron(id: string): Promise<CronDetail | null> {
  const res = await authFetch(
    `${API_URL}/api/v1/schedules/${encodeURIComponent(id)}`,
    { cache: "no-store" },
  ).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

async function postCron<T>(id: string, action: string): Promise<T> {
  const res = await authFetch(
    `${API_URL}/api/v1/schedules/${encodeURIComponent(id)}/${action}`,
    { method: "POST", headers: action === "run" ? { "Idempotency-Key": crypto.randomUUID() } : undefined },
  );
  if (!res.ok) {
    const text = (await res.text().catch(() => "")).trim();
    throw new Error(text || `${action} failed: ${res.status}`);
  }
  return res.json();
}

export async function pauseCron(id: string): Promise<CronSchedule> {
  const body = await postCron<{ schedule: CronSchedule }>(id, "pause");
  return body.schedule;
}

export async function resumeCron(id: string): Promise<CronSchedule> {
  const body = await postCron<{ schedule: CronSchedule }>(id, "resume");
  return body.schedule;
}

export async function runCronNow(
  id: string,
): Promise<{ run_id: string; schedule: CronSchedule }> {
  return postCron<{ run_id: string; schedule: CronSchedule }>(id, "run");
}
