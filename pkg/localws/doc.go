// Package localws is the single-process local dev server: one HTTP
// server, one SQLite file, one port. It runs the controller in local mode
// with the dashboard attached, beside a logs service, so the CLI and the
// dashboard read from the same state.
//
// # Composition
//
// One [Run] call wires:
//
//   - A [github.com/sparkwing-dev/sparkwing/pkg/controller.Server]
//     (the run / node / event HTTP surface) backed by a sqlite
//     [github.com/sparkwing-dev/sparkwing/pkg/store.Store] at
//     <Home>/state.db.
//   - A [github.com/sparkwing-dev/sparkwing/pkg/logs.Server] for log
//     reads and writes, rooted at <Home>/logs.
//   - The embedded Next.js dashboard bundle, which the controller serves at
//     / in local mode: no sign-in pages, with the serve token as the
//     credential.
//
// All three live on the same mux at [Options.Addr] (default
// 127.0.0.1:4343), so `sparkwing run <pipeline>` and the dashboard
// in the browser observe the same state in real time without log
// forwarding or cross-process coordination.
//
// # Embedding
//
// [Options] tunes the composition: pre-built listener, alternative
// [storage.ArtifactStore] / [storage.LogStore] backends (e.g. S3 for
// shared-state mode), read-only mode for safe consoles, and
// NoLocalStore to drive the dashboard purely from object-storage
// reads without opening the local sqlite.
//
// # Cluster mode
//
// Cluster deployments run the same controller with its dashboard and
// browser sign-in as `sparkwing-controller`, beside `sparkwing-logs`.
// localws is the laptop equivalent.
package localws
