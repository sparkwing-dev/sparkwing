package runspec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) Spec {
	t.Helper()
	req, err := SealRequest(Request{Version: 1, Team: "team", Pipeline: "build", Mode: IntakeExecute, Dispatch: DispatchHost, Origin: OriginApp, RequestedArgs: map[string]string{"token": "private-token", "count": "42"}, Git: Git{Repository: Repository{Key: "gh:42", Display: "owner/repo"}, SHA: "original-sha", DefaultBranch: Optional[string]{Set: true, Value: "main"}, App: Optional[AppFacts]{Set: true, Value: AppFacts{RepositoryID: 42, SHA: "original-sha", DefaultBranch: "main", DefaultBranchPush: true}}}, Actor: Actor{ID: "original"}, PullRequest: Optional[PullRequest]{Set: true, Value: PullRequest{Action: "synchronize", BaseSHA: "base-sha", Number: 3, BaseRef: "main", HeadRef: "feature", HeadSHA: "original-sha"}}}, IntakeFacts{RunID: "source", AdmittedAt: time.Unix(1, 0), AllowedPools: []string{"private"}})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := SealSpec(SealInput{Request: req, Resolved: Resolved{Args: map[string]string{"token": "private-token", "count": "42"}, SecretArgs: []string{"token"}, DefaultBranch: Optional[string]{Set: true, Value: "main"}, DefaultBranchSource: DefaultBranchApp}, Plan: json.RawMessage(`{"version":2,"jobs":[]}`), Now: time.Unix(2, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestRepositoryIdentity(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"https://user:password@EXAMPLE.com:8443/Org/Repo.git?token=hidden#secret", "git:example.com:8443/Org/Repo"},
		{"git@GitLab.com:Org/Repo.git", "git:gitlab.com/org/repo"},
		{"ssh://git@BitBucket.org:2222/Org/Repo.git", "git:bitbucket.org:2222/org/repo"},
		{"ssh://git@[2001:db8::1]:2222/Org/Repo.git", "git:[2001:db8::1]:2222/Org/Repo"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			got, err := CanonicalRepository(RepoInput{RemoteURL: tc.remote})
			if err != nil || string(got.Key) != tc.want {
				t.Fatalf("got %+v, %v", got, err)
			}
			if strings.Contains(got.Display, "password") || strings.Contains(got.Display, "hidden") {
				t.Fatal("credentials in display")
			}
		})
	}
	a, err := CanonicalRepository(RepoInput{AbsolutePath: "/tmp/a/../repo"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalRepository(RepoInput{RemoteURL: "file:///tmp/repo"})
	if err != nil || a.Key != b.Key {
		t.Fatalf("local identities differ: %v", err)
	}
	for _, use := range []RepoUse{RepoAccessRule, RepoControllerMemo} {
		if ValidateRepoUse(a.Key, use) == nil {
			t.Fatal("local controller identity accepted")
		}
	}
	if ValidateRepoUse(a.Key, RepoMachineStore) != nil {
		t.Fatal("local machine identity refused")
	}
	gh, err := CanonicalRepository(RepoInput{GitHubRepositoryID: Optional[int64]{Set: true, Value: 42}, RemoteURL: "https://github.com/renamed/repo"})
	if err != nil || gh.Key != "gh:42" {
		t.Fatal(gh, err)
	}
	for _, in := range []RepoInput{{}, {AbsolutePath: "relative"}, {GitHubRepositoryID: Optional[int64]{Set: true}}, {RemoteURL: "https://example.com/"}} {
		if _, err := CanonicalRepository(in); err == nil {
			t.Fatalf("accepted invalid input %+v", in)
		}
	}
}

func TestSealsAndReplay(t *testing.T) {
	s := fixture(t)
	copy := s
	copy.SealedAt = time.Unix(20, 0)
	copy.Request.AdmittedAt = time.Unix(10, 0)
	if ok, err := CheckSeal(s, copy); err != nil || !ok {
		t.Fatalf("timestamp replay: %v %v", ok, err)
	}
	copy = s
	copy.Resolved.Args = map[string]string{"count": "99"}
	if _, err := CheckSeal(s, copy); err == nil {
		t.Fatal("trusted forged hash")
	}
	copy = s
	copy.Declarations.File = "other.yaml"
	copy.Hash, _ = CanonicalHash(copy)
	if _, err := CheckSeal(s, copy); err == nil {
		t.Fatal("changed declaration replay accepted")
	}
	input := s.Request.Request
	sealed, err := SealRequest(input, IntakeFacts{RunID: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	input.RequestedArgs["count"] = "changed"
	if sealed.Request.RequestedArgs["count"] != "42" {
		t.Fatal("request aliases input")
	}
}

func TestCanonicalHash(t *testing.T) {
	a, err := CanonicalHash(json.RawMessage(`{"z":9007199254740993,"a":{"hash":"semantic"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CanonicalHash(json.RawMessage(`{ "a": {"hash":"semantic"}, "z":9007199254740993 }`))
	if a != b {
		t.Fatal("object order changed hash")
	}
	c, _ := CanonicalHash(json.RawMessage(`{"z":9007199254740992,"a":{"hash":"semantic"}}`))
	if a == c {
		t.Fatal("integer precision lost")
	}
	c, _ = CanonicalHash(json.RawMessage(`{"z":9007199254740993,"a":{"hash":"changed"}}`))
	if a == c {
		t.Fatal("semantic nested hash removed")
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `{} {}`, `not json`} {
		if _, err := CanonicalHash(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestPullRequestFields(t *testing.T) {
	s := fixture(t)
	plan := json.RawMessage(`{"version":2,"jobs":[]}`)
	got, err := CarryRetry(RetryInput{Request: s.Request, Spec: Optional[Spec]{Set: true, Value: s}, Plan: plan, Retrier: Actor{ID: "retrier"}, SourceRunID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Spec.Value, s) || !reflect.DeepEqual(got.Request, s.Request) || got.Lineage.RetryOf != "source" || got.Lineage.Retrier.Value.ID != "retrier" {
		t.Fatal("retry changed sealed inputs")
	}
	ctx := got.Spec.Value.RunContext()
	if ctx.Actor.ID != "original" || ctx.PullRequest.Value.Number != 3 || ctx.PullRequest.Value.Action != "synchronize" || ctx.PullRequest.Value.BaseSHA != "base-sha" || ctx.PullRequest.Value.BaseRef != "main" || ctx.PullRequest.Value.HeadRef != "feature" || ctx.PullRequest.Value.HeadSHA != "original-sha" || !ctx.Git.App.Value.DefaultBranchPush || ctx.Git.SHA != "original-sha" {
		t.Fatal("lost original facts")
	}
	ctx.Args["token"] = "mutated"
	got.Spec.Value.Resolved.Args["count"] = "mutated"
	plan[0] = 'x'
	if s.Resolved.Args["count"] != "42" || s.Resolved.Args["token"] != "private-token" || got.Plan[0] != '{' {
		t.Fatal("retry/context aliases input")
	}
	if _, err := CarryRetry(RetryInput{Request: s.Request, Spec: Optional[Spec]{Set: true, Value: s}, Plan: json.RawMessage(`{}`), SourceRunID: "source"}); err == nil {
		t.Fatal("retry accepted different plan")
	}
}

func TestSecretDisplay(t *testing.T) {
	s := fixture(t)
	for _, d := range []Display{DisplayRequest(s.Request), DisplaySpec(s)} {
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "private-token") || strings.Contains(string(b), string(s.Hash)) {
			t.Fatal("private value or hash displayed")
		}
	}
	if DisplayRequest(s.Request).RequestedArgs["count"] != RedactedArgValue {
		t.Fatal("unclassified input exposed")
	}
	if DisplaySpec(s).Args["count"] != "42" || DisplaySpec(s).Args["token"] != RedactedArgValue {
		t.Fatal("classification ignored")
	}
	for _, names := range [][]string{nil, {""}, {"token", "token"}, {"unknown"}} {
		s.Resolved.SecretArgs = names
		if DisplaySpec(s).Args["count"] != RedactedArgValue {
			t.Fatal("malformed classification did not fail closed")
		}
	}
}

func TestEnvironmentDigest(t *testing.T) {
	a, err := EnvironmentDigest(map[string]string{"B": "secret", "A": "value"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := EnvironmentDigest(map[string]string{"A": "value", "B": "secret"})
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a.Names, []string{"A", "B"}) {
		t.Fatal("unstable snapshot")
	}
	b, _ = EnvironmentDigest(map[string]string{"A": "value", "B": "different"})
	if a.Digest == b.Digest {
		t.Fatal("values not bound")
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("environment value persisted")
	}
}

func TestCanonicalNumberEquivalence(t *testing.T) {
	for _, spellings := range [][]string{{"1", "1.0", "1e0", "10e-1"}, {"0", "-0", "0e9999999999999999999999"}, {"123.45", "12345e-2", "123450e-3"}, {"1000", "1e3", "1.000E+3"}} {
		want, err := CanonicalHash(json.RawMessage(spellings[0]))
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range spellings[1:] {
			got, err := CanonicalHash(json.RawMessage(raw))
			if err != nil || got != want {
				t.Fatalf("%s differs from %s: %v", raw, spellings[0], err)
			}
		}
	}
	hash, err := CanonicalHash(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil || hash != "43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777" {
		t.Fatalf("golden hash: %s %v", hash, err)
	}
	if _, err := CanonicalHash(make(chan int)); err == nil {
		t.Fatal("unsupported input accepted")
	}
}

func TestRetryPreservesPlanBytes(t *testing.T) {
	s := fixture(t)
	plan := json.RawMessage("{\n  \"jobs\": [], \"version\": 2\n}\n")
	snapshot, err := CarryRetry(RetryInput{Request: s.Request, Spec: Optional[Spec]{Set: true, Value: s}, Plan: plan, SourceRunID: "destination-from-earlier-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Plan) != string(plan) || snapshot.Lineage.RetryOf != "destination-from-earlier-retry" {
		t.Fatal("retry did not preserve bytes or destination lineage")
	}
	snapshot, err = CarryRetry(RetryInput{Request: s.Request, SourceRunID: "source"})
	if err != nil || snapshot.Spec.Set || len(snapshot.Plan) != 0 {
		t.Fatal("preplan retry failed", err)
	}
	if _, err := CarryRetry(RetryInput{Request: s.Request, Plan: json.RawMessage(`{}`), SourceRunID: "source"}); err == nil {
		t.Fatal("unaccepted retry plan allowed")
	}
}

func TestProjectionAndContext(t *testing.T) {
	s := fixture(t)
	s.Resolved.Settings.Options.DryRun = Setting[bool]{Optional: Optional[bool]{Set: true, Value: true}}
	s.Resolved.Entrypoint = "ci/main.go"
	p := Project(s)
	if p.RepoKey != "gh:42" || p.SHA != "original-sha" || p.DefaultBranch != "main" || p.RequestHash != s.Request.Hash || p.SpecHash != s.Hash || p.PlanHash != s.PlanHash {
		t.Fatalf("projection lost inputs: %+v", p)
	}
	p.AllowedPools[0] = "mutated"
	if s.Request.AllowedPools[0] != "private" {
		t.Fatal("projection aliases seal")
	}
	c := s.RunContext()
	if !c.DryRun || c.Source.Entrypoint != "ci/main.go" || !reflect.DeepEqual(c, s.GuardContext()) {
		t.Fatal("context did not use sealed winners")
	}
	s.Request.Request.Git.App = Optional[AppFacts]{}
	s.Resolved.DefaultBranchSource = DefaultBranchOriginHEAD
	if s.GuardContext().Git.App.Set {
		t.Fatal("origin HEAD manufactured App trust")
	}
	s.Resolved.DefaultBranch = Optional[string]{}
	if Project(s).DefaultBranch != "main" {
		t.Fatal("lost intake default branch")
	}
}

func TestSealRefusals(t *testing.T) {
	s := fixture(t)
	for _, change := range []func(*Request){func(r *Request) { r.Version = 2 }, func(r *Request) { r.Team = "" }, func(r *Request) { r.Mode = "other" }, func(r *Request) { r.Dispatch = "other" }, func(r *Request) { r.Git.Repository.Key = "invalid" }} {
		request, _ := clone(s.Request.Request)
		change(&request)
		if _, err := SealRequest(request, IntakeFacts{RunID: "run"}); err == nil {
			t.Fatal("invalid request sealed")
		}
	}
	for _, change := range []func(*SealInput){func(i *SealInput) { i.Request.Request.Git.SHA = "changed" }, func(i *SealInput) { i.Resolved.SecretArgs = nil }, func(i *SealInput) { i.Resolved.DefaultBranch.Value = "other" }, func(i *SealInput) { i.Resolved.DefaultBranchSource = "other" }, func(i *SealInput) { i.Plan = json.RawMessage(`invalid`) }} {
		input := SealInput{Request: s.Request, Resolved: s.Resolved, Plan: json.RawMessage(`{}`)}
		change(&input)
		if _, err := SealSpec(input); err == nil {
			t.Fatal("invalid spec sealed")
		}
	}
	for _, key := range []RepoKey{"gh:0", "gh:01", "local:123", RepoKey("local:" + strings.Repeat("A", 64)), "git:EXAMPLE.com/a", "git:example.com/a.git", "other:123"} {
		if ValidateRepoUse(key, RepoMachineStore) == nil {
			t.Fatalf("invalid key %s allowed", key)
		}
	}
	if ValidateRepoUse("gh:42", "other") == nil {
		t.Fatal("unknown use allowed")
	}
	for _, values := range []map[string]string{{"": "value"}, {"A=B": "value"}, {"A": "bad\x00value"}} {
		if _, err := EnvironmentDigest(values); err == nil {
			t.Fatal("invalid env allowed")
		}
	}
	nilEnv, _ := EnvironmentDigest(nil)
	emptyEnv, _ := EnvironmentDigest(map[string]string{})
	if !reflect.DeepEqual(nilEnv, emptyEnv) {
		t.Fatal("empty environment digest depends on map allocation")
	}
}

func TestReproducerSeparatesPipelineFlags(t *testing.T) {
	s := fixture(t)
	if got := s.Reproducer(); !reflect.DeepEqual(got, []string{"sparkwing", "run", "build", "--", "--count=42"}) {
		t.Fatalf("wrong reproducer argv: %v", got)
	}
	s.Resolved.SecretArgs = nil
	if got := s.Reproducer(); !reflect.DeepEqual(got, []string{"sparkwing", "run", "build"}) {
		t.Fatalf("unclassified reproducer: %v", got)
	}
}

func TestGitHubIdentityRequiresResolvedID(t *testing.T) {
	if _, err := CanonicalRepository(RepoInput{RemoteURL: "https://github.com/owner/repo"}); err == nil {
		t.Fatal("GitHub acquired a competing URL-derived identity")
	}
}

func TestRequestRefusesEmbeddedSourceCredentials(t *testing.T) {
	request := fixture(t).Request.Request
	for _, ref := range []string{"https://user:private-password@example.com/repo.git", "https://example.com/repo.git?access_token=private-password", "ssh://git:private-password@example.com/repo"} {
		request.Source.Ref = ref
		_, err := SealRequest(request, IntakeFacts{RunID: "run"})
		if err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("source credential accepted or echoed: %v", err)
		}
	}
	request.Source.Ref = "ssh://git@example.com/repo"
	if _, err := SealRequest(request, IntakeFacts{RunID: "run"}); err != nil {
		t.Fatal("SSH transport username refused", err)
	}
}
