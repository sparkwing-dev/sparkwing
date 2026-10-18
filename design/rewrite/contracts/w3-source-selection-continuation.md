# W3 source selection

<!-- design:begin -->

`runspec.SealRequest` refuses executable requests unless `Git.SHA` and
`Source.Ref` are full hexadecimal SHA-1 or SHA-256 object IDs. Ref intent stays
in `Git.Ref` and `Source.RequestedRef`. `source.ResolveRevisions` chooses the
execution/code pair. Verified App commits pass through without a branch lookup.
`source.ResolveRef` supplies local and authenticated remote lookup for that
resolver. Schedules use it too. Origin-qualified refs use one normalization:
`origin/main` and `refs/remotes/origin/main` identify `refs/heads/main` remotely
and the corresponding `refs/remotes/origin/main` locally. Explicit branch and
tag refs keep their namespaces. An unborn local checkout remains valid for
unsealed inspection, but cannot pass executable intake without a resolved
commit. Local working-tree execution with an existing HEAD retains its live
files and carries that resolved commit in both identities.

Fresh-branch children submit intent through the authenticated child-intake
route. Before sealing, the controller resolves it against the parent's stored
repository and acquisition location. A different repository or directory is
refused before lookup. Resolution uses controller-held App or team credentials.
They never enter the running parent's process. The existing refusal to release
source credentials to a started node remains intact. Admission rechecks the
parent's current authority after resolution. Fresh selections clear a parent
bundle and working-tree mode, and acquire committed source.

Native service assembly retains the accepted engine's SDK. For unaccepted work,
it leaves SDK selection to `source.Acquire` after checkout. Execution and code
checkouts share submodule and LFS materialization. A committed local checkout
uses its original remote and live source access for those operations, while
preserving the owner's files.

`source.Options.LocalBundle` supplies a host-owned absolute bundle file for
local snapshot inspection. It requires a local checkout and cannot combine with
`PreparedCheckout`. It imports objects before the shared committed checkout.
Remote bundle acquisition chooses the object format from the sealed object ID.
Selectors resolve against the original repository, including local branches,
origin-qualified refs, tags and configured code pins. Working-tree selection
reads configuration from the captured commit through the same acquisition owner.
Inspection passes the original checkout and resolved execution/code pair to
`source.Acquire`. Native trigger and burst pass the captured bundle through
`LocalBundle` when needed. Shared acquisition owns both committed workspaces and
their cleanup, while the submitted request retains its remote acquisition
location. There is no execution-only inspection replica or origin rewrite.
Acquisition fetches only resolved commit IDs and the tags an explicit checkout
policy requests. It never imports operational reference namespaces.
`Options.LocalReplica` and the wildcard namespace importer are deleted.
`procenv.Git` forces `GIT_NO_REPLACE_OBJECTS=1`; source Git also sets
`core.useReplaceRefs=false`. Replacement rules cannot change a sealed commit's
contents. Acquisition reads the checked-out HEAD after materialization.

The default committed configuration reader acquires the selected execution
commit through `PrepareCheckout`, then reads through `os.Root` at that acquired
root. It follows contained directory and file symlinks, including `.sparkwing`
pointing at `ci`, and refuses reads outside the checkout. Configuration reads
never install an SDK. The trusted `ReadFile` adapter must provide the same
committed-path semantics.

Local and remote acquisition finish through one checkout owner. After seeding,
it applies the accepted depth to the selected commit with a local file-protocol
fetch from the seeded repository, then checks out that commit and applies
submodule/LFS policy. Unpushed commits need no remote lookup. Execution and code
checkouts each apply their depth. Depth zero and full-history policies retain
the available ancestry.
Bundle pipeline entries must be directories. Failed transfers and cancelled Git
processes remove acquisition-owned workspaces.

The `internal/source` subtree owns Go Git clone, checkout and worktree execution.
Its `gitcmd` leaf supplies SDK clone and hook self-test operations without
importing the host's SDK/engine dependencies back into the SDK. The guard test
scans production Go packages and exercises its command-argument detector.
Fixture directories, generated dependencies and test files are excluded.
Binary-cache bundle acquisition and its unused SourceData/UploadSourceBundle
upload API are deleted. Source publication has one client entry point. Storage
prefix acceptance exercises the signed byte and record interfaces directly.

The source-credential response adds optional `extra_repositories`. C1 returns
`source.Credential` with the names from its audited App release. C2 and the
host client preserve that list for acquisition. The release rechecks both IDs
and names after minting. No route, schema, grant, setting or environment
variable was added.
The acquisition owner binds dependency preparation to its projected installation
environment through the Go toolchain session. Install hooks and dependency tools
consume that snapshot instead of capturing the calling process again. An explicit
toolchain session environment replaces an inherited snapshot when they differ.
Calls without a new environment retain the selected snapshot and notice sink.
Sparks resolver construction takes that context. Go setting probes and fallback
resolution read the captured session rather than os.Environ.

Source approval, snapshot capture, inspection and scanning require the prepared
environment. Snapshot capture builds its Git environment through `procenv.Git`
and retains it for later bundle operations and secret scans. Internal object,
index and commit-identity overrides are added to that environment. No snapshot
helper captures ambient values. Git can execute callbacks, so its process
environment remains filtered even before SDK installation. Profile-cache
lookups use the persisted request environment too. Default-branch lookup takes
the prepared masker so accepted proxy credentials remain supported and masked.

`client.UploadSourceSnapshot` requires `source.Publication`, carrying its final
bundle path, prepared process base and a nonnil withheld-secret classification.
`source.OpenPublication` copies the bundle into private storage and scans every
included blob object, including unchanged files and any bundled history, before
any upload request. Hashing and transfer read that scanned copy. Trigger passes
all configured held values. Burst passes those values plus resolved local secret
providers. A missing classification or masker fails closed. The scan retains
its eight-byte minimum and PEM-armor filtering. It never reports private paths
or values. The caller-specific changed-blob scanner is deleted.

Help, describe and plan share acquisition and prepared-environment withholding
with execution. Child local-secret inheritance remains part of the child request
contract.

<!-- design:end -->
