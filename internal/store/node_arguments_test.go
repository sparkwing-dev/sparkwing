package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestNodeArgumentDefaultsLeaveMetadataAndHashesIntact(t *testing.T) {
	s := bindingStore(t)
	raw := []byte(`{"id":"alpha","arguments":[{"name":"token","type":"string","default":"binding-canary-91f2","unknown":9007199254740993}],"steps":[{"id":"body"}],"deps":["producer"],"memo":{},"children":[{"pipeline":"child"}],"closures":{"skip_if":["guard"]},"unknown":9007199254740993}`)
	stored, err := encodeNodeSpec(s.runEnvCipher, "a", "run", "alpha", raw)
	if err != nil || bytes.Contains(stored, []byte(bindingCanary)) {
		t.Fatalf("default stored in plaintext: %v", err)
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(stored, &metadata) != nil || string(metadata["id"]) != `"alpha"` || string(metadata["unknown"]) != "9007199254740993" || metadata["steps"] == nil || metadata["deps"] == nil || metadata["memo"] == nil || metadata["children"] == nil || metadata["closures"] == nil {
		t.Fatal("node metadata was hidden")
	}
	plain, err := decodeNodeSpec(s, "a", "run", "alpha", stored)
	if err != nil {
		t.Fatal(err)
	}
	before, err := runspec.CanonicalHash(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	after, err := runspec.CanonicalHash(json.RawMessage(plain))
	if err != nil || before != after {
		t.Fatalf("node hash changed: %v", err)
	}
	if _, err := decodeNodeSpec(s, "a", "other", "alpha", stored); err == nil {
		t.Fatal("node defaults opened under another run")
	}
	if _, err := decodeNodeSpec(s, "a", "run", "other", stored); err == nil {
		t.Fatal("node defaults opened under another node")
	}
}

func TestMalformedArgumentDocumentsNeverSkipSealing(t *testing.T) {
	s := bindingStore(t)
	raw := []byte(`{"arguments":[{"default":42}],"private":"binding-canary-91f2"}`)
	stored, err := encodeJobArgumentBody(s, "a", "run", planArgumentsScope, raw)
	if err != nil || bytes.Contains(stored, []byte(bindingCanary)) {
		t.Fatalf("malformed document bypassed sealing: %v", err)
	}
	if _, err := encodeNodeSpec(s.runEnvCipher, "a", "run", "node", raw); err == nil {
		t.Fatal("malformed node arguments were accepted")
	}
}
