package runspec

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSecretRegistrationIncludesRawEffectiveDefaultsAndListItems(t *testing.T) {
	request := Request{
		RequestedArgs: map[string]string{"tokens": "alpha-token,\u00a0beta-token"},
		SecretArgs:    Optional[[]string]{Set: true, Value: []string{"tokens"}},
	}
	resolved := Resolved{
		Args:       map[string]string{"tokens": "alpha-token,\u00a0beta-token", "fallback": "effective-token"},
		SecretArgs: []string{"tokens", "fallback"},
		SecretRegistration: SecretRegistration{
			Defaults: []SecretValue{{Raw: "default-token"}, {Raw: "first-default,\u00a0second-default", List: true}},
			ListArgs: []string{"tokens"},
		},
	}
	got, err := BuildSecretRegistration(request, resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"default-token", "first-default,\u00a0second-default", "first-default", "second-default", "alpha-token,\u00a0beta-token", "alpha-token", "beta-token", "effective-token"} {
		if !slices.Contains(got.Values, want) {
			t.Errorf("missing registration %q in %q", want, got.Values)
		}
	}
}

func TestSecretRegistrationRefusesShortAndMarkerListItems(t *testing.T) {
	for _, raw := range []string{"ab,long-enough", "long-enough,***marker"} {
		request := Request{RequestedArgs: map[string]string{"tokens": raw}, SecretArgs: Optional[[]string]{Set: true, Value: []string{"tokens"}}}
		resolved := Resolved{Args: map[string]string{"tokens": raw}, SecretArgs: []string{"tokens"}, SecretRegistration: SecretRegistration{ListArgs: []string{"tokens"}}}
		_, err := BuildSecretRegistration(request, resolved)
		if err == nil || !strings.Contains(err.Error(), "plain config variable") {
			t.Fatalf("%q: expected plain-config refusal, got %v", raw, err)
		}
	}
}

func TestSecretRegistrationRemainsBoundAcrossPersistence(t *testing.T) {
	base := fixture(t)
	resolved := base.Resolved
	resolved.SecretRegistration.Defaults = []SecretValue{{Raw: "other-pipeline-default"}}
	resolved.SecretRegistration.Values = nil
	sealed, err := SealSpec(SealInput{Request: base.Request, Resolved: resolved, Plan: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	var restored Spec
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckSeal(sealed, restored); err != nil {
		t.Fatal(err)
	}
	restored.Resolved.SecretRegistration.Values[0] = "unregistered"
	if _, err := CheckSeal(sealed, restored); err == nil {
		t.Fatal("changed projection passed the seal")
	}
}

func TestDisplaySpecHidesGeneratorClassifiedArguments(t *testing.T) {
	spec := fixture(t)
	spec.Request.Request.RequestedArgs["generated"] = "generated-private-token"
	spec.Resolved.Args["generated"] = "generated-private-token"
	spec.Resolved.SecretRegistration.Defaults = []SecretValue{{Raw: "generated-private-token"}}
	shown := DisplaySpec(spec)
	if shown.Args["generated"] != RedactedArgValue || shown.RequestedArgs["generated"] != RedactedArgValue {
		t.Fatalf("generator classification absent from presentation: %+v", shown)
	}
	if strings.Contains(strings.Join(shown.Reproducer, " "), "generated-private-token") {
		t.Fatal("reproducer exposed generator-classified argument")
	}
}

func TestLegacySealRefusesResumeBeforeDrift(t *testing.T) {
	spec := fixture(t)
	spec.Resolved.SecretRegistration = SecretRegistration{}
	_, err := CheckSeal(spec, spec)
	if err == nil || !strings.Contains(err.Error(), "start a new run instead of resuming") {
		t.Fatalf("expected explicit resume refusal, got %v", err)
	}
}
