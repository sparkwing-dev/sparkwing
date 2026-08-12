# Documentation style guide

How sparkwing's docs stay accurate without a docs team. The core idea:
**generate as much of the reference as possible, hand-write only the
small conceptual layer, and gate everything.** Prose drifts; derived
docs don't.

## The layers

| Layer | Source | Where |
|---|---|---|
| **Reference** | generated from code | `docs/cli-reference.md` + `docs/cli-<group>.md` (command registry), `docs/config-reference.md` (schema structs), `docs/sdk-reference.md` (the `sparkwing` package via go/doc), `docs/api-reference.md` (controller + logs route registrations). Add more; don't hand-maintain reference. |
| **Executable examples** | compile-checked | every ```` ```go ```` block in `docs/` is compiled against the SDK; every ```` ```yaml ```` `pipelines:` block is parsed by the real config parser. |
| **Concepts** | hand-written, small | the *why* and the model (execution, profiles, the two-layer DAG, caching). Keep these short. |
| **Tutorials** | real templates | `sparks-core/templates` + `sparkwing pipeline new`; the `template-verify` pipeline scaffolds, builds, lints and explains every registry template (and runs the runnable-tier ones), and the release pipeline gates on it. Prefer "scaffold this template" over prose steps. |
| **Architecture** | near code | contributor docs (`DESIGN-*.md`, `architecture.md`). |

One question per page: tutorial = *how do I?*, reference = *what does it
do?*, concept = *why?*, architecture = *how is it built?*. A page that
answers several is hard to keep correct.

## Source of truth

- **Edit `docs/`.** It is the canonical tree and the source the
  sparkwing.dev site builds from. `pkg/docs/mirror/` is a generated copy
  the CLI embeds (`go:embed`); regenerate it with `bash bin/sync-docs.sh`
  and commit both. **Never hand-edit `pkg/docs/mirror/`.** To report drift
  without touching the tree -- reviewing someone else's branch, say -- run
  `bash bin/sync-docs.sh --check`, which writes nothing and exits non-zero
  listing what differs.
- **Generated reference pages are generated.** Every page in the
  Reference row carries a "do not edit" banner naming the code it
  derives from and the script that regenerates it. To change one, change
  that code (the command registry in `cmd/sparkwing/help_registry.go`,
  the schema structs in `pkg/pipelines` / `pkg/projectconfig`, the
  `sparkwing` package, or the route registrations in `pkg/controller` /
  `pkg/logs`) and run the `bin/gen-*-docs.sh` the banner names. pre-push
  regenerates each one and fails if the committed file is stale.

## Writing rules (hand-written pages)

- **Present tense; describe what IS.** No history, rename, or
  deprecation narrative ("used to", "formerly", "post-rewrite",
  "deprecated") -- that belongs in `docs/migrations/` or the CHANGELOG.
- **No frozen counts or closed lists over an open set** ("three places",
  "four checks"). The count is wrong the moment the code grows the set.
  Describe the mechanism, or link a generated list.
- **One term per concept.** Pick *runner* (not agent/worker), *pipeline*,
  *job*, *work*/*step*, *plan*, *trigger*, *profile*, *backend* -- and
  use it everywhere. Terminology sprawl is what makes docs read like a
  committee.
- **Every example compiles / parses.** Don't paste illustrative-only
  code; the gate rejects it.
- **No internal pointers** -- no ticket IDs, commit hashes, or "see
  Slack". They rot for outside readers.

## What enforces this

`internal/doccheck` runs in pre-push over `docs/` (the source) plus the
CLI help registry, and is the durable guard:

- go blocks compile against the in-repo SDK;
- `sparkwing.yaml` blocks parse through the real strict config parser;
- a denylist of dead tokens (renamed flags, old file/path names);
- history / deprecation narrative;
- failure-reason completeness (every `pkg/store` `Failure*` constant is
  documented in `observability.md`);
- frozen counts over open sets;
- sidebar completeness (`docs/_sidebar.json` and the docs tree agree in
  both directions, so a new page can't be published unreachable);
- aux docs: the root-level markdown (README, DELIVERY, VERSIONING,
  DESIGN-*, ...) and the charts/install/web READMEs get the drift subset
  (dead tokens, CLI-verb resolution, `.md`-link resolution); the
  pipeline registrations in `.sparkwing/` and `examples/*.yaml` get dead
  tokens and CLI-verb resolution over the `sparkwing ...` commands their
  help strings and workflow steps quote, since link resolution is a
  markdown-only check. The editorial checks stay `docs/`-only:
  VERSIONING.md documents removal and breaking-change policy and
  DESIGN-* docs record design evolution, so change vocabulary is their
  subject matter. The CHANGELOG is excluded outright -- recording dead
  names is its job.

Plus: a pre-commit check that `docs/` and `pkg/docs/mirror/` are in
sync, and pre-push drift gates that regenerate the generated reference
pages and fail if the committed file is stale.

## Adding a new generated reference

1. Render markdown from the code source (a registry, a struct) to
   stdout.
2. Write a `bin/gen-<name>-docs.sh` that redirects it to
   `docs/<name>.md` and runs `bin/sync-docs.sh`.
3. Add a pre-push drift gate: regenerate and `diff` against the
   committed file.
4. Register the slug in `docs/_sidebar.json` and link it from the
   relevant concept page.
