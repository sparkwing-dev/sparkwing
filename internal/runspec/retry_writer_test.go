package runspec

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRetryPreservesAssertedWriter(t *testing.T) {
	for _, writer := range []Writer{PrincipalWriter("account", "alice"), RunWriter("source"), "armer/nightly"} {
		t.Run(string(writer), func(t *testing.T) {
			source := fixture(t)
			p, err := AssertedChildProvenance(writer, source.Request.Request.Git.Ref, source.Request.Request.Git.SHA, "build")
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
			if got.Request.Provenance.Writer() != writer {
				t.Fatalf("retry changed the code writer: %q", got.Request.Provenance.Writer())
			}
			if !got.Lineage.Retrier.Set || got.Lineage.Retrier.Value.ID != "bob" {
				t.Fatal("retry lost its actor")
			}
			if got.Request.Provenance.DefaultBranch() != "main" || !got.Request.Provenance.SharedSecretsOnly() || !reflect.DeepEqual(got.Request.Request, source.Request.Request) || string(got.Plan) != string(plan) {
				t.Fatal("retry changed sealed code, inputs or trust restrictions")
			}
			if !reflect.DeepEqual(got.Spec.Value.Request, got.Request) || verifyRequest(got.Request) != nil || verifySpec(got.Spec.Value) != nil || got.Spec.Value.Hash != source.Hash {
				t.Fatal("retry changed its accepted seal")
			}
			if source.Request.Provenance.Writer() != writer {
				t.Fatal("retry mutated the source")
			}
		})
	}
}
