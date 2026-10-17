# W9 burst controls and SDK execution

Starting HEAD: `b3d311eee`. Earlier burst implementation is retained.
No Git writes, installed Sparkwing execution, services, deployment or release ran.

## Implemented

- `overflow show --profile NAME` reports the account's saved allowance and usage.
  `overflow set` accepts exactly one of `--minutes N` or `--uncapped`. Zero
  prevents new Cloud overflow. The existing default remains uncapped.
- The shared OverflowManager uses the existing authenticated account routes and
  retry policies. It refuses invalid inputs before HTTP and malformed receipts.
  JSON retains allowance, usage and period. Plain emits minutes or `uncapped`.
- Process route evidence now distinguishes secret names from pipeline names.
  Previously `/node/v1/secrets/TOKEN` also selected pipeline TOKEN, so the real
  store refused the release. The registered route selects the relevant field.
- Controller secret reads use the shared SecretValue response. The private
  native client previously refused false shared visibility because the response
  omitted it. Empty values were omitted too. Both remain explicit now.
- Real Go SDK planning and steps execute in-process from here, personal-machine
  and exchanged Cloud awards. The composed path uses real intake, source
  upload/download, store claims, private HTTP state, process delegation, secret
  release, start/step/result writes and requester cancellation. The host registers
  masks before SDK delivery. Both raw and base64 SDK log messages are masked.
  Completion removes the old claim's live read authority, and the owner observes
  the successful result.

The selector invariant is: each registered route contributes only its own named
selectors. The value invariant is: an authorized explicit secret read preserves
its value, including empty, and its masking classification. Neither fix widens
grants, claim liveness or declaration checks. The selector property failed before
the fix. The native-client regression failed with `missing shared` before the
response fix. The three SDK paths initially failed at those actual boundaries.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole-repository `go build -p 4 ./...` and vet on all changed Go packages.
- Complete client, controllerwire and clisurface suites.
- CLI overflow, routing, source, help/reference and docs-honesty selections.
  The overflow command runs through the real client, router, account authority
  and SQLite. It sets, reads and clears limits and joins every transport.
- Five existing burst flows plus three SDK execution flows. Requester secret
  tests now use the native process client for nonempty and empty values.
- Focused nodehost process, secret, executor and native-stream masking tests.
- Controller plan-secret, credential-route, registration and mutation selections.
- Race checks on composed burst, SDK, native-secret, CLI and client paths.
- Selector fuzz campaign: 5,510 executions in three seconds, all passing.
- Surface and credential-policy acceptance selections. No native matrix cell
  was newly unskipped, and no archived test was newly ported.
- Full doccheck package and executable checks, generated command-reference
  drift, embedded docs/changelog mirrors and privacy checks, and diff whitespace.
- Commentcheck against HEAD: clean across all 15 changed Go files.

Commentcheck against main still fails on the inherited untagged pinnedClock
comment in pkg/store/store.go:108. It is present in the starting HEAD and is
unchanged. Broader controller secret/dashboard and pkg/docs web-client selections
cannot run because httptest listeners are refused by the sandbox. Their
socket-free focused replacements above pass. No full-repository test pass is
claimed. The new CLI integration fixture initially lacked the session cipher.
After matching production cipher construction, its ordinary and race runs pass.

Command docs were generated through a temporary test of the existing exporter,
then that test was removed. The exported API snapshot and embedded mirrors are
regenerated. No operational Sparkwing binary ran.

## Contract changes made

- C2: named process-route evidence and explicit shared SecretValue responses.
- C3 controller wire: OverflowManager construction, methods and validation.
- C3 protocol/SDK: composed declared-secret execution and native boundaries.
- C4: overflow CLI grammar, profile selection, output and mutation behavior.

The four owning contract files record these changes. Lead-owned amendments and
handoffs are untouched. No schema, route, grant, environment variable, timeout or
new budget default was added. The only changed private signature is
serviceEvidence; its single registration caller is migrated in the same file.

Mechanisms added: two CLI verbs and the shared management client for the already
implemented account allowance. A run-local YAML cap cannot read or change a saved
account limit. Removing these verbs and client construction reverses that surface
without changing the existing API or account data. The connection callback uses
the existing operator-command test pattern and adds no runtime service.

## Remaining work

- DESIGN retained: local-secret values exist in one Store process. Cross-replica
  Cloud release still needs verified affinity or an unpersisted relay. Raw values
  must not enter SQL, seals or bundles. This session adds no substitute store.
- Native verification remains with the lead: selected-engine installation and SDK
  subprocess transport, here hosting and local log streaming, registered runner
  execution, Cloud Job execution, Ctrl-C, artifacts and competing local admission.
  The composed SDK adapter runs real author operations in-process. Engine and
  transport boundaries are fixtures, and SDK code is not compiled from the
  uploaded bundle by this test. Source acquisition is independently real in the
  same composed path.
- The earlier committed retry-mode replay DESIGN remains open. No second patch
  was made to that mechanism. Earlier broader integration requests retain their
  status in W9-BURST-FINALIZATION.md and W9-INTEGRATION-RECHECK.md.

## Size and shared files

This session adds 178 production Go lines and replaces 8, net +170. No whole
legacy implementation was replaced. Earlier legacy cuts stand. W9-owned work is
+159/-1, net +158. Cumulative W9 production versus main is +18,344/-23,823,
net -5,479, within the -2,900 budget. Counts exclude tests, testdata, acceptance
and contract declaration files. No removed API leaves callers behind.

Shared files outside W9 ownership:

- internal/nodehost/process_services.go and process_service_reads_test.go
- pkg/controller/secret_reads.go, local_secrets_test.go,
  burst_acceptance_test.go, burst_overflow_test.go and burst_sdk_execution_test.go
- design/rewrite/contracts/c2-credentials.md, c3-controller-wire.md,
  c3-protocol-sdk.md and c4-surface-config.md
- .apidiff/pkg_controller_client.txt, CHANGELOG.md and pkg/docs mirrors

Friction: sandbox listeners, the inherited main commentcheck finding, and the
standards tool's reported source lag. Local Git identity and core.worktree
overrides are absent. All checks are joined. Worker scratch is removed after
verification, and the shared Go cache is retained.

STATUS: CONTINUE
