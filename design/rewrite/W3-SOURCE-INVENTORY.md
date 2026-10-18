# W3 source inventory

The baseline is HEAD `7d9792315`. Before edits, I searched production Go for
`clone`, `checkout`, `worktree`, `materializ`, `IsTypeScript`, `ReadPin`,
`ResolveRevisions` and `ResolveRefCommit`. A second search included TypeScript,
CI definitions and snapshot creation. Test fixtures and result/blob
materialization are separate from pipeline source acquisition.

Refreshed at `b4662bde5` before the native-admission continuation. The same
production search found 160 matches, including comments and result materialization.
The Git ownership guard passes. The table below still covers every production
source acquisition and directory-based SDK/engine selection path found.

| Baseline path | Source operation | Result in this worktree |
| --- | --- | --- |
| `internal/source/checkout.go` | Remote init/fetch/checkout, local clone/checkout, prepared checkout and separate pipeline-code checkout | Owns acquisition. Local and remote committed trees share depth enforcement and submodule/LFS materialization after seeding. |
| `internal/source/submodules.go` | Recursive submodule checkout and LFS fetch/checkout | Retained behind shared policy materialization. |
| `internal/source/bundle.go` | Authorized bundle download, digest verification and object import | Retained. Checkout object format follows the sealed SHA. |
| `internal/source/git.go`, `credential.go`, `command_unix.go`, `command_windows.go` | Git processes, credential-helper lookup and process joining | Process joining moved to the dependency-free `internal/source/gitcmd` leaf. |
| `internal/source/acquire.go` | Classifies acquired pipeline code, reads its pin, resolves/verifies its engine and installs its SDK | Owns unaccepted SDK classification. |
| `internal/source/projection.go`, `revisions.go` | Projects sealed execution/code identity and resolves intent | Retained. New `resolve.go` owns local/remote Git ref lookup. |
| `cmd/sparkwing/trigger_native.go` | Snapshot inspection, two direct checkouts and temporary inspection materialization | Direct checkouts deleted. Uses `PrepareCheckout` and `Options.LocalBundle`. |
| `cmd/sparkwing/run_dispatch.go` | Resolves a ref, adds/removes/prunes temporary worktrees | Direct worktree commands deleted. Those paths are deleted. Run flags retain the resolved selection and prepared environment. |
| `cmd/sparkwing/trigger_prepare.go` | Reads Git objects/config and resolves execution/pipeline refs | Uses `ResolveRevisions`. No clone/checkout/worktree command. |
| `cmd/sparkwing/trigger_inspect.go` | Acquires selected source and inspects its SDK entry | Uses shared acquisition. |
| `cmd/sparkwing/local_run.go`, `local_run_host.go`, `schedules_local.go` | Native CLI and local-schedule source preparation | Shared-source consumers. |
| `cmd/sparkwing/local_run_environment.go`, `action_plan.go` | Prepares captured input for help, describe, plan, preparation and burst | Withholds configured local secrets with procenv name identity before selection, acquisition or SDK installation. Run dispatch retains the prepared snapshot. |
| `cmd/sparkwing/run_source_selection.go` | Local execution/pipeline source selection | Uses shared resolution and acquisition. |
| `cmd/sparkwing/worktree_snapshot.go` | Captures working/index bytes as a synthetic commit and bundle, adds selected revisions | Retained as input capture. Inspection imports that bundle through `internal/source`. |
| `internal/orchestrator/ref_resolve.go`, `pipeline_source.go` | Legacy ref lookup and pipeline-source projection | Deleted after the merged CLI removed their last callers. Remaining ref and option-injection coverage lives in internal/source. |
| `internal/bincache/source_bundle.go`, `source_import.go`, `source_import_unix.go`, `source_import_windows.go` | Unused alternative bundle downloader/importer/checkouter, snapshot validation and raw-file restoration | Import API and its support files deleted. Upload API retained. No production caller needed migration. |
| `internal/bincache/materialize.go`, `entry.go`, `bincache.go` | Materializes compiled pipeline entries from supplied source | Retained compiler/cache responsibility. These do not select pipeline commits. |
| `internal/githooks/fire.go` | Adds/removes/prunes the throwaway worktree used to prove a hook | Delegates worktree acquisition to `source/gitcmd.HookWorktree`. Hook commits stay in githooks. |
| `sparkwing/git/clone.go` | Public Go SDK job helper clones an arbitrary user-requested repository | Delegates clone execution to `source/gitcmd.CloneRepository`. Public SDK API and behavior remain. |
| `sdk/typescript/src/helpers/git.ts` | Public TypeScript SDK job helper constructs and runs `git clone` | Unchanged W5 surface. It is outside the Go package guard and needs a lead/W5 decision if the ownership rule includes SDK job helpers across languages. It does not acquire pipeline code or select an engine. |
| `internal/wingd/localcontroller/native.go` | Detects the SDK from the live local directory before acquisition | Detection deleted. Accepted engine SDK is retained. Unaccepted SDK is detected after checkout by Acquire. |
| `internal/nodehost/native_host.go`, `owner.go`, `claimed_node.go` | Host acquisition and binding of the selected entry | Retained shared-source consumers. |
| `internal/runners/repository.go`, `internal/runners/hosting.go`, `internal/runners/launcher/controller.go`, `internal/runners/launcher/source_init.go`, `internal/cluster/production_runtime.go` | Runner/launcher source preparation and SDK entry installation | Shared-source consumers. Production Go ownership guard covers these packages. |
| `pkg/controller/github_app_webhook.go` | Builds App intake with a branch in Source.Ref | Resolves the verified execution/code pair before admission without looking up a newer branch. |
| `pkg/controller/schedule_source.go` | Local rev-parse or remote ls-remote followed by pair selection | Bespoke Git lookup deleted. Uses shared `Select`, including execution-commit configuration and the configured code pin. |
| `pkg/store/schedule_intake.go` | Builds pinned scheduled requests and replay digests | Pinned identity resolves through the shared pair selector. Replay carries both committed IDs. |
| `pkg/controller/client/native_services.go` | Fresh-branch child clears parent SHAs and submits unresolved intent | Fresh source flags are cleared here. Controller child intake resolves against the parent's stored location before sealing. |
| `pkg/controller/child_source.go` | Added authenticated child-ref resolution and server-private credential lookup | Uses shared ref/pair resolution. Credentials never enter a running parent. |
| `cmd/sparkwing/action_plan.go`, `compile.go`, `run_help.go` | Preview/help materializes pipeline code and chooses its SDK from a directory | Preview, describe and help use shared acquisition with the prepared environment. SDK selection follows the acquired directory. |
| `cmd/sparkwing/toolchain_select.go`, `describe_cache.go` | Selects local engine or SDK-dependent description identity | W9's preview/describe selection review remains with W9. |
| `cmd/sparkwing/cache.go`, `cache_warm.go` | Classifies directories and reads pins for explicit cache management | Retained management surface. |
| `cmd/sparkwing/upgrade.go`, `info.go` | Classifies directories for explicit upgrade or informational output | Retained management/information surface. |
| `internal/repos/resolver.go` | Classifies directories for repository/entry discovery | Retained discovery surface. |
| `internal/pipelineentry/pipelineentry.go` | Pure manifest and directory classification | Retained classifier used after acquisition. |
| `internal/engine/pin.go`, `install.go`, `sdkinstall/install.go` | Reads the supplied directory's SDK pin and installs through the selected engine | Retained engine installation responsibility. |
| `internal/engine/upgrade.go` | Stages source/config and reads pins before/after an explicit SDK upgrade | Retained explicit upgrade responsibility. |
| `internal/sparks/overlay.go` | Materializes dependency overlays and sums | Retained dependency-management responsibility. |
| `pkg/controller/burst_sdk_source_test.go` | Test fixture copies tracked working bytes into an owned SDK dependency directory | Test-only source capture. It excludes transient untracked files created by concurrent TypeScript tests. Execution still uses shared acquisition. |
| `.github/workflows/*`, `.sparkwing/*` | CI checks out/builds the repository through GitHub actions and pipeline tooling | Build infrastructure, outside production application packages. |

`TestGitAcquisitionHasOneOwner` parses production Go packages and refuses Git
clone/checkout/worktree arguments outside `internal/source`. Its detector tests
cover direct argv, argv slices and append, helper calls and shell commands.
The TypeScript helper above is an explicit remaining cross-language boundary.
