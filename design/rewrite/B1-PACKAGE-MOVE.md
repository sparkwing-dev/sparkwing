# Barrier B1 package move

The harness committed session 1 as f8ba35d93. Integration (215ddd7d4, bw-3095-rewrite) is already an ancestor of HEAD. This session made no commit.

Moved store, store/teststore, controller, controller/client, controller/ciphertest and all storage packages under internal/, including nested helpers. No alias facade remains. NodeRef, CreateTokenRequest, CreateServiceTokenRequest, EnrollRunnerRequest, CreatedToken and RotatedToken moved to internal/controllerwire. RunSnapshot remains a private store read model because it contains authority state.

Session 1 updated imports, depguard rules, allow-list paths, generators, documentation, package comments and VERSIONING.md, added the Breaking changelog entry, and removed ten public snapshots. Production Go is net +9 lines. No behavioral bug closure is claimed.

Session 2 corrected two remaining split-component paths in the concurrency and lifecycle guards using gofmt -r. Both tests failed before the change and pass afterward, including qatrust and rewrite_integration builds. This session changes no production Go and adds no mechanism.

Build and vet pass in default and qatrust modes. TypeScript SDK compilation and web type checking pass. See B1-session2-test-output.txt for commands and results.

The one-time acceptance of 368 existing unexport candidates across seven newly internal packages was committed in session 1. This is census growth from the move, with no standing exemption. No allow-list grew this session.

Guardcheck still reports the three inherited findings. The local-controller independent-awards test fails identically on both B1 and the integration baseline: heartbeat 409, lease authority lost.

## Decisions needing the lead

- Handle inherited F3 secret_preflight and unexport counts for limits (116 to 117) and runspec (5 to 4), plus the pre-existing local-controller lease failure.
- Rerun the full suites with the corrected guards, PostgreSQL/socket checks and the web production build with Google Fonts access. Review and commit.

STATUS: COMPLETE
