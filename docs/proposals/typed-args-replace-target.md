# Typed args, profile `default-args:`, and the end of first-class `--target`

Status: design draft. Targets v0.6.0 (breaking).

## Problem

v0.5.x exposes two first-class CLI flags for pipeline dispatch:

- `--profile NAME` -- storage / controller / token addressing.
- `--target NAME` -- pipeline-internal deployment env (dev / staging / prod).

`--profile` earns its keep: every command needs to address a backend. `--target` doesn't: only a handful of release/deploy pipelines have multi-target shape, but every pipeline pays the cost in help text, flag namespace, validation, and the `sparkwing.Target(ctx)` SDK accessor. Pre-commit, pre-push, lint, release-from-a-single-fleet -- none of them want `--target` anywhere near them, but it shows up in their help pages and `pipeline describe` output regardless.

There's also a related ergonomics gap: CI runs almost always know which target they're against (branch -> env, env-var -> env), but the framework offers no way to express "auto-resolve `--target` from this context" short of wrapping every CI invocation in shell that decides the flag value.

## Solution: three coordinated changes

### 1. Pipeline `args:` block in `.sparkwing/sparkwing.yaml`

Schema-bearing dispatch args (the kind that bind runners / source / secrets) move under a per-pipeline `args:` map. The pipeline author names them; the framework treats them uniformly.

```yaml
pipelines:
  - name: release
    entrypoint: Release
    on: { push: { branches: [main] } }
    args:
      target:                          # name is author's choice
        dev:  {}
        prod: { runners: [my-pool], source: prod-secrets, protected: true }
      region:                          # nothing stops you from having two
        us-west-2: {}
        eu-west-1: { runners: [eu-pool] }
```

Each value under an arg is the existing `pipelines.Target` shape (runners / source / values / approvals / protected / backend / secrets). Pipelines with no schema-bearing args simply omit `args:` and pay nothing -- no flag, no help-text noise, no validation.

### 2. Free-form typed inputs stay in Go, gain `required-when:` and `bind:` tags

The existing `Inputs` struct system stays. Two new struct tags:

```go
type ReleaseInputs struct {
    // bind:"target" tells the framework this string field IS the value
    // of the args.target dispatch key -- resolves runners / source /
    // secrets / etc. from the YAML on flag set.
    Target string `flag:"target" bind:"target" desc:"deployment env"`

    // required-when:"local" -- this flag must be present when the
    // resolved profile is local (no controller). CI profiles auto-fill
    // this via default-args:; local invocations must pass it explicitly.
    Version string `flag:"version" required-when:"local" desc:"version to tag"`
}
```

- `bind:"<arg-name>"` -- this struct field is the value of the schema-bearing arg `<arg-name>`. Validates against the YAML enum and triggers the binding (runners/source/secrets) at run start.
- `required-when:"<context>"` -- the flag is required when the resolved context matches. Initial contexts: `local` (no controller in resolved profile), `remote` (controller set), `<profile-name>` (specific profile resolves). Composable: `required-when:"local,gha"`.

Pipelines that want fully YAML-driven dispatch (no Go struct) get a default inputs struct synthesized from the `args:` block. Pipelines that want fully Go-driven (no YAML schema-bearing args) can use `Inputs` as today and read values via `sparkwing.Inputs[T](ctx)`.

### 3. Profile `default-args:` block

Profiles can supply default values for any declared arg. The pipeline still owns the *declaration*; the profile owns the *default* for cross-pipeline ergonomics in that environment.

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  prod:
    controller: https://api-sparkwing.rangz.dev
    token: swu_...
    default-args:
      target: prod                     # `--profile prod` -> --target=prod implied
      version: ${VERSION}              # env interpolation, ${...} only

  gha:
    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
    default-args:
      target: staging                  # CI default; release pipeline can override
```

`default-args:` applies uniformly to YAML-declared `args:` and Go-declared `Inputs` fields. Unknown arg names emit a warning (typo'd default) but don't fail the run.

## Resolution chain

For any arg (whether from YAML `args:` or Go `Inputs`), the value resolves in this order, first match wins:

1. **Explicit CLI flag** (`--target prod`).
2. **Profile `default-args:`** for the resolved profile.
3. (future) **Arg-level `detect:`** -- per-arg env match, parallels profile detect.
4. **Required-when check** fires -- error with a clear message naming the arg, the unresolved context, and where defaults could be set.

`sparkwing profile` already prints the resolved profile + reason; extend it to print the resolved arg values and their source ("from --flag" / "from profile prod default-args" / "from env detect").

## Concrete worked example

```yaml
# repo: my-app/.sparkwing/sparkwing.yaml
pipelines:
  - name: release
    entrypoint: Release
    args:
      target:
        dev:  {}
        prod: { runners: [my-pool], source: prod-secrets, protected: true }
```

```go
// repo: my-app/.sparkwing/jobs/release.go
type ReleaseInputs struct {
    Target  string `flag:"target"  bind:"target"`
    Version string `flag:"version" required-when:"local"`
}
```

```yaml
# ~/.config/sparkwing/profiles.yaml
default: prod
profiles:
  laptop: {}                           # no defaults; --version required locally
  prod:
    controller: https://api.example.com
    token: swu_...
    default-args: { target: prod }
  ci:
    detect: { env_var: CI, equals: "true" }
    default-args: { target: dev, version: ${CI_BUILD_VERSION} }
```

Invocations:

```sh
# Local. Both args required (target has no default; version required-when=local).
sparkwing run release --target dev --version v1.2.3

# Default profile=prod -> target inferred. Version still required-when=local
# unless prod is treated as remote (controller set), which it is here -> ok.
sparkwing run release                          # target=prod, version=unset

# Override via flag.
sparkwing run release --target dev             # target=dev, version=unset

# CI: both auto-resolved from env detect + default-args.
sparkwing run release                          # target=dev, version=$CI_BUILD_VERSION

# Triggered remotely. Resolved args serialize into the trigger payload.
sparkwing pipeline trigger release --profile prod --target prod
```

## Transitive args and the arg dependency graph

Once args are first-class, pipeline composition (P1 invokes P2 via `RunAndAwait` or a future declarative spawn) raises a question: do P1's callers need to know P2 exists to set P2's required args? The flat-per-pipeline answer is yes, which is bad ergonomics. The framework already treats the run DAG as a first-class graph; args deserve the same treatment.

Two dimensions:

- **Vertical (composition).** P1's effective CLI surface = P1's own args + the transitive args of everything it composes, minus what P1 pre-fills, plus what P1 re-exposes.
- **Horizontal (dependency).** Within a pipeline, arg A's default can reference arg B's value, and arg C's `required-when:` can predicate on arg D. That's a DAG with the same shape as the node DAG, computed at registration time, validated for cycles.

### Composition is declared

Pipelines that invoke other pipelines declare the relationship in YAML. Same rationale as Go imports being explicit: explicit means the static graph is computable.

```yaml
pipelines:
  - name: release
    args:
      target:
        dev:  {}
        prod: { runners: [my-pool] }
    composes:
      - pipeline: notify-slack
        args:
          # Pre-fill from parent args. Go template, field access only.
          # Resolves after parent args are bound. Declares dependency
          # edges release.composes.notify-slack.message -> {target, version}.
          message: "shipped {{.target}} version {{.version}}"
        expose:
          # Re-export child's --webhook so release's callers can set it.
          # Renders in release's help; on name collision, prefixed.
          - webhook
```

Inline job composition (`sparkwing.Job(plan, ..., &OtherJob{})`) needs no declaration -- same plan, same arg context, no new CLI surface.

### Transitive union, computed at plan time

Effective arg set of P1 = P1's own `args:` + P1's Go `Inputs` + for each composed pipeline C: C's transitive args, minus pre-filled, restricted to `expose`'d. Recursively. Cycles in composition (P1 -> P2 -> P1) and cycles in dependency (A defaults from B, B defaults from A) both rejected at parse time with a clear error: `arg dependency cycle: release.a -> release.b -> release.a`.

### Inspection

```
$ sparkwing pipeline describe release --args
release
├── --target              required               bind:target {dev|prod}
├── --version             required-when=local    free-form
├── --webhook             required-when=remote   (re-exposed from notify-slack)
│                         default-args.webhook on profile=prod
└── composes notify-slack
    ├── --message         pre-filled "shipped {{.target}} version {{.version}}"
    │                     depends-on: target, version
    ├── --channel         default="#deploys"
    └── --webhook         forwarded from release.--webhook
```

`-o json` for tooling. `sparkwing run release --explain` runs the resolution chain in dry mode and prints the same view with resolved values + source-of-truth annotations ("from profile prod default-args", "from --flag", "from env detect", "still unresolved -- required-when=local fires here").

The describe view is the operator-facing answer to *"why is this arg being asked of me?"* -- the goal you called out.

### Required-when expressions

Beyond context labels (`local`, `remote`, `<profile-name>`), allow simple predicates on resolved arg values:

```yaml
args:
  slack-webhook:
    required-when: "target=prod"
  rollout-percent:
    required-when: "target in [staging,prod]"
```

Tiny expression language: `<arg>=<value>` and `<arg> in [<v1>,...]`. No arithmetic, no string ops, no compound logic. Anything more, compute it in the Go `Inputs` struct.

### Resolution order, refined

1. Explicit CLI flag.
2. Pre-fill from a parent's `composes.<child>.args:` (for the composed-pipeline case).
3. Profile `default-args:` for the resolved profile.
4. (future) Arg-level `detect:`.
5. Default from `default:` (literal or template, may depend on other already-resolved args).
6. `required-when:` predicate evaluation -- error if unresolved and the predicate matches.

Steps 2-5 run in topological order over the arg dependency DAG so each arg's resolution sees its dependencies' resolved values.

## Migration (v0.6.0)

Mechanical for most consumers. The v0.5.0 `targets:` block becomes `args.target:` with the same value shape:

```yaml
# Before (v0.5.x)
pipelines:
  - name: release
    targets:
      dev:  {}
      prod: { runners: [my-pool] }

# After (v0.6.0)
pipelines:
  - name: release
    args:
      target:
        dev:  {}
        prod: { runners: [my-pool] }
```

CLI flag stays `--target` for the conventional `target` arg name. Pipelines that pick a different name (`--env`, `--cluster`) use whatever they declare.

SDK changes:
- `sparkwing.Target(ctx)` keeps working as a sugar wrapper around `sparkwing.Arg[string](ctx, "target")`. Deprecated in v0.6.0, removed in v0.7.0.
- New tags `bind:"..."` and `required-when:"..."` on `Inputs` struct fields.
- New `Arg[T](ctx, name)` generic accessor for reading any resolved arg from a step body.

A `docs/migrations/v0.6.0.md` guide covers the `targets:` -> `args.target:` rename, `Target(ctx)` deprecation, and the new defaulting model. Migration code mod for the rename is straightforward (rename + nest one level).

The release pipeline's hard error from v0.5.0 ("multi-target pipeline requires `--target`") generalizes: "arg `<name>` has multiple values and no default; pass `--<name>` or set `default-args.<name>` on the resolved profile."

## Out of scope

- Removing the typed `Inputs` system in Go. We extend it; we don't replace it. Type safety is a feature.
- Allowing profiles to *declare* args (only default them). Cross-cutting flag declaration belongs to the pipeline author.
- Arg-level `detect:` blocks (per-arg env matching). Could land as a follow-up if profile-level defaults plus required-when don't cover enough.
- Hierarchical args. Flat namespace per pipeline. If you find yourself wanting `args.target.region`, declare two args.

## Open questions

- **`bind:` value set.** v0.6.0 ships `bind:"target"` because that's where the existing `targets:` block goes. Are there other schema-bearing args worth a built-in binding -- `bind:"runner"`? `bind:"profile"`? Or is `target` the only one and the rest should stay free-form? Leaning toward `target` only for v0.6.0; revisit when a second use case appears.
- **Env interpolation in `default-args:`** (`version: ${VERSION}`). Useful for CI, but introduces a tiny templating language. Limit to `${VAR}` plain references (no shell pipes, no expressions) and reject anything else at parse time.
- **`pipeline trigger` arg serialization.** Today `--profile X` and `--target Y` serialize into the trigger row. With arbitrary args, the trigger payload needs an `args: { name: value, ... }` map and the receiving controller has to schema-check before dispatch. Worth confirming the controller side accepts this shape before locking the design.
- **`required-when` context vocabulary.** `local` / `remote` / `<profile-name>` plus the new `<arg>=<value>` and `<arg> in [...]`. Should there be `required-when:"!ci"` (negation) or `required-when:"branch=main"` (env-condition)? Defer until a real use case shows up; the vocabulary can grow without breaking existing tags.
- **YAML-only pipelines.** If a pipeline declares `args:` but no Go `Inputs` struct, do we still synthesize one for `sparkwing.Inputs[T](ctx)`? Or expose only `sparkwing.Arg[T](ctx, name)` in that case? Probably the latter -- synthesis adds magic that doesn't pull its weight.
- **Composition declaration: required or optional?** If `composes:` is required, dynamic `RunAndAwait` to an undeclared pipeline becomes a runtime error (loud, debuggable). If optional, dynamic composition silently skips transitive-arg computation. Lean toward **required**, with `composes: [*]` as the explicit opt-out for dispatcher-style pipelines that genuinely call anything.
- **Forwarding policy: explicit or implicit-by-name?** Current sketch uses explicit `expose:`. Implicit (same-named child args auto-forward unless overridden) is more convenient but surprises callers on accidental collisions. Lean toward **explicit**; the inspection view makes the verbosity worth it.
- **Template language for pre-fills + `default:` expressions.** Go template restricted to field access (`{{.target}}`, no control flow) keeps the parser cheap and the audit story simple. Confirm before locking in -- a stricter `${arg}` interpolation is even simpler if the field-access dot syntax feels overweight.
- **`pipeline trigger` payload for composed pipelines.** When a parent triggered remotely composes a child, does the remote orchestrator resolve the child's args from the parent's pre-fills + the child's profile defaults at dispatch time, or does the trigger payload carry pre-resolved child args? Pre-resolved at the parent is more debuggable (single source of truth for what was meant); resolved at the child is more flexible (child can pick up updated profile defaults). Pick parent-side resolution unless the flexibility argument grows.
- **Cross-pipeline arg name collisions in `expose:`.** `release` composes both `notify-slack` and `metrics-push`, both declare `--webhook`. Either prefix automatically (`--notify-slack-webhook` / `--metrics-push-webhook`) or require the operator to rename via `expose: { webhook: notify-webhook }`. Probably support both: auto-prefix on collision, with explicit rename as the override.
- **Should `default-args:` interpolate values from other args?** Two-pass resolution within `default-args:` opens the door to confusion. The transitive arg DAG already handles cross-arg references via `default:` on the pipeline side; `default-args:` on profiles stays single-pass (literal or env interpolation only). If you find yourself wanting profile-side cross-arg expressions, the right pattern is probably `detect:` on a more specific profile.

## Cost estimate

Two-tier delivery makes sense:

**v0.6.0 (foundation, ~2-3 days):** the YAML `args:` block, `bind:` + `required-when:` tags, profile `default-args:`, the resolution chain (explicit -> profile defaults -> `default:` -> required-when), the `--target` -> generic args migration, `sparkwing.Arg[T]` accessor + sugar wrapper for `sparkwing.Target(ctx)`. No composition / transitive support yet -- `composes:` is parsed but inert.

**v0.6.x or v0.7.0 (transitive, ~4-6 days):** `composes:` becomes active, transitive arg union, pre-fill templating, `expose:` re-export, dependency-DAG cycle detection, the `pipeline describe --args` tree view, `sparkwing run --explain`. Includes the controller-side trigger-payload schema change for arg-rich `pipeline trigger`.

Splitting them means consumers can adopt the simpler v0.6.0 surface immediately without waiting on the harder composition design to settle. The transitive work then lands when the open questions above are answered.

The bigger cost across both tiers is the conversation: every consumer's release pipeline edits one config file plus (if they read `sparkwing.Target(ctx)` directly) one Go file for v0.6.0; composing pipelines additionally add `composes:` blocks in the transitive tier.
