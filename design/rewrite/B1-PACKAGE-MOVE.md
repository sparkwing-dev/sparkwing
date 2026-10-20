# Barrier B1 package move

Integration already matched HEAD at 215ddd7d4. The merge command could not write ORIG_HEAD in the sandbox. No commit was made. The harness owns committing.

Moved the store, store/teststore, controller, controller/client, controller/ciphertest and complete storage trees from pkg/ to internal/, including nested store helpers. No facade remains. NodeRef, CreateTokenRequest, CreateServiceTokenRequest, EnrollRunnerRequest, CreatedToken and RotatedToken moved to internal/controllerwire. RunSnapshot remains the private host read model because it contains store authority state.

Go rewrite tools updated imports, guards, paths, generators and docs. Positional NodeRef literals became keyed literals. VERSIONING.md and package docs describe internal implementation scope. The Breaking changelog entry links to migration guidance. Ten public snapshots were removed. Regeneration also captured two existing SDK declarations missing from its snapshot.

Production Go: 249,723 to 249,732 lines, net +9. Of 9,955 production functions compared after normalizing moved types and literal keys, only the doc checker's required file-path update differs. No behavioral bug closure is claimed.

Build and vet pass in default and qatrust modes. Focused tests, selected rewrite_integration tests, SDK compilation, web type checking, API drift checks and doc synchronization pass. The web production build needs network access to Google Fonts. Full suites, PostgreSQL and socket checks remain with the lead.

The move accepts 368 existing unexport candidates once across seven newly internal packages. No permanent growth exception was added. Guardcheck exactly reproduces three integration-baseline failures after path translation.

## Decisions needing the lead

Resolve the inherited F3 secret_preflight finding and stale unexport counts for internal/limits (116 to 117) and internal/runspec (5 to 4). Run the remaining checks and commit the reviewed tree.

STATUS: COMPLETE
