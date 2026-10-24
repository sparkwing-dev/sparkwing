package runspec

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRetrySealsAssertedWriterForRetrier(t *testing.T) {
	source := fixture(t)
	p, err := AssertedChildProvenance(PrincipalWriter("account", "alice"), source.Request.Request.Git.Ref, source.Request.Request.Git.SHA, "build")
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.ReadingDefault("main")
	if err != nil {
		t.Fatal(err)
	}
	source.Request.Provenance = p
	source.Request.Hash, _ = CanonicalHash(source.Request)
	source.Hash, _ = CanonicalHash(source)
	plan := json.RawMessage(`{"version":2,"jobs":[]}`)
	got, err := CarryRetry(RetryInput{Request: source.Request, Spec: Optional[Spec]{Set: true, Value: source}, Plan: plan, Retrier: Actor{Kind: "account", ID: "bob"}, SourceRunID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.Provenance.Writer() != PrincipalWriter("account", "bob") {
		t.Fatalf("retry writes as original submitter: %q", got.Request.Provenance.Writer())
	}
	if got.Request.Provenance.DefaultBranch() != "main" || !got.Request.Provenance.SharedSecretsOnly() || !reflect.DeepEqual(got.Request.Request, source.Request.Request) || string(got.Plan) != string(plan) {
		t.Fatal("retry changed sealed code, inputs or trust restrictions")
	}
	if !reflect.DeepEqual(got.Spec.Value.Request, got.Request) || verifyRequest(got.Request) != nil || verifySpec(got.Spec.Value) != nil || got.Spec.Value.Hash == source.Hash {
		t.Fatal("retry did not reseal its request and accepted spec together")
	}
	if source.Request.Provenance.Writer() != PrincipalWriter("account", "alice") {
		t.Fatal("retry mutated the source")
	}
}
