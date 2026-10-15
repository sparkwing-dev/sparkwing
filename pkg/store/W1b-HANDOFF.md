# W1b raw payment API cut and metering regression

Base: 65419c914. Net production Go delta: **-99 lines** (3 added, 102
deleted), entirely in pkg/store. Combined store delta against main: +3,113.
The -4,250 budget is still exceeded by **7,363 lines**. The overage remains
unjustified, and legacy output and claim-credit paths remain. This area is
incomplete.

Removed six public raw payment callback/lookup APIs with no production callers:
Tenant.SaveCard, Tenant.HoldForDispute, Operator.PaymentTeam,
Operator.ReversePayment, Operator.RecordPaymentWarning and
Operator.ReportCardRefund. Their transaction-local mutation owners remain for
verified-event settlement. Existing regression tests call those owners through
payment_fixture_test.go. These trusted-fixture tests do not establish provider
verification. Console hold/release APIs and the actual card collection worker
remain. No provider fact is accepted through a replacement public callback.

Removed Store.SetTokenMetered. Legacy credential fixtures seed their old marker
through test SQL. TokenMetered delegates to the existing transaction query
through storeRowQuerier, removing the duplicated read. The SQL scope registry
follows the private payment owners and removes entries for deleted methods and
the forwarding read. The registry's four tests pass.

Ported TestTokenMeteredMarkerIsOperatorSet from the deleted tokens setter to
TestMeteredRootPolicyCannotRewriteAwardProperty. Its 32 cases vary root metering,
runner versus launcher execution and elapsed compute. Invariant: awards derive
metering from persisted root authority, launchers always pay, and later root
changes cannot change an award's committed charges or replay.

**W1 implementation defect, red regression retained:** eight cases fail when
a launcher root has RootMetered=false. ClaimUnit succeeds and persists a Cloud
award with Metered=false. authority_issue.go's award constructor copies only
parent.Capability.RootMetered into BillingProvenance. queuedUnitTx also gates
financial eligibility only on the root marker. A59 requires launcher awards to
be metered regardless of that flag. The other 24 cases pass, including runner
marker changes after award and immutable financial replay. This is an issuance
policy fix for W1, not a change to the C1/C2 contract. No W1 implementation file
was edited. Run:

GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache go test -p 4
./pkg/store -run '^TestMeteredRootPolicyCannotRewriteAwardProperty$' -count=1

Verification:

- Whole-repository go build -p 4 ./... passes with GOWORK=off and VCS stamping off.
- Scoped go vet -p 4 passes for pkg/store, pkg/controller and
  cmd/sparkwing-controller. All tests in those packages compile.
- The focused store suite passes in 82.848s: TestPayment*, FuzzPayment* seeds,
  TestStoreData*, TestMeteredUnit*, TestLogPersistence*, TestBlob*, FuzzBlob*
  seeds, TestResultRecords* and TestTenantSQLScope_*.
- Seven existing store acceptance cells pass: CAS integrity, presence/read set,
  large presence budget, cache scope, reference edges, reachability/retention
  and billing/retention. No new native acceptance cell is claimed.
- TestAdminFreezesAndReleasesWithTheConsolesRecords passes. The changed legacy
  controller deletion/git-credential fixtures fail before reaching their
  migrated calls because their setup still writes the removed tokens table.
  The legacy operator-console fixture also attempts a socket, which this
  sandbox refuses. Their runtime paths remain integration evidence for the lead.
- commentcheck -base main is clean across 2,132 files. git diff --check is clean.
  Embedded changelog/migration guards pass. The store API snapshot is regenerated,
  including three earlier removals absent from the previous snapshot.
- The full new root-policy property is red on the eight launcher cases above.
  No whole-repository test run, Sparkwing pipeline or native PostgreSQL run was made.

Local contract changes: c1-data.md records the seven removed public methods.
The verified-event signature and all foreign contracts remain unchanged.
The new property ports the legacy marker finding but is not a passing acceptance
proof until W1 fixes launcher issuance. No test was skipped to hide it.

Remaining cross-area work:

- W2/W1: PaymentEvent still carries only checkout paid/expired/failed facts.
  payment_events still requires a checkout FK and permits only those kinds.
  The approved card-setup/refund/warning/dispute settlement needs W2's verified
  event payload and persisted receipt/setup correlation. Private mutation owners
  are tested here. No raw proof or caller-selected payer substitutes for C2.
- W1: per-award exhaustion anchoring and committed renewal cancellation are
  absent. accountUnitBillingTx returns only error, and renewals construct
  LeaseBeat before accounting. The legacy node exhaustion anchor cannot cover
  plan, eval and owner awards. Spend/grace and old claim-credit retirement remain.
- W1: this checkout lacks committed output/manifest selectors on NodeRow and
  nodeReadColumns. CopyMaterializedRecords cannot resolve a committed source
  selection. W2/W3/W9 must carry the selected result to authorized readers.
  The old output client still calls removed upload routes through the legacy
  StateStoreOld finish path. Its scheduler, rehydrate, claim-input and child-read
  callers need the declared record/result cutover. No new compatibility path
  was added here.
- W1: imported terminal-attempt seeding remains absent. The seven relocated
  cells retain teststore.SeedPresence's fixture. Harness moves and the two old
  output catalog removals were already complete and were not repeated.
- W2/W3: native host logseg.Data binding, authenticated log transport/recorder,
  physical allowance eviction, source-before-run binding and the
  ReadBlob/ChargeDownload composition remain production joins. NewDataClient
  already exists and is not reported as a stub.

Shared files touched: cmd/sparkwing-controller/admin_test.go and controller
operator_test.go, deletion_review_test.go and git_credentials_team_test.go contain
only fixture migrations. W1's tenant_sql_scope_guard_test.go contains registry
key migrations. claim_team_boundary_test.go contains one legacy marker fixture
migration. Other store test edits migrate calls to removed methods. Shared docs
are c1-data.md, CHANGELOG.md and docs/migrations/_unreleased.md with their embedded
mirrors. .apidiff/pkg_store.txt reflects the removed runtime APIs. Lead-owned
handoffs.md and amendments files are unchanged.

Friction: the launcher billing regression crosses the explicit W1 ownership
boundary. The remaining verified-event payload is not declared by C2. Legacy
controller fixtures still use the removed credential schema, and socket tests
are unavailable in this sandbox. No new production mechanism, schema, setting,
price or customer restriction was added. No Git writes occurred. Worker scratch
is deleted at completion, and /tmp/sw-gocache is retained.

STATUS: CONTINUE
