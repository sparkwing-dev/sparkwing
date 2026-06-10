# Architecture rules

Boundaries and layering for the sparkwing tree. Edit this file to change what the architecture reviewer enforces.

1. **Nothing in this public repo references the private platform repo.** No import of or path reference to `../sparkwing-platform`, and no platform ticket IDs (`IMP-`, `CLI-`, `ISS-`, `FRG-`, `ORG-`, `REG-`, `RUN-`, `SDK-`, `LOCAL-`, `TOD-` followed by a number) anywhere — code, comments, docs, strings. This is a hard boundary.

2. **Respect module boundaries.** `internal/` is private to the module and must not be imported across module lines. `pkg/` is the importable surface — moving something into `pkg/` makes it public, so it needs a reason.

3. **Layering: `cmd/` wires, logic lives below.** `cmd/` packages parse flags and assemble dependencies; business logic belongs in `pkg/` or `internal/`. A `cmd/` file that grows real logic (parsing, orchestration, algorithms) is a violation.

4. **No `replace` directives or `go.work` in committed go.mod files.** These are local-iteration scaffolding the module proxy can't resolve. The one allowed exception is `.sparkwing/go.mod`'s dogfood self-replace to `..`. A change that reintroduces any other `replace`, or commits `go.work`/`go.work.sum`, is a violation. (Pre-push also enforces this deterministically — flag it early.)

5. **One term per concept.** Use the established vocabulary in identifiers and types: *runner* (not agent/worker), *pipeline*, *job*, *work*/*step*, *plan*, *trigger*, *profile*, *backend*. Introducing a synonym for an existing concept is a violation.

6. **New cross-cutting dependencies need justification.** A new third-party module, or a new package-level global/singleton that other packages reach into, is worth a finding unless the diff makes the need clear.
