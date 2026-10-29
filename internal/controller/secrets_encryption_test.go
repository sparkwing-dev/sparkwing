package controller_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// secretClaim claims a job of a run GitHub proved, an App push to gh:1.
func secretClaim(t *testing.T, f *browserFixture, team store.Team, pipeline string, names ...string) string {
	t.Helper()
	return secretClaimAs(t, f, team, pipeline, true, names...)
}

func secretClaimAs(t *testing.T, f *browserFixture, team store.Team, pipeline string, verified bool, names ...string) string {
	t.Helper()
	id := string(team) + "-" + pipeline
	if err := teststore.SeedRun(f.st, t.Context(), team, store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: pipeline, Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	tenant := teststore.ForTeam(t, f.st, team)
	run, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		sha := strings.Repeat("a", 40)
		provenance, err := runspec.AppProvenance(runspec.AppFacts{Event: "push", RepositoryID: 1, Repository: "acme/repo", Ref: "refs/heads/main", SHA: sha, DeliveryKey: "delivery-" + id, DefaultBranch: "main", VerifiedAt: time.Unix(1, 0)}, runspec.Optional[runspec.PullRequest]{}, pipeline)
		if err != nil {
			t.Fatal(err)
		}
		run.Request.Provenance = provenance
		request, err := json.Marshal(run.Request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `UPDATE runs SET request_json=? WHERE team=? AND id=?`, request, team, id); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := json.Marshal(runspec.Spec{Request: run.Request, Resolved: runspec.Resolved{SecretNames: names}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `UPDATE runs SET spec_json=?,spec_hash='fixture-spec',plan_json=?,plan_hash='fixture-plan',sealed_at=1,dispatch='controller' WHERE team=? AND id=?`, spec, []byte(`{"jobs":[{"id":"job"}]}`), team, id); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES (?,?,'plan',1,1,?,?,?)`, team, id, time.Now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNode(f.st, t.Context(), store.Node{RunID: id, NodeID: "job", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNodeReady(f.st, t.Context(), id, "job"); err != nil {
		t.Fatal(err)
	}
	return "Bearer " + controller.LauncherFixtureClaim(t, f.st, id, "job")
}

func TestSecrets_EncryptionRoundTrip(t *testing.T) {
	f := newBrowserFixture(t, nil)
	for _, masked := range []bool{false, true} {
		name := "VARIABLE"
		if masked {
			name = "TOKEN"
		}
		if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": name, "value": "supersecret", "repo": "gh:1", "pipeline": "ci", "masked": masked}); code != http.StatusOK {
			t.Fatalf("write: HTTP %d %s", code, body)
		}
		row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow(name, "gh:1", "ci")
		if err != nil || !secrets.IsBound(row.Value) || strings.Contains(row.Value, "supersecret") {
			t.Fatalf("unsealed or plaintext storage: %v", err)
		}
	}
	claim := secretClaim(t, f, store.DefaultTeam, "ci", "TOKEN", "VARIABLE")
	for _, name := range []string{"TOKEN", "VARIABLE"} {
		code, body, headers := secretsRequest(t, f, "GET", "/api/v1/secrets/"+name, claim, nil)
		var got struct {
			Name, Value   string
			Masked, Bound bool
		}
		if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Name != name || got.Value != "supersecret" || got.Masked != (name == "TOKEN") || !got.Bound || headers.Get("Cache-Control") != "no-store" {
			t.Fatalf("claim roundtrip: HTTP %d %s", code, body)
		}
	}
	if code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/TOKEN?repo=gh:1&pipeline=ci", "", nil); code != http.StatusForbidden || strings.Contains(body, "supersecret") {
		t.Fatalf("masked member read: HTTP %d %s", code, body)
	}
}

func TestSecrets_SaveWarningAndWriteOnlyAccess(t *testing.T) {
	f := newBrowserFixture(t, nil)
	for _, value := range []string{"", "abc", "ééé", "abcd", "éééé"} {
		code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "TOKEN", "value": value, "repo": "gh:1", "pipeline": "ci"})
		var response controllerwire.SecretWritten
		wantWarning := ""
		if !secrets.Maskable(value) {
			wantWarning = secrets.ShortValueWarning
		}
		if code != http.StatusOK || json.Unmarshal([]byte(body), &response) != nil || response.Warning != wantWarning || !response.Secret.Masked {
			t.Fatalf("short secret: HTTP %d %s", code, body)
		}
		row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("TOKEN", "gh:1", "ci")
		if err != nil || !row.Masked || !secrets.IsBound(row.Value) {
			t.Fatalf("short secret lost protection: %+v %v", row, err)
		}
		want := controllerwire.SecretMetadata{
			Name: row.Name, Principal: row.Principal, Repo: row.RepoName, RepoKey: row.Repo, Pipeline: row.Pipeline,
			Masked: row.Masked, Shared: row.Shared, Bound: true,
			CreatedAt: row.CreatedAt.Unix(), UpdatedAt: row.UpdatedAt.Unix(),
		}
		if response.Secret != want {
			t.Fatalf("save receipt: %+v, committed metadata: %+v", response.Secret, want)
		}
		if code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/TOKEN?repo=gh:1&pipeline=ci", "", nil); code != http.StatusForbidden {
			t.Fatalf("member read: HTTP %d %s", code, body)
		}
		code, body, _ = secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "VARIABLE", "value": value, "masked": false})
		if code != http.StatusOK || strings.Contains(body, `"warning"`) {
			t.Fatalf("plain config: HTTP %d %s", code, body)
		}
	}
}

func TestSecrets_EnvelopeIsBoundToNameAndPipeline(t *testing.T) {
	for _, field := range []string{"name", "pipeline", "unscoped"} {
		t.Run(field, func(t *testing.T) { checkSecretBindingMutation(t, field, "supersecret") })
	}
}

func checkSecretBindingMutation(t *testing.T, field, private string) {
	t.Helper()
	f := newBrowserFixture(t, nil)
	// A shared row has no repository or pipeline, so the shared flag is
	// relocated on an unscoped row.
	write, repo, scope := map[string]any{"name": "TOKEN", "value": private, "repo": "gh:1", "pipeline": "ci"}, "gh:1", "ci"
	if field == "shared" {
		write, repo, scope = map[string]any{"name": "TOKEN", "value": private}, "", ""
	}
	if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", write); code != http.StatusOK {
		t.Fatalf("write: HTTP %d %s", code, body)
	}
	row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("TOKEN", repo, scope)
	if err != nil || !secrets.IsBound(row.Value) {
		t.Fatalf("bound row: %v", err)
	}
	team, pipeline := store.DefaultTeam, "ci"
	edited := *row
	switch field {
	case "name":
		edited.Name = "OTHER"
	case "pipeline":
		edited.Pipeline, pipeline = "other", "other"
	case "unscoped":
		edited.Repo, edited.Pipeline, edited.Shared, pipeline = "", "", true, "other"
	case "shared":
		edited.Shared = true
	case "masked":
		edited.Masked = false
	case "team":
		team = "other"
		if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('other',1,1)`); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown field %s", field)
	}
	claim := secretClaim(t, f, team, pipeline, "TOKEN", "OTHER")
	if err := teststore.SeedSecret(f.st, team, edited, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/"+edited.Name, claim, nil)
	var failure apierr.Envelope
	if code != http.StatusInternalServerError || json.Unmarshal([]byte(body), &failure) != nil || failure.Error.Code != apierr.Internal || failure.Error.Message != "request failed" || strings.Contains(body, private) || strings.Contains(f.logs.String(), private) {
		t.Fatalf("%s relocation: HTTP %d %s", field, code, body)
	}
	stored, err := teststore.ForTeam(t, f.st, team).GetSecretRow(edited.Name, edited.Repo, edited.Pipeline)
	if err != nil || stored.Value != row.Value || stored.Shared != edited.Shared || stored.Masked != edited.Masked {
		t.Fatalf("refused read changed the row: %v", err)
	}
	edited.Value = private
	bound, err := store.SealSecret(testCipher(t), team, edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(f.st, team, bound.Row(), time.Now()); err != nil {
		t.Fatal(err)
	}
	code, body, _ = secretsRequest(t, f, "GET", "/api/v1/secrets/"+edited.Name, claim, nil)
	var opened struct{ Value string }
	if code != http.StatusOK || json.Unmarshal([]byte(body), &opened) != nil || opened.Value != private || strings.Contains(f.logs.String(), private) {
		t.Fatalf("correctly bound control: HTTP %d %s", code, body)
	}
}

func TestSecrets_ReadRefusesEnvelopesSealedBeforeTeamBinding(t *testing.T) {
	f := newBrowserFixture(t, nil)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	f.h = f.srv.WithSecretsCipher(cipher).Handler()
	legacy, err := cipher.Seal("older secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "LEGACY", Value: legacy, Principal: "alice"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/LEGACY", "", nil)
	if status != http.StatusInternalServerError || strings.Contains(body, "older secret") {
		t.Fatalf("unbound read: HTTP %d %s", status, body)
	}
	row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("LEGACY", "", "")
	if err != nil || row.Value != legacy {
		t.Fatalf("refused read changed the stored envelope: %v", err)
	}
}

func TestSecrets_PlaintextRowFailsClosedOnceEncrypted(t *testing.T) {
	f := newBrowserFixture(t, nil)
	if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "PREDATES", Value: "plain value", Principal: "alice"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/PREDATES", "", nil)
	var failure apierr.Envelope
	if status != http.StatusInternalServerError || json.Unmarshal([]byte(body), &failure) != nil || failure.Error.Code != apierr.Internal || failure.Error.Message != "request failed" || strings.Contains(body, "plain value") || strings.Contains(body, "not sealed") {
		t.Fatalf("plaintext read: HTTP %d %s", status, body)
	}
	row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("PREDATES", "", "")
	if err != nil || row.Value != "plain value" {
		t.Fatalf("refused read changed the row: %v", err)
	}
}

func TestSecrets_EnvelopeIsBoundToAccessFields(t *testing.T) {
	for _, field := range []string{"shared", "masked"} {
		t.Run(field, func(t *testing.T) { checkSecretBindingMutation(t, field, "supersecret") })
	}
}

func TestSecretsEnvelopeBindingProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 6.1s measured in the short class; the full suite runs it")
	}
	fields := []string{"team", "name", "pipeline", "unscoped", "shared", "masked"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			property := func(value []byte) bool {
				checkSecretBindingMutation(t, field, "private-envelope-"+hex.EncodeToString(value))
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSecrets_ListNamesUnboundRows(t *testing.T) {
	f := newBrowserFixture(t, nil)
	if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "BOUND", "value": "fresh"}); code != http.StatusOK {
		t.Fatalf("write: HTTP %d %s", code, body)
	}
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := cipher.Seal("older secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "UNBOUND", Value: legacy, Principal: "alice", Masked: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets", "", nil)
	var list struct {
		Secrets []struct {
			Name  string `json:"name"`
			Bound bool   `json:"bound"`
		} `json:"secrets"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || strings.Contains(body, "older secret") || strings.Contains(body, "fresh") {
		t.Fatalf("list: HTTP %d %s", status, body)
	}
	want := map[string]bool{"BOUND": true, "UNBOUND": false}
	for _, row := range list.Secrets {
		bound, exists := want[row.Name]
		if !exists || row.Bound != bound {
			t.Fatalf("unexpected binding metadata: %+v", row)
		}
		delete(want, row.Name)
	}
	if len(want) != 0 {
		t.Fatalf("list omitted rows: %v", want)
	}
}

func TestSecretsMalformedEnvelopeReadsProperty(t *testing.T) {
	f := newBrowserFixture(t, nil)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	f.h = f.srv.WithSecretsCipher(cipher).Handler()
	property := func(value []byte, legacy bool) bool {
		private := "private-malformed-" + hex.EncodeToString(value)
		stored := private
		if legacy {
			var err error
			stored, err = cipher.Seal(private)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := teststore.SeedSecret(f.st, store.DefaultTeam, store.Secret{Name: "MALFORMED", Value: stored, Principal: "alice"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		status, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/MALFORMED", "", nil)
		row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("MALFORMED", "", "")
		var failure apierr.Envelope
		return err == nil && row.Value == stored && status == http.StatusInternalServerError && json.Unmarshal([]byte(body), &failure) == nil && failure.Error.Code == apierr.Internal && failure.Error.Message == "request failed" && !strings.Contains(body, private) && !strings.Contains(f.logs.String(), private)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestSecrets_MarkerContainingValuesAreAccepted(t *testing.T) {
	f := newBrowserFixture(t, nil)
	code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "TOKEN", "value": "prefix***suffix"})
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
}

// Decisions 66 and 68: a manual run started with a member token, naming its
// repository itself, reads no pipeline secret of that or any repository; only
// team-wide shared secrets arrive. Pipeline rows name a GitHub repository.
func TestSecrets_AssertedRunReadsOnlySharedSecrets(t *testing.T) {
	f := newBrowserFixture(t, nil)
	for _, write := range []map[string]any{
		{"name": "OWN", "value": "own-value", "repo": "gh:1", "pipeline": "ci"},
		{"name": "TEAM", "value": "team-value", "shared": true},
	} {
		if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", write); code != http.StatusOK {
			t.Fatalf("write %v: HTTP %d %s", write["name"], code, body)
		}
	}
	for _, write := range []map[string]any{
		{"name": "ORPHAN", "value": "v", "pipeline": "ci"},
		{"name": "ORPHAN", "value": "v", "repo": "https://git.example.com/acme/other", "pipeline": "ci"},
	} {
		if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", write); code != http.StatusBadRequest {
			t.Fatalf("%v: HTTP %d %s", write, code, body)
		}
	}
	claim := secretClaimAs(t, f, store.DefaultTeam, "ci", false, "OWN", "TEAM")
	for name, want := range map[string]string{"OWN": "", "TEAM": "team-value"} {
		code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets/"+name, claim, nil)
		if want == "" {
			if code == http.StatusOK || strings.Contains(body, "own-value") {
				t.Fatalf("asserted run read a pipeline secret: HTTP %d %s", code, body)
			}
			continue
		}
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("%s: HTTP %d %s", name, code, body)
		}
	}
}

// `sparkwing secrets set --only-on` failed on every call: the API dropped the
// pool, so the client's confirmation of it never matched.
func TestSecrets_WriteKeepsOnlyOnPool(t *testing.T) {
	f := newBrowserFixture(t, nil)
	code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "DEPLOY", "value": "deploy-value", "repo": "gh:1", "pipeline": "ci", "only_on": "prod"})
	var written controllerwire.SecretWritten
	if code != http.StatusOK || json.Unmarshal([]byte(body), &written) != nil || written.Secret.OnlyOn != "prod" {
		t.Fatalf("write: HTTP %d %s", code, body)
	}
	row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).GetSecretRow("DEPLOY", "gh:1", "ci")
	if err != nil || row.OnlyOn != "prod" {
		t.Fatalf("stored pool: %+v %v", row, err)
	}
	if code, body, _ := secretsRequest(t, f, "GET", "/api/v1/secrets", "", nil); code != http.StatusOK || !strings.Contains(body, `"only_on":"prod"`) {
		t.Fatalf("list lost the pool: HTTP %d %s", code, body)
	}
	if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "DEPLOY", "value": "replaced-value", "repo": "gh:1", "pipeline": "ci"}); code != http.StatusOK || !strings.Contains(body, `"only_on":"prod"`) {
		t.Fatalf("replacement without a pool lifted the restriction: HTTP %d %s", code, body)
	}
	// The pool changes after the console loaded the row; the console's value
	// replacement names no pool, so the newer pool stands.
	if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "DEPLOY", "value": "v2", "repo": "gh:1", "pipeline": "ci", "only_on": "release"}); code != http.StatusOK {
		t.Fatalf("pool change: HTTP %d %s", code, body)
	}
	if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "DEPLOY", "value": "v3", "masked": true, "repo": "gh:1", "pipeline": "ci"}); code != http.StatusOK || !strings.Contains(body, `"only_on":"release"`) {
		t.Fatalf("console replacement restored a stale pool: HTTP %d %s", code, body)
	}
	if code, body, _ := secretsRequest(t, f, "POST", "/api/v1/secrets", "", map[string]any{"name": "DEPLOY", "value": "v", "only_on": "bad pool\n"}); code != http.StatusBadRequest {
		t.Fatalf("invalid pool: HTTP %d %s", code, body)
	}
}
