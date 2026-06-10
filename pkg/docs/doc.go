// Package docs ships the sparkwing user-facing documentation as
// embedded markdown so the CLI is offline and version-locked to the
// running binary. Source of truth is repo-root /docs/; this package
// embeds a mirror under mirror/.
//
// The CLI's `sparkwing docs read`, `docs list`, `docs all`, and
// `docs search` verbs delegate to [Read], [List], [All], and
// [Search] here. Each doc topic is described by an [Entry].
// Unknown slugs return [ErrNotFound].
//
// Cross-doc markdown links (`[label](slug.md)`) are rewritten on
// read into actionable CLI commands so terminal-rendered output
// stays navigable without a browser.
package docs
