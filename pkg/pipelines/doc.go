// Package pipelines parses the pipelines section of
// .sparkwing/sparkwing.yaml. The registry maps each invocation name to
// its Go entrypoint and dispatch metadata.
//
// The file is intentionally a thin registry. The Plan itself, jobs,
// conditions, and per-step details all live in Go code.
//
// # Loading
//
// Use [Parse] to read a standalone registry document from an
// io.Reader. It returns a validated [*Config].
//
// # Shape
//
// [Config] is the top-level document with one or more [Pipeline]
// entries. Each Pipeline carries [Triggers], [Guards], argument
// defaults, a profile selector, and runner requirements. Triggers fan
// out by source: [PushTrigger], [PullRequestTrigger], [WebhookTrigger],
// [PreHookTrigger], [PostHookTrigger], and [PostCommitHookTrigger].
package pipelines
