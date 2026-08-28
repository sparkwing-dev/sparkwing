package store_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func secretRun() store.Run {
	return store.Run{
		ID:       "r1",
		Pipeline: "deploy",
		Args:     map[string]string{"token": "hunter2", "env": "prod"},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": "hunter2", "env": "prod"},
			"reproducer":                  "sparkwing run deploy --env=prod --token=hunter2",
			"inputs_hash":                 "abc123",
			"flags":                       map[string]any{"full": true},
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}
}

func TestRedactedForDisplay_RedactsArgsInvocationAndReproducer(t *testing.T) {
	got := secretRun().RedactedForDisplay()

	if got.Args["token"] != store.RedactedArgValue {
		t.Errorf("Args[token] = %q, want %q", got.Args["token"], store.RedactedArgValue)
	}
	if got.Args["env"] != "prod" {
		t.Errorf("non-secret arg was redacted: Args[env] = %q", got.Args["env"])
	}
	invArgs, _ := got.Invocation["args"].(map[string]string)
	if invArgs["token"] != store.RedactedArgValue {
		t.Errorf("invocation.args[token] = %q, want %q", invArgs["token"], store.RedactedArgValue)
	}
	if invArgs["env"] != "prod" {
		t.Errorf("non-secret invocation arg was redacted: %q", invArgs["env"])
	}
	repro, _ := got.Invocation["reproducer"].(string)
	want := "sparkwing run deploy --env=prod --token=" + store.RedactedArgValue
	if repro != want {
		t.Errorf("reproducer = %q, want %q", repro, want)
	}

	if got.Invocation["inputs_hash"] != "abc123" {
		t.Errorf("inputs_hash lost: %v", got.Invocation["inputs_hash"])
	}
	if _, ok := got.Invocation["flags"]; !ok {
		t.Error("flags block lost; the attempts dropdown reads it")
	}
}

func TestRedactedForDisplay_LeavesSourceRunUntouched(t *testing.T) {
	src := secretRun()
	_ = src.RedactedForDisplay()

	if src.Args["token"] != "hunter2" {
		t.Fatalf("source Args mutated: %q -- retry and the log masker read this", src.Args["token"])
	}
	srcInvArgs, _ := src.Invocation["args"].(map[string]string)
	if srcInvArgs["token"] != "hunter2" {
		t.Fatalf("source invocation.args mutated: %q", srcInvArgs["token"])
	}
	if repro, _ := src.Invocation["reproducer"].(string); repro != "sparkwing run deploy --env=prod --token=hunter2" {
		t.Fatalf("source reproducer mutated: %q", repro)
	}
}

func TestRedactedForDisplay_GrandfathersRunsWithoutClassification(t *testing.T) {
	old := store.Run{
		ID:   "old",
		Args: map[string]string{"token": "hunter2"},
		Invocation: map[string]any{
			"args":       map[string]string{"token": "hunter2"},
			"reproducer": "sparkwing run deploy --token=hunter2",
		},
	}
	got := old.RedactedForDisplay()
	if got.Args["token"] != "hunter2" {
		t.Errorf("old row changed: Args[token] = %q, want hunter2", got.Args["token"])
	}
	if repro, _ := got.Invocation["reproducer"].(string); repro != "sparkwing run deploy --token=hunter2" {
		t.Errorf("old row reproducer changed: %q", repro)
	}
}

func TestRedactedForDisplay_DeclaredButUnsuppliedSecretIsNotInvented(t *testing.T) {
	r := store.Run{
		ID:   "r",
		Args: map[string]string{"env": "prod"},
		Invocation: map[string]any{
			"args":                        map[string]string{"env": "prod"},
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}
	got := r.RedactedForDisplay()
	if _, ok := got.Args["token"]; ok {
		t.Errorf("invented an arg that was never supplied: %v", got.Args)
	}
	if got.Args["env"] != "prod" {
		t.Errorf("Args[env] = %q", got.Args["env"])
	}
}

func TestRedactedForDisplay_SurvivesJSONRoundTrip(t *testing.T) {
	raw, err := json.Marshal(secretRun().Invocation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	r := store.Run{
		ID:         "r1",
		Args:       map[string]string{"token": "hunter2", "env": "prod"},
		Invocation: decoded,
	}
	got := r.RedactedForDisplay()
	if got.Args["token"] != store.RedactedArgValue {
		t.Errorf("Args[token] = %q after round trip", got.Args["token"])
	}
	invArgs, _ := got.Invocation["args"].(map[string]string)
	if invArgs["token"] != store.RedactedArgValue {
		t.Errorf("invocation.args[token] = %q after round trip (%T)", invArgs["token"], got.Invocation["args"])
	}
	if repro, _ := got.Invocation["reproducer"].(string); repro != "sparkwing run deploy --env=prod --token="+store.RedactedArgValue {
		t.Errorf("reproducer = %q after round trip", repro)
	}
}

func TestRedactedForDisplay_ReproducerAnchorsOnFlagName(t *testing.T) {
	r := store.Run{
		ID:   "r",
		Args: map[string]string{"token": "true", "dry": "true"},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": "true", "dry": "true"},
			"reproducer":                  "sparkwing run deploy --dry=true --token=true",
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}
	got := r.RedactedForDisplay()
	repro, _ := got.Invocation["reproducer"].(string)
	want := "sparkwing run deploy --dry=true --token=" + store.RedactedArgValue
	if repro != want {
		t.Errorf("reproducer = %q, want %q", repro, want)
	}
}

func TestRedactedRuns_PreservesNilAndRedactsEach(t *testing.T) {
	if got := store.RedactedRuns(nil); got != nil {
		t.Errorf("nil slice became %v; encoders rely on nil vs empty", got)
	}
	src := secretRun()
	got := store.RedactedRuns([]*store.Run{&src})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Args["token"] != store.RedactedArgValue {
		t.Errorf("slice element not redacted: %q", got[0].Args["token"])
	}
	if src.Args["token"] != "hunter2" {
		t.Errorf("slice redaction mutated the source run: %q", src.Args["token"])
	}
}

func TestSecretArgNames_ReadsClassification(t *testing.T) {
	names := secretRun().SecretArgNames()
	if len(names) != 1 || names[0] != "token" {
		t.Errorf("SecretArgNames = %v, want [token]", names)
	}
	if got := (store.Run{}).SecretArgNames(); got != nil {
		t.Errorf("empty run SecretArgNames = %v, want nil", got)
	}
}

func TestInheritSecretArgs_CarriesClassificationToAMintedRun(t *testing.T) {
	src := secretRun()
	inv := store.InheritSecretArgs(nil, &src)
	if inv == nil {
		t.Fatal("no invocation allocated for a classified source")
	}
	minted := store.Run{ID: "r2", Args: map[string]string{"token": "hunter2"}, Invocation: inv}
	if got := minted.SecretArgNames(); len(got) != 1 || got[0] != "token" {
		t.Fatalf("minted classification = %v, want [token]", got)
	}
	if minted.RedactedForDisplay().Args["token"] != store.RedactedArgValue {
		t.Error("minted run does not redact")
	}

	if _, ok := inv["args"]; ok {
		t.Errorf("InheritSecretArgs carried more than the classification: %v", inv)
	}
}

func TestInheritSecretArgs_NoOpForUnclassifiedOrNilSource(t *testing.T) {
	if got := store.InheritSecretArgs(nil, nil); got != nil {
		t.Errorf("nil source produced %v, want nil", got)
	}
	plain := store.Run{ID: "r", Args: map[string]string{"env": "prod"}}
	if got := store.InheritSecretArgs(nil, &plain); got != nil {
		t.Errorf("unclassified source produced %v, want nil", got)
	}
}

func TestInheritSecretArgs_PreservesExistingInvocationKeys(t *testing.T) {
	src := secretRun()
	inv := store.InheritSecretArgs(map[string]any{"cwd": "/repo"}, &src)
	if inv["cwd"] != "/repo" {
		t.Errorf("existing key lost: %v", inv)
	}
	if _, ok := inv[store.InvocationSecretArgsKey]; !ok {
		t.Errorf("classification not added: %v", inv)
	}
}
