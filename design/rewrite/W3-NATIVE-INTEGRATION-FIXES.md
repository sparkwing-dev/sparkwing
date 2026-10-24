# W3 native integration fixes

The local queue applied strict online runner policy before its authenticated
claim reached local policy selection. Failure stopped the queue, which canceled
the controller workers and closed api.sock. The new production-composition
regression reproduced the failed version-policy fetch with network denied.
Both queue startup/refresh and claim admission now use
`EngineController.CheckRunner`, which reads the persisted claimant subject.
Only local subjects select local policy. Machine and service policy stays strict.
The regression now checks both preflight and a real empty-queue claim without
network access. The machine-claim fuzz property still refuses forged local labels.

The original install-to-green native failure occurred within 29.37 seconds.
The default idle delay is five minutes. Socket layout is a pure function of
home under /tmp, independent of TMPDIR. No idle timeout, socket retry or cached
path workaround was added. The lead still needs to run install-to-green against
this source to confirm that fixing the worker failure closes that native case.

The offline fixture exposed a separate source bug before controller startup.
Local acquisition required HEAD even for a freshly initialized repository.
It now accepts an unborn local branch when no SHA was supplied, keeping SHA
empty. Exact sealed commits and remote sources remain required. The new tests
also refuse detached missing commits and branch references to missing objects.
The full source suite passes, including the existing moved-HEAD refusal.

The OIDC test was stale against C1's sealed-event contract. Its AppFacts lacked
Event, and it expected replay-ledger expiry to erase the retained run's identity.
The fixture now supplies the sealed event and requires the same identity after
deleting the delivery. The existing forged-selector property and every event
case pass. No OIDC production behavior changed. The test lives in internal/controller,
rather than cmd/sparkwing.

The offline test also used removed compile-progress text and
invocation.binary_source fields. It now observes actual go build invocations,
requires no additional build on the cached run, and reads public successful run
IDs. Connected and denied runs use identical build flags because GOFLAGS is a
cache input. After warming, it stops the connected controller so the denied run
starts its controller with denied proxies too. The independent review identified
that environment gap. Its contract-wording finding is corrected as well.

## Evidence and remaining native work

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, temporary paths below /tmp/sw-astra-W3 and at most four
package workers:

- Whole-repository go build ./... and focused vet of source, runners, controller,
  CLI and localcontroller.
- Full source, runners, nodehost and localcontroller suites. The host suite
  completed in 25.628 seconds.
- Production local-controller/queue tests, controller claim tests, OIDC identity
  property and machine-policy fuzz seeds. The machine-policy fuzzer also ran
  32 executions with two workers.
- TestNativeStoreOwnerW8, FuzzStoreQueueClaimAuthorityW8 seeds and
  TestEngineRetryPlanKeepsSourceSeal in tests/acceptance.
- A temporary socket-free CLI probe compiled the offline fixture, then planned
  it again with GOPROXY=off and unreachable HTTP proxies. The second plan made
  no additional go build call. This passed in 8.494 seconds. The probe was
  removed with scratch.
- Commentcheck against main across 2,691 files, formatting and whitespace checks.

The offline run first reproduced source git rev-parse failure. After the source
fix, it reached controller startup and failed only on the sandbox's Unix-socket
EPERM, including the cleanup probe. After the test migration it reached the same
socket refusal. Those failures are retained in the session output and are not
passing native evidence. The public install-to-green run and the full offline
HTTP-recorder run remain for the lead harness. No test was skipped or weakened.

The requested W3-native-latest.md evidence file did not exist at startup or the
final check. W1-tests-native-latest.md contains the original missing-api.sock
failure. W3 cannot be COMPLETE until the lead supplies clean native evidence.
No new acceptance matrix cell was enabled. The existing component cells above
pass. Earlier retry-replay work and its separate race finding were not redone.

## Contracts, scope and volume

Contract changes made under integration authority: C3 engine/version documents
the shared persisted-subject policy check, and C3 protocol/source documents
unborn local checkout semantics. No wire, schema or credential grant changed.
No contract change in another area remains requested. The lead-owned handoffs
and amendment files were not edited.

Shared files touched are internal/wingd/localcontroller/store_runner.go,
internal/controller/engine_claim.go, internal/controller/review_oidc_native_test.go,
cmd/sparkwing/local_services_test.go, cmd/sparkwing/run_offline_unix_test.go,
internal/wingd/LOCAL-CONTROLLER.md and the two C3 contract files. The production
claim caller was migrated to the shared policy method. No API was removed.

Production changes are +48/-15, net +33. W3-owned production changes are +11/-2,
net +9. W3 production versus main is +11,790/-26,047, net -14,257 under the
lead's exclusions. The removed production lines replace the duplicate policy
selection and mandatory local HEAD failure. No legacy module was removed in
this session. Test changes are +162/-21, net +141. The OIDC and offline retained
tests were ported to current contracts.

Assumptions: no setting, flag, environment variable, retry, cache or interface
was added. The shared policy method prevents the two callers selecting different
policy. The test-only Go forwarding script observes compilation without relying
on removed display fields. The independent review found no production regression
and accepted both additions as needed for the fixes and their proofs.

Friction is the missing native evidence file and forbidden sockets. No Git
writes, operational installs, pipeline runs or releases ran. Git has no local
identity or core.worktree override. All checks joined and no scratch executable
was running before removing /tmp/sw-astra-W3. The shared build cache was kept.

STATUS: CONTINUE
