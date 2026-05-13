// API client for the sparkwing dashboard. Talks to the controller
// over HTTP; browser-side client. The Go server that serves this
// SPA templates the token + API URL into window globals at request
// time -- see pkg/orchestrator/web for the templating side.
//
// Public surface (2026-05-04):
//
//   /api/v1/runs                              -- list runs (shape: {runs: Run[]})
//   /api/v1/runs/:id?include=nodes            -- run + nodes (shape: {run: Run, nodes: Node[]})
//   /api/v1/runs/:id/logs                     -- concatenated log text
//   /api/v1/runs/:id/logs/:node               -- per-node log text
//   /api/v1/runs/:id/logs/:node/stream        -- SSE stream of log lines
//   /api/v1/runs/:id/events/stream            -- structured run-event SSE
//   /api/v1/runs/:id/cancel                   -- POST, 204 on success
//   /api/v1/runs/:id/paused                   -- list debug pauses
//   /api/v1/runs/:id/nodes/:node/release      -- POST to release a debug pause
//   /api/v1/triggers                          -- POST, body {pipeline, args, trigger}
//
// The old /jobs, /agents, /pipelines, /api/trends, /api/health/services,
// /search endpoints are dark today. Each gets lit up in its own
// session (PLAN-web-restoration.md sessions B-I). Stubs here return
// empty shapes so consumers compile and render empty states.

function literalMarker(s: unknown, marker: string): string {
  // Values templated by the Go server still look like their literal
  // marker when running under `npm run dev` or when the server skips
  // the substitution. Treat those as "not set".
  if (typeof s !== "string") return "";
  if (s === marker) return "";
  return s;
}

function getApiUrl(): string {
  if (typeof window !== "undefined") {
    const injected = literalMarker(
      (window as unknown as Record<string, unknown>).__SPARKWING_API_URL__,
      "__SPARKWING_API_URL_MARKER__",
    );
    if (injected) return injected;
    // Same-origin default: the Go binary serves both the UI and the
    // /api/* routes, so an empty prefix works under `sparkwing web`.
    return "";
  }
  return process.env.SPARKWING_CONTROLLER_URL || "";
}

const API_URL = getApiUrl();

function getAuthHeaders(): HeadersInit {
  if (typeof window === "undefined") return {};
  const token = literalMarker(
    (window as unknown as Record<string, unknown>).__SPARKWING_TOKEN__,
    "__SPARKWING_TOKEN_MARKER__",
  );
  if (!token) return {};
  return { Authorization: `Bearer ${token}` };
}

// --- Connection health tracking ---
export type ConnectionStatus = "ok" | "unreachable" | "unauthorized";
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

function authFetch(url: string, opts: RequestInit = {}): Promise<Response> {
  if (Date.now() < _backoffUntil) {
    return Promise.reject(new Error("rate-limited — backing off"));
  }
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10_000);
  return fetch(url, {
    ...opts,
    headers: { ...getAuthHeaders(), ...opts.headers },
    signal: controller.signal,
  })
    .then((res) => {
      if (res.status === 429) {
        _backoffUntil = Date.now() + 10_000;
        setConnectionStatus("ok");
        return res;
      }
      if (res.status === 401 || res.status === 403) {
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

// --- New types: Run + Node (post-rewrite) ---

// Run mirrors the raw store.Run JSON shape that /api/v1/runs returns.
// Field names track the Go struct's json tags directly; computed
// fields (duration, tags) live in helpers, not on the wire. The run
// detail endpoint (/api/v1/runs/<id>?include=nodes) returns
// {run: store.Run, nodes: store.Node[]} -- the same Run shape this
// type captures. Snapshot-derived adornments (groups, modifiers,
// work, dynamic, approval, on_failure_of) come from store.Node and
// the run's plan_snapshot field; until those are reattached, the
// node fields render as undefined and the DAG view falls back to
// flat nodes without group headers / modifier chips.
export interface Run {
  id: string;
  pipeline: string;
  status: string; // running | success | failed | cancelled
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
  // Annotation rollup surfaced into list rows. annotation_count is
  // the total across every node + step; top_annotation is the most
  // recent message. Server-maintained, so consumers don't have to
  // load run detail to surface them.
  annotation_count?: number;
  top_annotation?: string;
  // Invocation: snapshot of how the run was started, persisted on the
  // run row at CreateRun time. Mirrors the orchestrator's
  // run_start.attrs payload (see orchestrator/orchestrator.go's
  // buildRunInvocation). Free-form map so the dashboard can surface
  // new fields without a TS schema bump every time the orchestrator
  // adds one.
  invocation?: RunInvocation;
}

// RunInvocation snapshots how a run was started: flags, args, the
// reproducer command, binary cache hit, hashes, hints, etc. Every
// field is optional -- early/partial runs may carry only a subset.
// New context fields land here automatically via the orchestrator's
// buildRunInvocation; consumers that don't recognize them should
// ignore unknown keys gracefully.
export interface RunInvocation {
  run_id?: string;
  pipeline?: string;
  binary_source?: string; // cached | compiled | artifact-store | gitcache
  cwd?: string;
  args?: Record<string, string>;
  flags?: Record<string, unknown>;
  inputs_hash?: string;
  plan_hash?: string;
  reproducer?: string;
  trigger_env_keys?: string[];
  hints?: Record<string, string>;
}

// runDurationMs computes a wall-clock duration from a Run's
// started_at / finished_at timestamps. Returns 0 while the run is
// still in flight (no finished_at). Replaces the server-computed
// duration_ms field that came off the legacy /api/runs list shape.
export function runDurationMs(run: Run): number {
  if (!run.finished_at) return 0;
  return (
    new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()
  );
}

export interface Node {
  id: string;
  status: string;
  outcome: string; // success | failed | skipped | cancelled | cached | satisfied
  deps: string[];
  error?: string;
  output?: unknown;
  started_at?: string;
  finished_at?: string;
  duration_ms: number;
  // Cluster-mode dispatch signal. Holder shapes:
  //   pod:<runID>:<nodeID>       -- K8sRunner fallback
  //   runner:<hostname>:<nanos>  -- warm pool / agent
  claimed_by?: string;
  lease_expires_at?: string;
  // Runner-reported activity string + last heartbeat time. Populated
  // by the active runner so the summary can show "currently doing X"
  // + HeartbeatDot liveness while the node executes.
  status_detail?: string;
  last_heartbeat?: string;
  // Structured failure metadata. failure_reason is one of
  // oom_killed / agent_lost / timeout / queue_timeout / pod_error
  // / error. Empty for success or uncategorized failure; the UI
  // falls back to the raw error string in that case.
  failure_reason?: string;
  exit_code?: number;
  // Free-form, human-readable summaries posted by step code via
  // sparkwing.Annotate(ctx, msg). Multiple entries accumulate in
  // call order. Surfaced in the dashboard's NodeLogSummary block.
  annotations?: string[];
  // Named-group memberships from the Plan DSL: every plan.Group(name,
  // members...) this node belongs to. Populated from the plan snapshot
  // server-side; empty/undefined for ungrouped nodes. Drives the
  // collapsible cluster headers on /pipelines.
  groups?: string[];
  // True when the node is `.Dynamic()` (explicit) or the source of an
  // ExpandFrom (auto-inferred). Drives the rainbow "DYNAMIC" pill in
  // the DAG view, signalling that the plan preview isn't authoritative
  // — the node may spawn children at runtime.
  dynamic?: boolean;
  // True when the node was declared as an approval gate
  // (plan.Approval). Stays true for the whole run; the pill color
  // cycles based on status/outcome. Lets the DAG always surface "this
  // is a human gate" instead of only when a human is currently
  // blocked.
  approval?: boolean;
  // Present on nodes attached via .OnFailure(id, job). Carries the
  // parent node's ID so the DAG can draw a dashed failure-branch
  // edge (parent -> this) and place the recovery node one column
  // right of its parent instead of stranding it at level 0.
  on_failure_of?: string;
  // Active Plan-layer modifiers from the snapshot. Drives the
  // dispatch-envelope chips beside each node id (Retry, Timeout,
  // RunsOn, Cache, Inline). Optional fields are omitted on the wire
  // when unset.
  modifiers?: NodeModifiers;
  // Inner DAG (Steps + SpawnNode declarations). Populated for nodes
  // registered via plan.Job. The dashboard renders this as a
  // collapsible Work section under each node card.
  work?: NodeWork;
  // Cross-pipeline calls this node made via sparkwing.RunAndAwait.
  // Server joins the triggers table at response time; each entry
  // carries the target pipeline name and the spawned child run id.
  // Drives a corner pill on the node so the cross-pipeline edge is
  // visible without drilling into the trigger log.
  spawned_pipelines?: SpawnedPipelineRef[];
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
  runs_on?: string[];
  cache_key?: string;
  cache_max?: number;
  cache_on_limit?: string;
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
  // Named Step bundles from sparkwing.GroupSteps. Members are step
  // ids in declaration order. Empty name = a structural-only group.
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
  // Runtime state joined in from node_steps rows server-side.
  // status is one of "running" | "passed" | "failed" | "skipped";
  // missing/empty means the step hasn't started.
  status?: "running" | "passed" | "failed" | "skipped";
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  // Free-form per-step summaries fired via sparkwing.Annotate()
  // while this step was active. Mirrors RunNode.annotations but
  // scoped to one step inside the inner Work DAG.
  annotations?: string[];
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

export type RunVenue = "local" | "pool" | "jobs" | "pool+jobs" | "cluster";

export function computeVenue(nodes: Node[]): RunVenue {
  const prefixes = new Set<string>();
  for (const n of nodes) {
    if (!n.claimed_by) continue;
    const idx = n.claimed_by.indexOf(":");
    prefixes.add(idx >= 0 ? n.claimed_by.slice(0, idx) : n.claimed_by);
  }
  if (prefixes.size === 0) return "local";
  const hasRunner = prefixes.has("runner");
  const hasPod = prefixes.has("pod");
  if (prefixes.size === 1 && hasRunner) return "pool";
  if (prefixes.size === 1 && hasPod) return "jobs";
  if (prefixes.size === 2 && hasRunner && hasPod) return "pool+jobs";
  return "cluster";
}

export function parseHolder(claimedBy?: string): {
  kind: "local" | "pool" | "jobs" | "cluster";
  label: string;
} {
  if (!claimedBy) return { kind: "local", label: "local" };
  const parts = claimedBy.split(":");
  const prefix = parts[0];
  const second = parts[1] || claimedBy;
  if (prefix === "runner") return { kind: "pool", label: second };
  if (prefix === "pod") return { kind: "jobs", label: second };
  return { kind: "cluster", label: second };
}

// --- New run API ---

export interface RunFilter {
  limit?: number;
  pipeline?: string; // comma-separated accepted by controller
  status?: string;
  since?: string; // Go duration: "1h", "24h"
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

export async function getRun(runID: string): Promise<RunDetail | null> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}?include=nodes`, {
    cache: "no-store",
  });
  if (!res.ok) return null;
  const body = (await res.json()) as RunDetail;
  // The server nests plan-snapshot adornments under `decorations` to
  // keep the core Node row lean. Flatten them onto the Node so the
  // dashboard's existing readers (n.work / n.modifiers / n.groups /
  // n.dynamic / n.approval / n.on_failure_of) keep working.
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

// Log fetchers ask the server for raw NDJSON so the dashboard's
// logParser can read the structured event stream (run_start, step_*,
// exec_line, etc.) rather than re-parsing pretty-rendered text. With
// the structured shape we get accurate step bucketing in
// LogBucketView and each view mode (steps / inline) can format the
// breadcrumb on its own terms.
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

// searchRunLogs greps every node's log file in one run server-side
// and returns matching (node_id, line, content) tuples. Only matching
// bytes come over the wire -- the dashboard doesn't have to pull N
// node-log payloads to the browser to search them.
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
  return `${API_URL}/api/v1/runs/${runID}/logs/${nodeID}/stream?format=ansi`;
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

// searchRunsGrep is the dashboard counterpart of `sparkwing runs grep`.
// Walks the recent-runs window narrowed by the filter args and returns
// matching log lines across every (run, node) pair. Matching uses the
// displayed log body (msg / attrs), not the raw NDJSON framing.
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

// RunEvent mirrors store.Event on the wire. Payload is opaque JSON;
// consumers cast it per kind. The set of kinds is documented in
// docs/design/structured-sse-events.md — adding a new kind on the
// server is backward-compatible as long as clients tolerate unknown
// kinds (useRunEvents does, via a catch-all callback).
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

// --- Dark stubs: each becomes a real endpoint in a later session ---
//
// These exist so components that reference them still compile and
// render sensible empty states. See PLAN-web-restoration.md sessions
// B-I for the roadmap. Do NOT delete the types; delete the dark
// implementations as each endpoint lights up.

// Session F -- agents.
export interface Agent {
  name: string;
  type: string;
  labels: Record<string, string>;
  last_seen: string;
  status: string;
  active_jobs?: string[];
  max_concurrent: number;
}

export async function getAgents(): Promise<Agent[]> {
  const res = await authFetch(`${API_URL}/api/v1/agents`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  const data = await res.json();
  return data.agents || [];
}

// Session C -- pipelines registry.
export interface PipelineArg {
  name: string;
  type: string; // "string" | "bool" | "int"
  required: boolean;
  desc: string;
  default?: string;
}

export interface PipelineMeta {
  args: PipelineArg[];
  tags?: string[];
}

// Stops polling /api/v1/pipelines after the first 404 — the local
// dev server (sparkwing-local-ws) doesn't expose the pipeline
// registry, only the controller does, and the empty fallback is fine
// in both cases.
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

// Session E -- trends.
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

// Session B -- service health.
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

// Session G -- log search.
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
}

export function getLogsUrl(): string {
  // Logs service URL: defaults to same origin as the controller, since
  // `sparkwing web` proxies /api/logs/* to the logs-service when
  // configured with --logs. When running the dashboard against a
  // remote cluster, set NEXT_PUBLIC_LOGS_URL at build time.
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

// Session H -- metrics.
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

// Legacy alias kept so pre-rewrite components compile unchanged.
export type JobMetrics = NodeMetrics;

// Session I -- retry.
export async function retryRun(runID: string): Promise<Run | null> {
  const res = await authFetch(`${API_URL}/api/v1/runs/${runID}/retry`, {
    method: "POST",
  }).catch(() => null);
  if (!res || !res.ok) return null;
  return res.json();
}

// --- Deprecated: old "Job" type + functions kept as dark stubs so
//     pre-rewrite components still compile. Delete each as the page
//     that owns it is ported. ---

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
  // Map new Run[] into a minimal Job[] shape so old components get
  // something renderable. Fields not in Run are left undefined.
  const runs = await getRuns({ limit: 50 });
  return runs.map((r) => ({
    id: r.id,
    pipeline: r.pipeline,
    status: mapRunStatusToJobStatus(r.status),
    created_at: r.started_at,
    result: r.finished_at
      ? {
          success: r.status === "success",
          duration: runDurationMs(r) * 1_000_000, // ms -> ns (legacy shape)
        }
      : undefined,
  }));
}

function mapRunStatusToJobStatus(status: string): string {
  // Old Job model distinguished claimed/running/complete/failed.
  // New Run model collapses into running/success/failed/cancelled.
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

// Deferred to follow-up (breakpoints were never used in practice).
export async function getBreakpointStatus(): Promise<{ status: string }> {
  return { status: "" };
}

export async function continueBreakpoint(): Promise<void> {
  throw new Error("breakpoints not implemented");
}

// debug pause state for the paused-node dashboard panel.
export interface PauseState {
  run_id: string;
  node_id: string;
  reason: string; // pause-before | pause-after | pause-on-failure
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

// --- Approvals ---

export interface Approval {
  run_id: string;
  node_id: string;
  requested_at: string;
  message?: string;
  timeout_ms?: number;
  on_timeout?: string;
  approver?: string;
  resolved_at?: string;
  resolution?: string; // "approved" | "denied" | "timed_out" | ""
  comment?: string;
}

// getApproval returns the single approval row for (run, node), or
// null when the gate doesn't exist. Components polling a gate banner
// use this to pick up a resolution that happened in another tab.
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

// getPendingApprovals returns every unresolved approval across all
// runs, oldest-first. Backs the top-nav pending-approvals badge.
export async function getPendingApprovals(): Promise<Approval[]> {
  const res = await authFetch(`${API_URL}/api/v1/approvals/pending`, {
    cache: "no-store",
  }).catch(() => null);
  if (!res || !res.ok) return [];
  const body = await res.json();
  return body.approvals || [];
}

// resolveApproval writes a human decision onto a pending gate. The
// approver is populated server-side from the authenticated principal;
// the comment is optional. Throws on 409 (already resolved) so the
// caller can refresh and surface the winning decision.
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
