# W7 storage continuation

<!-- design:begin -->

## Implemented

Starting HEAD is `5bb42357e`. The checkout was clean. No merge conflicts remain.
W3's hosted_run, trigger_host and pod_node_host deletions remain intact.
No Git write was made.

Remote cache construction now takes the authenticated team and the complete
`DataBinding.Authority`. Every record lookup, verification download and artifact
read carries that exact authority. Returned records must match the selected team.
The old four-line partial-authority constructor is deleted.

This is a W7-local contract correction: `RemoteCacheOptions.AuthorityRef` becomes
`Authority`, and `Team` is required. The explicit repository still must match the
bound authority. No foreign interface changed. No production caller constructs
these options yet. The existing unit and acceptance callers were migrated.

The invariant is that remote-cache reads retain the exact bound repository
identity and cannot admit a foreign tenant's record. The existing selector fuzz
property now checks complete authority on both record and byte operations,
including artifact reads. Existing generated remote-cache checks now mutate the
returned team as well as repo, ref, provenance and member metadata. With the
former partial-authority behavior restored temporarily, all twelve seed cases
fail. The corrected implementation passes them and 1,000 generated executions.

## Real integration gaps reproduced

A temporary extension of the existing SQLite remote-cache cell tried W2's real
`DataController.BindData`. It exposed three mismatches. The W7 binding mismatch
above is fixed. Two W1/W2 obligations remain:

- **W1/W2 account-scoped data grants.** W2's remote reader requires an account
  credential with `data.read`. W1's `readAuthorityTx` intersects an unbound account
  capability with `ScopesForRole`. No role includes `data.read`, so even an
  explicitly scoped reader loses it. A persisted account, membership and
  repo-bound reader therefore fail `ResolveBlobAuthority` with
  `blob grant and data scope are required`. A machine fixture passes W1 but W2
  correctly refuses it as a repo-reader credential. W1/W2 must agree how current
  membership preserves the explicitly granted repository-scoped read authority.
  W7 has not widened membership grants or substituted a machine for a user.
- **W1 server-default record selection (A1-29).** The remote-cache contract asks
  `LookupRecord` to select a verified default ref without accepting the local
  ref. After an explicit verified key succeeds, omitting that ref with
  `VerifiedDefaultOnly=true` fails with
  `empty or invalid record namespace component`. `lookupRecordTx` constructs
  `RecordNamespace` before selecting a ref. W1 must implement the contracted
  server-side selection, or propose a contract amendment. W7 has not guessed a
  default branch or copied W1's fallback logic.

The temporary integration probes were removed after recording their failures.
The existing SQLite cell retains its valid direct-tenant evidence. Authenticated
remote-cache proofs still use contract fakes. No acceptance cell, registry or
scenario catalog was added. These two seams prevent replacing those fakes with
real authenticated reader bindings.

## Legacy deletion and production lines

W7's net production Go delta against main is **-6,868 lines**, measured with the
lead's area prefixes and exclusions for tests, testdata, acceptance and contract
files. The budget is -6,630, leaving **238 lines within budget**.
This session adds 14 and removes 16 production lines, net **-2**.

The cache command/service, gitcache, package proxy, cache chart, raw S3 output
and log backends, run-directory log fallback, direct source-fetch clients and
credential transport remain deleted. Historical signed release assets remain.
No legacy file was restored. The existing deletion manifest records prior cuts.
W1 still owns the remaining OutputRef and node_outputs/data_objects metadata cut.

## Verification

All Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-gocache`, and scratch beneath `/tmp/sw-astra-W7`.
Tests use `-p 4` or less. Pipelines and independent review remain with the lead.

- Whole-repository `go build ./...`: pass.
- Tests for blob/..., artifacts, depcache and pkg/storage/...: pass.
- Selector fuzzing: pass, 1,000 executions with four workers.
- Vet on blob/... and tests/acceptance: pass.
- Existing `TestStorage`: pass, 102.05 seconds, 70 pass events, 424 skips,
  zero failures. The existing remote-cache component cell includes the new tenant
  mutation. Native skipped cells remain unclaimed.
- `internal/doccheck`: pass.
- `commentcheck -base main .`: clean across 2,184 files.
- `git diff --check`: pass. Git identity and core.worktree configuration unchanged.

No archived test port was added. The existing properties were strengthened.
Native HTTP/api.sock, Windows/Darwin, PostgreSQL, placement, deployment, release
and real-repository proofs remain unclaimed.

## Remaining handoffs

A1-29/30/31/33 retain their implementations, with A1-29's server-default selection
and A1-30's authenticated reader integration gaps identified above. A1-24/32/34/35
writer consumers retain the real tenant store and controller binding from the
previous session. A1-36/37 storage consumers retain the host and SDK contracts.
A1-43's historical release assets remain intact. A1-50 production composition
and A1-53 native evidence remain with their assigned owners. Lead-owned handoffs
and amendments were read and left unchanged.

The previous foreign seams remain open:

- W1/W2 need a tenant-authorized selected-output digest projection in the run
  snapshot before W1 can remove OutputRef and its legacy metadata readers.
- W2 needs authenticated `blob.LogTransport` and `SegmentRecorder` client
  bindings, including the uncertain-upload route using `logseg.UploadVerifier`.
- W3/W8/W9 must supply fresh data/log bindings per attempt and owner generation,
  remote-cache opt-in, and source-bundle intake in production host construction.
- W1 needs an elected, fenced expired-ticket selector before physical staging
  cleanup can consume it. W7's deletion backends are ready.

Materialization remains closed by W1b's prior real controller/host byte-copy
proof. This session did not change that path.

## Shared files and assumptions

There are no shared production edits. Owned source changes are limited to
`internal/blob/remote.go`, `types.go` and `README.md`. The migrated test callers
are `internal/blob/remote_test.go`, `remote_selector_test.go`, and
`tests/acceptance/storage_remote_test.go` and `storage_remote_archives_test.go`.

No new interface, wrapper, environment variable, credential policy or retry
mechanism was added. The full authority and team replace reconstructed identity
because W2 compares every operation with its persisted binding. No compatibility
field remains. Reverting these local option changes would restore the partial
identity bug.

Friction: earlier fake-boundary proofs concealed the account-grant and default-ref
selection gaps. Their implementation belongs to W1/W2. W7 continues against the
contract rather than introducing a second authorization or fallback owner.

Scratch `/tmp/sw-astra-W7` is removed at exit. Shared `/tmp/sw-gocache` is retained.

<!-- design:end -->

STATUS: CONTINUE
