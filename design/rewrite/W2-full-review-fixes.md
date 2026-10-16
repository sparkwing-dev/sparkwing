# W2 full-review fixes

<!-- design:begin -->

Findings 1-11 and 14-16 are addressed. Findings 12 and 13 were left to their
assigned owners. No git write, release, Sparkwing pipeline or candidate binary ran.

| Finding | Status | Invariant and evidence |
| --- | --- | --- |
| 1 | FIXED | Completing setup ends bootstrap authority atomically. The setup gate locks before actor resolution. Issuance, seed/child/session revocation and generation advances commit together. Native setup, rollback and tagged-subject tests pass. A consumed seed returns an already-used error. |
| 2 | FIXED | A pod bootstrap authorizes source acquisition and one exchange only. CredentialBootstrap is checked before ordinary alternatives. Native generated metadata refusals and all ordinary credential alternatives fail closed. Source reaches its handler and exchange commits once. |
| 3 | FIXED | Delegation never increases effective repository authority. Null is unrestricted and an explicit empty list denies everything. Shared narrowing and every-ancestor claim/award intersection pass 500 generated narrowing cases and 160 native claim cases. |
| 4 | FIXED | Issuance, readiness and deployment routes recognize the same authority. DeploymentAuthority covers operator and service subjects. A natively issued service administrator passes inventory and readiness after the seed ends. |
| 5 | FIXED | Transactions revalidate the console authority accepted by the route. ConsoleProof carries its direct session. The shared predicate checks origin, lifetime and current configured account. Native issue/read/revoke operations and generated stale/foreign/missing proof cases pass. |
| 6 | FIXED | Licensed self-hosted controllers refuse deployment-admin bearer access during authentication. Generated tests cover licensed/unlicensed and hosted/self-hosted combinations, including forged query hints. Execution and source routes keep their separate credential classes. |
| 7 | FIXED | Routes and commits use the same token ownership relation. OwnsToken is shared by binding, metadata, rotation and revocation. Native reader/editor revocation and generated creator/administrator cases pass. |
| 8 | FIXED | Data binding checks the operation and selectors decoded from the request. All nine operations pass through the real router, authenticator, store and filesystem. Ticket commits check the uploader, batch reads check each digest, and membership loss ends account data opt-ins. |
| 9 | FIXED | Configured credential operations have concrete production implementations. Native ProvisionPolicy, tenant/operator CapabilityManager and tenant ReadOIDCFacts are wired through unchanged constructor signatures, with compile-time conformance. Default-constructor machine, service, signed Actions, inventory, rotation and signed OIDC tests pass without permissive policy adapters. |
| 10 | FIXED | Enrollment and claiming evaluate repositories through one owner. RepositoryPattern and RepositoryIdentity are distinct types. Native HTTP enrollment patterns admit the matching canonical work and refuse another owner. |
| 11 | FIXED | A committed result replays only with its recorded actor, route, resource, fence and exact HTTP-body digest. Existing terminal state_actor and capability result_digest fields persist those proofs in the committing transactions. Native node, trigger and controller-plan replays pass across handoff and cancellation. Generated proof/holder/body changes fail. A changed accepted-plan body also fails while its owner remains live. Failed proof persistence rolls back plan acceptance. |
| 14 | FIXED | Every native forbidden response includes its discriminator. Reason-bearing constructors cover native bindings and controller authorization errors. The native generated binding refusal test passes. |
| 15 | FIXED | Signed filesystem transfers use canonical transport errors and exact methods. Native tests cover 400/403/404/409 envelopes and implicit HEAD returning 405 with Allow before signature verification. |
| 16 | CORRECTED | Security comments describe implemented behavior. Removed the reviewed login, local-token, delegation, expiry, failure-budget, cache and stale-authentication claims. This is a comment-only correction, so no artificial failing behavioral test was added. |
| 12, 13 | NOT TOUCHED | Cloud award metering and local peer intake remain with W1 and W9. |

The original failures were captured through native requests. Later independently
added bootstrap and Actions tests reproduced the old failures with temporary Go
overlays, without changing shared source. Authentication and binding fakes were
not used to establish native success. The Actions fixture substitutes only
external public-key delivery and verifies real RS256 signatures.

No skipped acceptance cell was changed. Existing root-provisioning and Actions
acceptance tests now use the production policies instead of requiring a 503 and
then installing a permissive adapter. The independent source/exchange policy
expectations retain their coverage. No historical fixture port is claimed.

Production deletions total 150 lines for this session under the lead's counting
rules. These include the redundant delegation adapter, replaced issuance code,
the retired provision_contract.go scaffolding and stale comments. Its real
interface and implementation now live in authority_provision_policy.go.

W2 production delta against main is +14,593/-17,363, net **-2,770**, within the
-250 budget. This session changes W2 production by +302/-87, net **+215**.
Including authorized shared production files, this session is +1,111/-150,
net **+961**. Counts include untracked production files and exclude tests,
testdata, acceptance harness and contract-named files, matching the lead's script.

Mechanisms added follow the lead's selected designs: shared authority predicates,
a typed console proof, concrete native adapters and persisted result proofs.
They replace policy/transaction disagreement and absent production owners.
No schema table, environment variable, CLI flag, retry or compatibility alias
was added. WithCloudDeployment is selected from the existing cloud/cloud-free
startup profile because the license does not distinguish hosted Cloud from
licensed self-hosting. The production constructor signatures remain unchanged.

Verification passed with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR under /tmp/sw-astra-W2. Tests used -p 4.

- Full go build -p 4 ./... and go vet -p 4 ./....
- Full internal/capability, internal/apierr and internal/blob suites.
- Integrated focused store, controller, tests/acceptance and internal/nodehost
  tests: 14.850s, 3.200s, 7.112s and 3.104s respectively. The run covers native
  authority, provisioning, repository admission, console, data, replay, OIDC,
  signed transport, credential routes, plan acceptance and owner expansion.
- Additional native token-ownership and deployment-eligibility properties pass.
- Embedded documentation mirror and authentication guide checks pass.
- commentcheck against main and git diff --check pass.

Existing integration gaps remain visible. TestFinalCredentialServingRegistrations
still reports these registrations missing in both listener configurations:

- GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output
- GET /api/v1/runs/{run}/jobs/{job}/output
- POST /api/v1/operator/teams/{team}/refunds
- POST /api/v1/runs/{run}/jobs/{job}/claim/input

Broader exploratory runs also encountered legacy tokens/args_json fixtures and
the sandbox's socket refusal. No full-controller or whole-repository test pass
is claimed. Native nodehost fixture scopes were migrated to null, and process
delegation now preserves its parent scope. Those direct-dependent tests pass.

Contract change needed in another area: W6's AppFacts needs a durable verified
App event populated by the verified intake owner. It currently persists a
DeliveryKey but no event. Native OIDC reads that verified receipt; if it has
been pruned, issuance returns unavailable instead of inventing a manual
identity. A native missing-receipt test pins the refusal. No foreign contract
or schema was changed to hide that retention gap.

Files outside W2:

- pkg/store: authority_binding.go, authority_bootstrap.go,
  authority_delegate.go, authority_issue.go, authority_provision.go,
  authority_console.go, authority_management.go, authority_oidc.go,
  authority_provision_policy.go, authentication_contract.go,
  capabilities_contract.go, blob_data_access.go, console.go, node_state.go,
  plan_seal.go, run_spec.go, run_state.go, result_proof.go, store.go and
  unit_matching.go. Removed provision_contract.go. Store tests changed in
  authority_runtime_test.go and delegation_runtime_test.go; added
  repository_claim_test.go.
- internal/blob/filesystem.go: canonical signed-transfer errors.
- internal/nodehost/owner_expansion_authority.go: preserve repository scope.
  Test fixtures changed in native_store_test.go and store_host_fixture_test.go.
- cmd/sparkwing-controller/main.go: hosted-profile eligibility composition.
- tests/acceptance/root_provisioning_http_test.go and
  actions_issuance_http_test.go: default native policies and route expectations.
- docs/auth.md and pkg/docs/mirror/auth.md: implemented authority behavior.

W2's C2 contract records ConsoleProof, repository semantics, native adapters and
persisted result proofs. Lead-owned handoffs and amendments were not edited.

Friction was limited to temporary compilation conflicts during shared edits,
stale unavailable-adapter expectations, legacy fixtures and sandbox sockets.
Scratch is removed at handoff. The shared build cache is retained.

<!-- design:end -->

STATUS: COMPLETE
