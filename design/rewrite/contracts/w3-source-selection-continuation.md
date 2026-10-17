# W3 source selection

<!-- design:begin -->

`runspec.SealRequest` refuses executable requests unless `Git.SHA` and
`Source.Ref` are full hexadecimal SHA-1 or SHA-256 object IDs. Ref intent stays
in `Git.Ref` and `Source.RequestedRef`. `source.ResolveRevisions` chooses the
execution/code pair. Verified App commits pass through without a branch lookup.
`source.ResolveRef` supplies local and authenticated remote lookup for that
resolver. Schedules use it too.

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
Bundle pipeline entries must be directories. Failed transfers and cancelled Git
processes remove acquisition-owned workspaces.

The `internal/source` subtree owns Go Git clone, checkout and worktree execution.
Its `gitcmd` leaf supplies SDK clone and hook self-test operations without
importing the host's SDK/engine dependencies back into the SDK. The guard test
scans production Go packages and exercises its command-argument detector.
Fixture directories, generated dependencies and test files are excluded.
Binary-cache bundle acquisition is deleted. Its upload API remains because
source publication still calls it.

No wire field, route, schema, grant, setting or environment variable was added.
No lead-owned handoff or amendment file changed. W9 owns preview/describe and
child local-secret inheritance.

<!-- design:end -->
