package acceptance

// Fixtures specifies synthetic inputs and independent oracles shared by the placement adapters.
// No fixture may discover credentials or state through the user's home directory.
func Fixtures() []Fixture {
	return []Fixture{
		{
			ID:     "repo",
			Setup:  "Two teams, two repositories per team, two default-branch commits, a feature branch, a PR base, a fork, a rename, case-distinct non-GitHub paths, a port and a remote-free checkout. Seed a local git origin and an independently signed App delivery for the exact SHA.",
			Oracle: "A fixture manifest pins repository IDs, SHA, source file hashes, expected default branch and actor. A typed URL or ref carries no verified authority. Advance origin HEAD after delivery to expose accidental head checks.",
		},
		{
			ID:     "sdk",
			Setup:  "Equivalent Go and TypeScript pipelines: typed args and outputs, steps and groups, conditional needs, retry, memo, depcache, artifacts and consumes, startup/body env, cleanup, risk, approvals, generators, recovery jobs, child runs, refs, OIDC, secrets, masking, annotations and summary. Malformed protocol and plan fixtures are separate from SDK output.",
			Oracle: "Checked-in expected graph, outputs and side-effect counters use customer job names. Generator chains reconstruct code from recorded output but cannot alter persisted topology. Both SDKs must produce the same observable result.",
		},
		{
			ID:     "store",
			Setup:  "Fresh final-schema SQLite and PostgreSQL databases, isolated per case, with tenant and operator access. Keep a separate incompatible database fixture, one-success-per-pipeline retention fixtures and financial rows.",
			Oracle: "Read-only SQL snapshots prove rows, event uniqueness, attempts, monotone generations, ownership leases, outbox revisions, retained balances and atomic counters. Product transition tables never generate expected outcomes.",
		},
		{
			ID:     "identities",
			Setup:  "Anonymous, local peer, member and nonmember sessions, deployment admin, operator console, team machine, launcher service, push-scoped Actions, here award, trigger/work/plan/eval awards, bootstrap and node-host bearer. For every relevant identity create live, expired, revoked-parent, ended-binding and wrong-team variants.",
			Oracle: "A hand-reviewed rights matrix independently names allowed routes, grant/binding refusals, liveness failures and transactional races. Values are synthetic, held in memory or owner-only files, and excluded from argv, env and receipts.",
		},
		{
			ID:     "secrets",
			Setup:  "Required, optional, pool-restricted and undeclared secrets; plain variables; team git credentials with an opted-in and an opted-out machine. Use unique long sentinels, short secret 42 and multibyte values, with identifiers containing each registered value.",
			Oracle: "Expected release and audit counts are independent of authorization tables. Scan logs, errors, annotations, summaries and receipts for sentinels before saving evidence. Identifier refusal never repeats the matched value.",
		},
		{
			ID:     "blobs",
			Setup:  "Filesystem signer and S3-compatible fixture with known bytes/digests, another team's object, another run's object, two unrelated jobs in one run, valid dependency/reference/memo edges, corrupt and missing bytes, a tombstone, signed upload/download and log segments.",
			Oracle: "Independent sha256 and HTTP transfer ledger check integrity, existence, read sets, receipt identity, immutable commit and unique-byte accounting. Unknown digests stay unreadable even when their bytes exist.",
		},
		{
			ID:     "clock",
			Setup:  "Inject a store clock and barrier-controlled keeper transport. Advance across claim, owner, queue, approval, retention, URL and billing boundaries without sleeping. Hold and release responses to force transaction races.",
			Oracle: "Assert from the store's committed time and explicit event acknowledgements. Advancing a fixture clock never substitutes for native process termination or an external provider's actual expiry.",
		},
		{
			ID:     "processes",
			Setup:  "Build binaries and SDK fixtures from the candidate revision, recording hashes. Start controller, runner, CLI and SDK children with private homes and explicit endpoints; expose readiness, claim, start, report and exit barriers.",
			Oracle: "Owned process handles, pipe EOF, exit codes and committed events establish ordering. Inject SIGINT/SIGTERM on Unix and native termination on Windows, reap descendants and fail on cleanup leaks.",
		},
		{
			ID:     "github",
			Setup:  "Hermetic HMAC webhook/App API and OIDC issuer/JWKS fixtures with replay, wrong issuer/audience/repo/push, fork and key-rotation cases. The hosted lane separately uses a dedicated repository and actual GitHub Actions identity.",
			Oracle: "Record exact checked-out SHA and outbound check revisions. Delayed pending/running acknowledgements cannot erase terminal state. A fake OIDC issuer proves protocol cases but cannot count as hosted integration evidence.",
		},
		{
			ID:     "cluster",
			Setup:  "Explicit test kube context and namespace, image digests, one Job per team-dedicated node, enforced default-deny egress, root plus privileged DinD for plan/eval/work, fixed 20 GiB scratch, bootstrap Secret and a disposable network canary.",
			Oracle: "Inspect admitted Pods and run docker build/run plus an allowed-egress and denied-egress probe. Inventory reachable DaemonSet ports and record the escape canary. Manifests alone do not prove isolation or enforcement.",
		},
		{
			ID:     "stripe",
			Setup:  "A hermetic billing provider for deterministic replay/race cases and a separate Stripe test-mode account for checkout, webhook, credit, refusal and refund integration. Test credentials arrive in private files. Replay events, deliver them out of order and restart the controller.",
			Oracle: "Provider test-mode IDs, verified webhook payloads and immutable ledger rows agree. Fake receipts never count as Stripe evidence. Abort before requests if keys/account/objects indicate live mode, and never print keys.",
		},
		{
			ID:     "desktop",
			Setup:  "Disposable Linux and macOS login-service users and a native Windows SID/DACL test account. Separate homes, captured env snapshots, dashboard clients, managed schedule units and mismatched service binaries.",
			Oracle: "One local state owner, four independent stay-alive reasons, login restart, idle exit, update restart and doctor skew diagnostics. A second SID cannot read private files or gain local authority.",
		},
		{
			ID:     "catalog",
			Setup:  "Independent inventories of target command paths, flag grammar, route resources, error envelopes, allowed SDK symbols, env names and configuration keys from frozen architecture sections 5-9.",
			Oracle: "Exercise the CLI/API/SDK parsers and compare declared behavior to the independent inventory. Removed aliases, commands, fields, credentials and cache service surfaces are refused rather than silently accepted.",
		},
	}
}
