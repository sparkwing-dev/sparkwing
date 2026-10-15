// Package client is the HTTP StateBackend implementation orchestrator
// pods (and any other consumer) use to talk to a running
// sparkwing-controller. Each method maps 1:1 to a controller endpoint.
//
//	c := client.New("http://controller:4344", nil)
//	runs, err := c.ListRuns(ctx, store.RunFilter{Pipeline: "build"})
//
// # Construction
//
// [New] creates a default client with a 30s timeout. [NewWithToken]
// wires a bearer token for shared-secret auth in cluster mode;
// callers running against laptop mode (no auth) pass an empty token.
//
// # Wire types
//
// Inputs and outputs use the [store] data model directly
// ([store.Run], [store.Node]). Request types
// specific to the HTTP surface live here: [TriggerRequest] /
// [TriggerResponse] for pipeline invocation, [AcquireSlotRequest] /
// [AcquireSlotResponse] / [HeartbeatSlotResponse] for the
// concurrency admission protocol, [Secret] for vault reads,
// [HeartbeatStatus] for runner liveness, [GitMeta] and
// [TriggerMeta] for run-origin context.
package client
