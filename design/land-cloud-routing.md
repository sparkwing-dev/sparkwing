# Invocation routing and the local landing boundary

An explicit `sparkwing run PIPELINE --sw-route cloud --profile NAME` captures
and inspects source locally, then uses the existing controller dispatch path
with the `class=cloud` runner selector. It sends no burst preferences and
requires no here owner or local execution controller. The machine-wide burst
path retains its existing behavior. The repository can still require local
execution.

The CLI accepts a Cloud profile for this invocation because the user selected
Cloud. Repository configuration still cannot authorize spending, requester
secret sources or untracked files. A repository Cloud preference would need a
new machine authorization contract, so this change uses invocation flags.
There are no SDK, controller API, wire or store changes.

## Customer-visible decisions for maintainer approval

- Add `--sw-route local|cloud` and permit `--profile NAME` and
  repository `allow_secret_files` with the explicit Cloud route. Cloud supports non-interactive foreground observation and
  `--sw-detached`. Defaults remain local unless the machine opted into burst.
- Explicit Cloud selects Cloud immediately and fails when connection or
  upload fails. It never falls back to desktop work. Existing machine burst
  routing keeps here/machines preferences and pre-intake local fallback.
- Direct Cloud jobs use normal team credits and admission rules. They do not
  consume the machine's burst overflow allowance. The existing runner selector
  path already has this behavior through `pipeline trigger --runner cloud`.
- Interrupting direct Cloud observation leaves the run active. Cancel it
  explicitly with `sparkwing runs cancel --profile NAME --run ID`. Machine
  burst's attached owner still requests cancellation on interrupt.
- Requester secrets follow existing burst selection, scanning, masking and
  in-memory storage. Working-tree runs receive only team shared secrets.
  Direct Cloud has no here owner to restore requester values after a controller
  restart. If values are lost, a new submission must resolve them again.

GitHub Actions selects hosted or self-hosted execution per job using
[`runs-on`](https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/choose-the-runner-for-a-job).
CircleCI selects the execution environment and compute size per job using
[executors and `resource_class`](https://circleci.com/docs/reference/configuration-reference/).
Those selectors do not describe workstation-first overflow. This change makes
explicit Sparkwing Cloud selection behave as a destination, while keeping
workstation-first overflow as a separate machine preference. Sparkwing's
requester-secret relay and restart behavior remain its existing contract.

## This repository

`land -- --cloud-profile cloud` keeps preconditions, source policy, review,
verification and push local. A quarter-core inline job invokes the product CLI
to run `gate --sw-route cloud --profile cloud -- --full --base SHA` and waits for its
successful receipt. The parent records that Cloud run ID as `gate_run`.
The remote graph includes policy, broad checks, race tests and the tree record.
The wrapper names this repository's tracked secret-shaped fixtures with
`.sparkwing/sparkwing.yaml` `allow_secret_files`, because the upload refuses them otherwise.
The wrapper checks clean, unchanged HEAD before and after the gate and allows
90 minutes, including remote preparation and observation. Normal local land
keeps its affected check selection.

The local parent passes the exact public `origin/main` SHA that preconditions
fetched. Each remote job fetches that commit over HTTPS through the repository
pipeline's `--base SHA` input, then compares its snapshot tree against that base
without requiring common ancestry. Policy and lint therefore ignore historical
findings. Full mode still runs every package. The two heavy jobs each request
four Cloud cores, matching their GitHub Actions lanes. The source bundle keeps
its parentless format, and no developer Git credential is uploaded. Cloud does
not need the developer's push credentials or
local review record. If main moves afterward, the existing merge-delta build
and tests still run locally. A complete remote merge-delta recheck would need
its own tree handoff and is outside this change.

Assumptions and mechanisms added: invocation flags choose an existing product
execution path, a land input chooses an existing product CLI, and the repository
gate input `--base SHA` fetches a public comparison commit in each job. Passing only
`--full` was insufficient because source-policy and lint filters require a
comparison tree. This repository-specific fetch avoids widening generic source
uploads to include Git history. Removing the flags and the optional Cloud gate branch returns to machine-wide burst and
local land. No new environment variable or background service is introduced.
