package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// trustMatrixFile holds today's within-team trust behavior. Any change to a cell
// shows up as a diff of this file; SPARKWING_UPDATE_TRUST_MATRIX=1 rewrites it.
const trustMatrixFile = "testdata/trust_matrix.txt"

const (
	matrixTeam   = "matrix"
	matrixRepo   = runspec.RepoKey("gh:7")
	matrixMain   = "0000000000000000000000000000000000000a11"
	matrixOther  = "0000000000000000000000000000000000000b22"
	matrixPinned = "0000000000000000000000000000000000000c33"
)

// trustMatrixRow is one kind of run, described by what intake receives.
type trustMatrixRow struct {
	key, title string
	request    IntakeRequest
	principal  capability.Principal
	schedule   *scheduleAdmission
	parent     string
}

func matrixAccount(id string) capability.Principal {
	return capability.Principal{Capability: capability.Capability{Team: matrixTeam, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: id}}}
}

func matrixGit(repo runspec.RepoKey, ref, sha string) runspec.Request {
	return runspec.Request{Version: 1, Team: matrixTeam, Pipeline: "ci", Mode: runspec.IntakeExecute, Git: runspec.Git{Repository: runspec.Repository{Key: repo}, Ref: ref, SHA: sha}, Source: runspec.Source{Ref: sha, Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/" + strings.TrimPrefix(string(repo), "gh:") + ".git"}}}
}

func matrixApp(event, ref string, defaultPush bool) runspec.Request {
	in := matrixGit(matrixRepo, ref, matrixMain)
	in.Origin = runspec.OriginApp
	in.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: event, DeliveryKey: "delivery", InstallationID: 1, RepositoryID: 7, Repository: "acme/repo", Ref: ref, SHA: matrixMain, DefaultBranch: "main", DefaultBranchPush: defaultPush, VerifiedAt: time.Unix(1, 0)}}
	return in
}

func matrixScheduleRequest() IntakeRequest {
	in := matrixGit(matrixRepo, "refs/heads/main", matrixMain)
	in.Origin = runspec.OriginSchedule
	in.Git.DefaultBranch = runspec.Optional[string]{Set: true, Value: "main"}
	in.Git.RepositoryName = runspec.Optional[string]{Set: true, Value: "acme/repo"}
	return IntakeRequest{Authority: capability.AuthorityRef{Kind: "schedule", ID: "nightly"}, Request: in}
}

// matrixSchedule is an App-declared schedule on the default branch; edit adjusts it.
func matrixSchedule(edit func(*scheduleAdmission)) *scheduleAdmission {
	in := &scheduleAdmission{schedule: CronSchedule{ID: "nightly", Team: matrixTeam, GitHubInstallationID: 1, GitHubRepositoryID: 7, GitBranch: "main", ArmedBy: "alice"}}
	if edit != nil {
		edit(in)
	}
	return in
}

func trustMatrixRows() []trustMatrixRow {
	pr := matrixApp("pull_request", "refs/pull/1/head", false)
	pr.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 1, BaseRepo: matrixRepo, HeadRepo: matrixRepo, BaseRef: "main", HeadRef: "feature"}}
	branchPin := matrixApp("push", "refs/heads/main", true)
	branchPin.Source.RequestedRef, branchPin.Source.Ref = "release", matrixPinned
	fetched := matrixGit(matrixRepo, "refs/heads/main", matrixMain)
	upload := matrixGit(matrixRepo, "refs/heads/main", matrixMain)
	upload.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: "sha256:" + strings.Repeat("1", 64)}
	local := matrixGit(matrixRepo, "refs/heads/main", matrixMain)
	local.Origin = runspec.OriginLocal
	local.Source.Location = runspec.SourceLocation{LocalCheckout: "/work/repo"}
	actions := matrixGit(matrixRepo, "refs/heads/main", matrixMain)
	actions.Origin = runspec.OriginActions
	push := func(runnerEnvironment string) capability.Principal {
		return capability.Principal{Capability: capability.Capability{Team: matrixTeam, Subject: capability.Subject{Kind: capability.SubjectService, ID: "actions"}, Binding: capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: string(matrixRepo), Repository: "acme/repo", Ref: "refs/heads/main", SHA: matrixMain, WorkflowRunID: "1", RunnerEnvironment: runnerEnvironment}}}}
	}
	child := func(parent string, repo runspec.RepoKey, sha string) IntakeRequest {
		in := matrixGit(repo, "refs/heads/main", sha)
		in.Origin = runspec.OriginChild
		in.Lineage = runspec.Lineage{ParentRunID: parent, ParentJobID: "job"}
		return IntakeRequest{Request: in}
	}
	alice := matrixAccount("alice")
	return []trustMatrixRow{
		{key: "app-push-default", title: "GitHub App push to the default branch", request: IntakeRequest{VerifiedDeliveryID: "delivery", Request: matrixApp("push", "refs/heads/main", true)}},
		{key: "app-push-branch", title: "GitHub App push to another branch", request: IntakeRequest{VerifiedDeliveryID: "delivery", Request: matrixApp("push", "refs/heads/feature", false)}},
		{key: "app-pull-request", title: "GitHub App same-repository pull request", request: IntakeRequest{VerifiedDeliveryID: "delivery", Request: pr}},
		{key: "app-push-branch-source-pin", title: "GitHub App push to the default branch whose pipeline source: pins a branch", request: IntakeRequest{VerifiedDeliveryID: "delivery", Request: branchPin}},
		{key: "schedule-admin", title: "Admin-armed App schedule on the default branch, unmodified fire", request: matrixScheduleRequest(), schedule: matrixSchedule(nil)},
		{key: "schedule-run-now", title: "Run-now of that schedule by a member", request: matrixScheduleRequest(), schedule: matrixSchedule(func(in *scheduleAdmission) { in.caller = &capability.Authorized{Principal: alice} })},
		{key: "schedule-overridden", title: "Admin-armed App schedule with an override", request: matrixScheduleRequest(), schedule: matrixSchedule(func(in *scheduleAdmission) { in.schedule.Override = &CronOverride{} })},
		{key: "schedule-locked", title: "Admin-armed App schedule locked to a commit", request: matrixScheduleRequest(), schedule: matrixSchedule(func(in *scheduleAdmission) { in.schedule.LockedRef = matrixMain })},
		{key: "schedule-no-app", title: "Schedule with no App installation or consent (non-admin armer)", request: matrixScheduleRequest(), schedule: matrixSchedule(func(in *scheduleAdmission) { in.schedule.GitHubInstallationID, in.schedule.GitHubRepositoryID = 0, 0 })},
		{key: "console-branch-head", title: "Console or CLI run at a branch head the controller fetched", request: IntakeRequest{FetchedRepository: FetchedRepository{ID: 7, Name: "acme/repo", Ref: "refs/heads/main", SHA: matrixMain}, Request: fetched}, principal: alice},
		{key: "cli-upload", title: "CLI upload of a connected repository", request: IntakeRequest{Request: upload}, principal: alice},
		{key: "local", title: "Local run or --runner here", request: IntakeRequest{Request: local}, principal: capability.Principal{Capability: capability.Capability{Team: matrixTeam, Subject: capability.Subject{Kind: capability.SubjectLocal, ID: "laptop"}}}},
		{key: "actions-github-hosted", title: "GitHub Actions push binding, GitHub-hosted runner", request: IntakeRequest{Request: actions}, principal: push(githubHostedRunner)},
		{key: "actions-self-hosted", title: "GitHub Actions push binding, self-hosted runner", request: IntakeRequest{Request: actions}, principal: push("self-hosted")},
		{key: "child-same-commit", title: "Child of the default-branch push at its parent's commit", request: child("run-app-push-default", matrixRepo, matrixMain), parent: "app-push-default"},
		{key: "child-other-commit", title: "Child of the default-branch push at another commit", request: child("run-app-push-default", matrixRepo, matrixOther), parent: "app-push-default"},
		{key: "child-other-repo", title: "Child of the default-branch push in another repository", request: child("run-app-push-default", "gh:8", matrixMain), parent: "app-push-default"},
		{key: "child-of-fetched", title: "Child of the fetched console run at its parent's commit", request: child("run-console-branch-head", matrixRepo, matrixMain), parent: "console-branch-head"},
	}
}

// matrixHost is one claimant shape the claim SQL judges a run against.
type matrixHost struct {
	column                string
	pools                 string
	trust                 capability.Trust
	launcher, persistentK bool
}

// The host and pool columns evaluate the claim query's own poolTrust and freshPod
// fragments; a reserved pool is one a team secret is restricted to with only_on.
var matrixHosts = []matrixHost{
	{column: "host_fresh_pod", pools: `[]`, launcher: true},
	{column: "host_plain_machine", pools: `[]`},
	{column: "host_persistent_k8s", pools: `[]`, persistentK: true},
	{column: "pool_reserved", pools: `["deploy"]`, launcher: true},
	{column: "pool_reserved_trust_any", pools: `["deploy"]`, trust: capability.TrustAny, launcher: true},
	{column: "pool_verified_only", pools: `["main-only"]`, trust: capability.TrustVerifiedDefault, launcher: true},
	{column: "pool_reserved_on_machine", pools: `["deploy"]`},
}

func TestTrustMatrix(t *testing.T) {
	ctx := t.Context()
	s, err := Open(t.TempDir() + "/matrix.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UnixNano()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO teams(name,created_at,updated_at) VALUES (?,?,?)`, matrixTeam, now, now)
	// A verified default-branch record lets unverified runs of the repository learn
	// the default branch they may read, as sealProvenanceTx does at intake.
	seed(`INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,1,?)`, matrixTeam, "sha256:"+strings.Repeat("0", 64), now)
	seed(`INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at) VALUES (?,'memo','s','n',?,?,'refs/heads/main','p','cloud',?,'job','r',1,1,?,?)`, matrixTeam, "sha256:"+strings.Repeat("0", 64), string(matrixRepo), []byte(`{}`), []byte(`{}`), now)

	dialects := matrixDialects(t)
	sealed := map[string]runspec.SealedRequest{}
	var out strings.Builder
	out.WriteString(trustMatrixHeader)
	for _, row := range trustMatrixRows() {
		if row.parent != "" {
			parent := sealed[row.parent]
			raw, err := json.Marshal(parent)
			if err != nil {
				t.Fatal(err)
			}
			seed(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,?,(SELECT COALESCE(MAX(number),0)+1 FROM runs),'ci','running',?,'h',1,?,'host','execute','p',?,'r30',30) ON CONFLICT DO NOTHING`, matrixTeam, parent.RunID, raw, string(parent.Request.Git.Repository.Key), now)
		}
		run := "run-" + row.key
		p, err := matrixSeal(ctx, s, row, run)
		if err != nil {
			t.Fatalf("%s: seal: %v", row.key, err)
		}
		request := runspec.SealedRequest{Version: 1, Request: row.request.Request, RunID: run, Provenance: p}
		sealed[row.key] = request
		cells, err := matrixCells(request)
		if err != nil {
			t.Fatalf("%s: %v", row.key, err)
		}
		hostCells, err := matrixHostCells(ctx, s, row, request)
		if err != nil {
			t.Fatalf("%s: hosts: %v", row.key, err)
		}
		cells = append(cells, hostCells...)
		checkSourceCredentialProof(t, row.key, cells)
		fmt.Fprintf(&out, "\n== %s: %s\n", row.key, row.title)
		for _, cell := range cells {
			out.WriteString(strings.TrimRight(fmt.Sprintf("%-50s %s", cell[0], cell[1]), " ") + "\n")
		}
		var claim string
		for _, dialect := range dialects {
			rendered, err := matrixClaimCells(ctx, dialect, request)
			if err != nil {
				t.Fatalf("%s %s: %v", row.key, dialect.name, err)
			}
			if claim != "" && rendered != claim {
				t.Fatalf("%s: %s claim SQL disagrees with SQLite:\n%s\nSQLite:\n%s", row.key, dialect.name, rendered, claim)
			}
			claim = rendered
		}
		out.WriteString(claim)
	}
	got := out.String()
	if os.Getenv("SPARKWING_UPDATE_TRUST_MATRIX") == "1" {
		if err := os.WriteFile(trustMatrixFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(trustMatrixFile)
	if err != nil {
		t.Fatalf("%v; run with SPARKWING_UPDATE_TRUST_MATRIX=1 to write it", err)
	}
	if diff := matrixDiff(string(want), got); diff != "" {
		t.Fatalf("within-team trust changed; review the cells below and rerun with SPARKWING_UPDATE_TRUST_MATRIX=1 to accept them (- file, + code):\n%s", diff)
	}
}

// matrixSeal runs intake's own identity sealing: identityTx, the fetched
// repository, and the default-branch read fallback.
func matrixSeal(ctx context.Context, s *Store, row trustMatrixRow, run string) (runspec.Provenance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return runspec.Provenance{}, err
	}
	defer func() { _ = tx.Rollback() }()
	tx.scheduleAdmission = row.schedule
	return sealProvenanceTx(ctx, tx, matrixTeam, row.request, row.principal, row.request.Request, run)
}

// matrixCells renders every cell the run's sealed request alone decides, through
// the functions secret release, the OIDC issuer and the claim award call.
func matrixCells(r runspec.SealedRequest) ([][2]string, error) {
	p := r.Provenance
	selection, err := runSecretSelection(r)
	if err != nil {
		return nil, err
	}
	pipeline := "no"
	if selection.scope == secretPipelineScope {
		pipeline = selection.repo + " pipeline " + selection.pipeline
	}
	oidc, err := oidcIdentity(matrixTeam, RunRow{Execution: runstate.Execution{ID: r.RunID, Request: r}})
	if err != nil {
		return nil, err
	}
	scope, err := awardDataScope(awardRow{team: matrixTeam, run: r.RunID, node: "job", repo: string(r.Request.Git.Repository.Key), attempt: 1}, r, capability.Capability{Unit: capability.UnitNode}, "")
	if err != nil {
		return nil, err
	}
	return [][2]string{
		{"trust", string(p.Trust())},
		{"pipeline_secrets", pipeline},
		{"shared_secrets", yesNo(selection.scope != secretNone)},
		{"oidc_trigger", oidc.Trigger},
		{"oidc_repository", matrixOr(oidc.Repository, oidc.RepositoryID)},
		{"oidc_ref", matrixOr(oidc.Ref, 0)},
		{"oidc_sha", matrixSHA(oidc.SHA)},
		{"cache_reads", strings.Join(scope.ReadRefs, " ")},
		{"default_branch_work_go", yesNo(p.DefaultBranchWork())},
		{"source_credential", matrixSourceCredential(r)},
	}, nil
}

// safety: these rows get a GitHub repository's source credential without proving that
// repository (no OIDC repository claim); they pin today's rule until the M1 decision,
// and a row that starts or stops contradicting fails the matrix.
var sourceCredentialUnproven = map[string]bool{
	"schedule-locked":    true,
	"schedule-no-app":    true,
	"child-other-commit": true,
	"child-other-repo":   true,
}

func checkSourceCredentialProof(t *testing.T, key string, cells [][2]string) {
	t.Helper()
	value := map[string]string{}
	for _, cell := range cells {
		value[cell[0]] = cell[1]
	}
	contradicts := strings.HasPrefix(value["source_credential"], "repository gh:") && value["oidc_repository"] == "-"
	if contradicts != sourceCredentialUnproven[key] {
		t.Errorf("%s: source_credential %q with oidc_repository %q; update sourceCredentialUnproven only with the M1 decision", key, value["source_credential"], value["oidc_repository"])
	}
}

func matrixSourceCredential(r runspec.SealedRequest) string {
	if sourceRepositoryProven(r) {
		return "repository " + string(r.Request.Git.Repository.Key)
	}
	return "host-wide only"
}

func matrixOr(name string, id int64) string {
	switch {
	case name != "" && id > 0:
		return strconv.FormatInt(id, 10) + " " + name
	case name != "":
		return name
	}
	return "-"
}

func matrixSHA(sha string) string {
	switch sha {
	case "":
		return "-"
	case matrixMain:
		return "main-head"
	case matrixOther:
		return "other-commit"
	}
	return sha
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

type matrixDialect struct {
	name    string
	dialect Dialect
	conn    *sql.Conn
}

// matrixDialects opens one connection per configured dialect with a session-local
// secrets table holding one secret restricted to the reserved pool.
func matrixDialects(t *testing.T) []*matrixDialect {
	t.Helper()
	open := func(name, driver, dsn string, dialect Dialect, temp string) *matrixDialect {
		db, err := sql.Open(driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(); _ = db.Close() })
		for _, stmt := range []string{`CREATE ` + temp + ` TABLE secrets(team TEXT, only_on TEXT)`, `INSERT INTO secrets VALUES ('` + matrixTeam + `','deploy')`} {
			if _, err := conn.ExecContext(t.Context(), stmt); err != nil {
				t.Fatal(err)
			}
		}
		return &matrixDialect{name: name, dialect: dialect, conn: conn}
	}
	dialects := []*matrixDialect{open("sqlite", "sqlite", ":memory:", DialectSQLite, "")}
	if dsn := os.Getenv("SPARKWING_TEST_PG_URL"); dsn != "" {
		dialects = append(dialects, open("postgres", "pgx", dsn, DialectPostgres, "TEMP"))
	}
	return dialects
}

// matrixClaimCells evaluates the claim query's trust fragments. Every configured
// dialect must render the same cells, so the file does not depend on whether
// PostgreSQL is configured.
func matrixClaimCells(ctx context.Context, d *matrixDialect, r runspec.SealedRequest) (string, error) {
	raw, err := json.Marshal(r.Provenance)
	if err != nil {
		return "", err
	}
	q := claimSQL{dialect: d.dialect}
	var cells []string
	for _, h := range matrixHosts {
		query := rewritePh(d.dialect, `WITH c(team,pools,trust,launcher,persistent_kubernetes,provenance) AS (SELECT CAST(? AS TEXT),CAST(? AS TEXT),CAST(? AS TEXT),CAST(? AS BIGINT),CAST(? AS BIGINT),CAST(? AS TEXT)) SELECT `+q.freshPod("c.persistent_kubernetes", "c.provenance")+` AND `+q.poolTrust("c.team", "c.pools", "c.trust", "c.launcher", "c.provenance")+` FROM c`)
		var eligible bool
		if err := d.conn.QueryRowContext(ctx, query, matrixTeam, h.pools, string(h.trust), boolInt(h.launcher), boolInt(h.persistentK), string(raw)).Scan(&eligible); err != nil {
			return "", err
		}
		cells = append(cells, fmt.Sprintf("%-28s %s", h.column, yesNo(eligible)))
	}
	var work bool
	if err := d.conn.QueryRowContext(ctx, rewritePh(d.dialect, `WITH p(value) AS (SELECT CAST(? AS TEXT)) SELECT `+q.defaultBranchWork("p.value")+` FROM p`), string(raw)).Scan(&work); err != nil {
		return "", err
	}
	cells = append(cells, fmt.Sprintf("%-28s %s", "default_branch_work_sql", yesNo(work)))
	cells = append(cells, fmt.Sprintf("%-28s %s", "fresh_host_required", yesNo(work)))
	return strings.Join(cells, "\n") + "\n", nil
}

// matrixDiff lists the lines that differ, in order, with their line numbers.
func matrixDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	// Longest common subsequence keeps an inserted row from reporting every later line.
	lcs := make([][]int, len(w)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(g)+1)
	}
	for i := len(w) - 1; i >= 0; i-- {
		for j := len(g) - 1; j >= 0; j-- {
			if w[i] == g[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var b strings.Builder
	section, printed := "", ""
	line := func(sign, text string) {
		if section != printed {
			b.WriteString(section + "\n")
			printed = section
		}
		b.WriteString("  " + sign + " " + text + "\n")
	}
	for i, j := 0, 0; i < len(w) || j < len(g); {
		switch {
		case i < len(w) && j < len(g) && w[i] == g[j]:
			if strings.HasPrefix(w[i], "== ") {
				section = w[i]
			}
			i, j = i+1, j+1
			continue
		case j < len(g) && (i == len(w) || lcs[i][j+1] >= lcs[i+1][j]):
			line("+", g[j])
			j++
		default:
			line("-", w[i])
			i++
		}
	}
	return b.String()
}

const trustMatrixHeader = `# Within-team trust matrix: generated by TestTrustMatrix (internal/store/trust_matrix_test.go).
# Do not edit by hand. Rerun with SPARKWING_UPDATE_TRUST_MATRIX=1 and review the diff.
#
# Each block is one kind of run, sealed through intake's sealProvenanceTx
# (identityTx, fetchedRepositoryTx, ReadingDefault). Repository gh:7 is acme/repo
# with default branch main; the store holds a verified main cache record for it.
#
# trust                        Provenance.Trust() after sealing
# pipeline_secrets             runSecretSelection: repository and pipeline whose secrets the run reads, or no
# shared_secrets               runSecretSelection: team-wide --shared secrets (only_on ones still need the pool,
#                              and bindingSecretTx refuses them to --runner here)
# oidc_*                       oidcIdentity, which reads Provenance.OIDC(); the subject's runner segment is the
#                              claimant's kind (runner, github-actions, personal, user, service; a Cloud pod is runner) and is not shown
# oidc_runner_environment_<host>  the runner_environment claim, from the claimant's FreshHost as authority_oidc
#                              reads it: fresh only on a fresh host, whatever the team's high_trust
# cache_reads                  awardDataScope for a job claim (ReadRefs)
# default_branch_work_go       Provenance.DefaultBranchWork()
# source_credential            sourceRepositoryProven: a git credential or App token scoped to the run's repository,
#                              or only a host-wide git credential (the CLI upload carries a bundle; local a checkout)
# actions_runner_fresh_host    claimantFactsTx FreshHost for the Actions binding's signed runner_environment
# cache_write_<host>           awardDataScope WriteRef with awardCacheHostTx's writer for that host:
#                              fresh_host (launcher pod, Cloud, GitHub-hosted Actions), long_lived (plain
#                              machine, persistent or self-hosted runner) and long_lived_high_trust (the same
#                              host on a team with high_trust on); host-<machine> stands for the host's hash
# cache_write_default_branch_<host>  blob.Record.VerifiedDefault over runRefFacts: records others restore as main
# host_* / pool_*              claim SQL freshPod AND poolTrust for that claimant (SQLite, and PostgreSQL when
#                              SPARKWING_TEST_PG_URL is set, which must agree):
#   host_fresh_pod             launcher pod, no pools, default trust
#   host_plain_machine         plain machine runner, no pools, default trust
#   host_persistent_k8s        persistent Kubernetes runner, no pools, default trust
#   pool_reserved              launcher pool a secret is only_on, default trust
#   pool_reserved_trust_any    the same pool registered --trust=any
#   pool_verified_only         launcher pool registered --trust=verified-default
#   pool_reserved_on_machine   plain machine in a reserved pool, default trust
# default_branch_work_sql      claim SQL defaultBranchWork
# fresh_host_required          freshPod refuses persistent Kubernetes runners exactly when default_branch_work_sql
`

// matrixHostCells renders the cells the claimant's host decides: whether an
// Actions runner counts as fresh (claimantFactsTx), and on a fresh host, a
// long-lived host and a long-lived host of a high-trust team, the cache scope
// the award writes (awardCacheHostTx, awardDataScope) and whether that record is
// one others restore as the default branch (runRefFacts).
func matrixHostCells(ctx context.Context, s *Store, row trustMatrixRow, r runspec.SealedRequest) ([][2]string, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var cells [][2]string
	if row.principal.Capability.Binding.Kind == capability.BindingPush {
		facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: row.principal}}, r.Request)
		if err != nil {
			return nil, err
		}
		cells = append(cells, [2]string{"actions_runner_fresh_host", yesNo(facts.FreshHost)})
	}
	for _, host := range []struct {
		name      string
		facts     claimantFacts
		highTrust bool
	}{
		{"fresh_host", claimantFacts{FreshHost: true}, false},
		{"long_lived", claimantFacts{Host: matrixTeam + "/machine"}, false},
		{"long_lived_high_trust", claimantFacts{Host: matrixTeam + "/machine"}, true},
	} {
		if _, err := tx.execContext(ctx, `UPDATE teams SET high_trust=? WHERE name=?`, boolInt(host.highTrust), matrixTeam); err != nil {
			return nil, err
		}
		writer, err := awardCacheHostTx(ctx, tx, matrixTeam, host.facts)
		if err != nil {
			return nil, err
		}
		scope, err := awardDataScope(awardRow{team: matrixTeam, run: r.RunID, node: "job", repo: string(r.Request.Git.Repository.Key), attempt: 1}, r, capability.Capability{Unit: capability.UnitNode}, writer)
		if err != nil {
			return nil, err
		}
		facts, err := runRefFacts(r, writer)
		if err != nil {
			return nil, err
		}
		record := blob.Record{Key: blob.RecordKey{Scope: blob.Scope{Ref: facts.Scope}}, RefFacts: facts}
		write := scope.WriteRef
		if writer != "" {
			write = strings.ReplaceAll(write, writer, hostWriterPrefix+"<machine>")
		}
		environment := "persistent"
		if host.facts.FreshHost {
			environment = "fresh"
		}
		cells = append(cells, [2]string{"cache_write_" + host.name, write}, [2]string{"cache_write_default_branch_" + host.name, yesNo(record.VerifiedDefault())}, [2]string{"oidc_runner_environment_" + host.name, environment})
	}
	return cells, nil
}
