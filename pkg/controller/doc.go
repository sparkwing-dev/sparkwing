// Package controller is the canonical sparkwing control plane: an
// HTTP service fronting the run / node / event / cache state store.
// The same code serves laptop mode (in-process, embedded by
// [github.com/sparkwing-dev/sparkwing/pkg/localws]) and cluster
// mode (standalone pod talked-to by short-lived orchestrators);
// mode is determined by which functional options the consumer sets,
// not by a build flag.
//
//   - Cluster mode wires [Server.AttachPool] + [Server.WithCostRate].
//   - Laptop mode wires [Server.WithArtifactStore] +
//     [Server.WithReconcileHook].
//   - The handler set is otherwise identical so the dashboard
//     frontend and CLI binary speak the same wire protocol against
//     either side.
//
// The matching HTTP client (StateBackend against a remote
// controller) lives in
// [github.com/sparkwing-dev/sparkwing/pkg/controller/client].
//
// # Construction
//
// [New] returns a [*Server] bound to a
// [github.com/sparkwing-dev/sparkwing/pkg/store.Store]. Tune with
// chainable options: [Server.WithDispatcher] (default
// [NoopDispatcher]), [Server.WithAuthenticator] (default no auth),
// [Server.WithQueueTimeout], [Server.WithSecretsCipher] (any
// [Cipher] implementation), [Server.WithGitHubWebhookSecret], and
// the mode-specific options above. Call [Server.Handler] to get the
// routed `http.Handler` to wire into a server.
//
// # Plug points
//
// [Dispatcher] decouples the HTTP surface from how runs actually
// launch -- pluggable so the same controller can drive in-process,
// pool-backed pod, or external systems. [Cipher] decouples secret
// encryption from any specific implementation so consumers can
// supply their own AEAD without depending on sparkwing's internal
// secrets package. [Authenticator] resolves bearer tokens to a
// [Principal] (with scope set checked per route).
package controller
