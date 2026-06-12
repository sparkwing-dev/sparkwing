// Package pipelines parses .sparkwing/pipelines.yaml, the registry
// that maps each pipeline's invocation name to the Go type that
// implements it plus its trigger rules, declared secrets, and tags.
//
// The file is intentionally a thin registry. The Plan itself, jobs,
// conditions, and per-step details all live in Go code; pipelines.yaml
// only holds metadata the controller needs before loading Go.
//
// # Loading
//
// Use [Load] to read from disk or [Parse] to read from any
// io.Reader; both return a [*Config]. Call [Config.Validate] before
// trusting the file's contents.
//
// # Shape
//
// [Config] is the top-level document with one or more [Pipeline]
// entries. Each Pipeline carries [Triggers], [SecretsField], optional
// [Target]s, and [PipelineValues] (the layered config-value surface).
// Triggers fan out by source: [ManualTrigger], [PushTrigger],
// [WebhookTrigger], [DeployTrigger], [PreHookTrigger],
// [PostHookTrigger], [PostCommitHookTrigger].
package pipelines
