// Package localws is the single-process local dev server: one HTTP
// server, one SQLite file, one port. It runs the controller in local mode
// with the dashboard attached, so the CLI and the dashboard read from the
// same state.
//
// # Composition
//
// One [Run] call wires:
//
//   - A [github.com/sparkwing-dev/sparkwing/pkg/controller.Server]
//     (the run / node / event HTTP surface) backed by a sqlite
//     [github.com/sparkwing-dev/sparkwing/pkg/store.Store] at
//     <Home>/state.db.
//   - Node log reads from the run directories under <Home>/runs, or from
//     [Options.LogStore] when one is set.
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
// [storage.ArtifactStore] / [storage.LogStore] backends, and read-only
// mode for safe consoles.
//
// # Cluster mode
//
// Cluster deployments run the same controller with its dashboard and
// browser sign-in as `sparkwing-controller`.
// localws is the laptop equivalent.
package localws
