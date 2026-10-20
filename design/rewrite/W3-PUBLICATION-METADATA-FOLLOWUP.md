# W3 publication metadata and constant evaluation

All three findings in W3-review-1792294330-final.md are implemented. The latest
lead native report has zero FAIL entries for committed HEAD d15f7619b. This
session still needs the lead's commit, review and native run.

## Implementation and contracts

DESIGN: source publication. Invariant: withheld values cannot cross publication
in any included object payload or transported source metadata. One streaming
policy now scans every object type, the complete bundle header and serialized
Git/source metadata. Tree names, commit/tag messages, branch/tag refs and bundle
prerequisite descriptions receive the same policy as blobs. The existing
eight-byte minimum, encoded patterns and PEM-armor handling remain unchanged.
Errors expose neither values nor private paths.

Publication now carries Git and Source. UploadSourceSnapshot takes that value
instead of separate metadata arguments because the uploader must send the
metadata that passed the scan. Both production callers and every test caller
are migrated. The source-selection continuation records this local C3 amendment.
No wire or schema changed. No foreign contract change is needed.

OpenPublication creates its copy root through fssecure.MkdirPrivateTemp and its
bundle through fssecure.OpenFile before copying bytes. The GoDoc now names the
platform private-file policy. A Windows test puts TEMP/TMP under an inheritable
Everyone ACL and checks the returned directory and file security descriptors.
That test cross-compiles here. Native Windows execution remains unverified.

DESIGN: recovery-command checking. Invariant: complete constant recovery
invocations receive grammar validation regardless of construction. Doccheck
uses go/types constant values before classifying dynamic fragments. It resolves
named constants, aliases, repeated const declarations, conversions, local scope
and references across files in the same package. Used constants are checked at
their use sites so intermediate fragments do not become standalone commands.
Dynamic boundaries and explicit data contexts retain their existing treatment.

Earlier W3 amendment/handoff work remains covered by the committed
continuations, including retry-selection persistence and restart coverage.
Lead-owned handoffs and amendments are untouched. No implementation stub or new
cross-area integration gap blocks this slice. Socket-dependent native checks,
supported-platform QA and final acceptance remain with the lead.

## Deletion and shared files

Replaced 46 production lines, including the blob-only loop and per-file literal
evaluation. Added 129 production lines, net +83. W3-owned production against
main is +12,674/-26,135, net -13,461, below its -2,300 budget. No additional legacy
host file or archived test port needed removal. The old burst assertion that
allowed a known secret in a filename is replaced with refusal coverage.

Shared production files: cmd/sparkwing/{burst_native,trigger_native}.go,
internal/doccheck/sourcehints.go and internal/controller/client/source_upload.go.
Shared tests: cmd/sparkwing/burst_source_test.go,
internal/doccheck/sourcehints_test.go, internal/controller/client/source_publication_test.go,
internal/controller/source_upload_test.go and internal/controller/burst_acceptance_test.go.
These contain all migrations for the uploader signature. Source files belong
to W3. Shared prose: docs/{deployment,local-execution}.md, their generated
pkg/docs/mirror copies and contracts/w3-source-selection-continuation.md.

## Verification

All Go commands use go -C /home/bombuntu/.xwing/envs/1081/sparkwing with
GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4,
TMPDIR=/tmp/sw-astra-W3 and GOTMPDIR=/tmp/sw-astra-W3/gotmp. Tests use -p 4.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Vet for source, doccheck, controller/client, controller and cmd/sparkwing.
- Full source, doccheck and controller/client suites.
- CLI `-run '^(TestWorkingTreeTrigger|TestBurstSecretScan|TestBurstSource|TestBurstSnapshot|TestTriggerSnapshot)'`.
- Controller `-run '^(TestSourceSnapshot|TestBurst)'`.
- Five-second publication-metadata fuzz campaign, two workers, 17 executions.
- Five-second named-constant fuzz campaign, two workers, 4,107 executions.
- Windows amd64 source test-binary cross-compilation, including the new ACL test.
- Docs mirror drift test, sync-docs.sh --check, commentcheck against main,
  formatting and diff whitespace checks.

The new publication property first failed for tree names, commit/tag messages,
bundle refs and request metadata. It now rejects before any network request.
Its ten forms also cover prerequisite descriptions and JSON-escaped values.
The named-constant property first accepted retired commands. Fuzzing then found
a valid command rejected when a partial constant was checked separately. That
input is retained as a seed. Current coverage includes partial dynamic lines,
aliases, repeated constants, cross-file references, local shadowing and formatted
output calls. No acceptance skip changed and no new native acceptance cell is
claimed. The full repository suite stays with the harness.

No Git write, candidate Sparkwing binary or service ran. Local Git configuration
has no fixture user/core.worktree overrides. Scratch is removed at session end,
and the shared build cache is retained.

Friction: an existing test encoded the metadata leak as allowed behavior.
Overwing discovery worked despite its reported source lag. Native Windows ACL
execution and the post-session native report remain external verification.

STATUS: CONTINUE
