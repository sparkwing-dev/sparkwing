// Package logs is the sparkwing-logs service: an HTTP frontend over
// file-per-node log storage. Workers POST log bytes as they stream;
// the dashboard and CLI fetch them for display.
//
// # Why a separate service (not the controller)
//
//   - Controller state is structured + small + queryable; logs are
//     unstructured + large + append-heavy. Different storage, different
//     access patterns.
//   - Logs scale with pipeline volume; controller DB shouldn't.
//   - In prod the logs service can back to S3 / gitcache / blob store
//     without touching the control plane.
//
// # Surface
//
// [Server] is the HTTP frontend: construct via [New], optionally
// wrap with [Server.WithControllerAuth] for token-validating
// middleware, and serve via [Server.Handler]. [Client] is the
// matching HTTP client: construct via [NewClient] (or
// [NewClientWithToken] for authenticated callers) and call
// [Client.Append], [Client.Read], [Client.ReadFiltered],
// [Client.Stream], [Client.ReadRun], or [Client.DeleteRun].
// [ReadFilter] narrows reads server-side; [AuthError] is the typed
// failure path for auth rejections (preserves the missing scope so
// callers can prompt the user precisely).
//
// # Resource bounds
//
// [Limits] caps stored bytes per node and per run, caps one line, holds
// the free-space floor below which appends are rejected, sets the
// retention the sweeper enforces, and bounds one search request.
// [DefaultLimits] carries the shipped values; [Server.WithLimits] and
// [ServeOptions] replace them. A node or run that reaches its cap gets
// [TruncationMarker] appended once, a line past MaxLineBytes is stored
// cut to the cap with [LineTruncationMarker] in place of its tail, an
// append whose control-byte share runs past BinaryRatio is dropped with
// [BinaryDropMarker], and a search stopped by a budget reports Truncated
// on its [SearchResponse].
// A search for a missing run returns the same 404 as a run outside the
// caller's team, including when the run has been archived.
//
// # One service per process
//
// The store ceiling's Prometheus collector reads a package-level
// pointer to the ceiling of the most recently constructed [Server], so
// /metrics describes one logs service: the deployment shape. A process
// that builds a second Server keeps serving both, and the gauges follow
// the later one.
//
// # Storage shape (v1)
//
// One file per (run_id, node_id) under `root/runs/<run_id>/<node_id>.log`,
// raw bytes appended on POST, whole file returned on GET. Fine for
// laptop iteration; clustered prod will grow this out.
package logs
