package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestScheduleArgumentEnvelopeNamesRemainReadable(t *testing.T) {
	cipher := bindingStore(t).runEnvCipher
	envelope, err := cipher.SealBound(secrets.PurposeRunArguments, "a", "schedule", scheduleArgumentsScope("args"), false, true, "opaque user value")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(map[string]string{"body": "", "sealed_arguments": envelope})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{string(legacy), `{"sealed_arguments":"x"}`, `{"body":"","sealed_arguments":"x"}`, `{"argument_document_version":"1","body":"","sealed_arguments":"x"}`} {
		for _, s := range []*Store{{}, bindingStore(t)} {
			plain, err := decodeScheduleDocument(s, "a", "schedule", "args", []byte(raw))
			if err != nil || !bytes.Equal(plain, []byte(raw)) {
				t.Fatalf("legacy argument names became an envelope: %v", err)
			}
			stored, err := encodeScheduleDocument(s, "a", "schedule", "args", []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			plain, err = decodeScheduleDocument(s, "a", "schedule", "args", stored)
			if err != nil || !bytes.Equal(plain, []byte(raw)) {
				t.Fatalf("argument names did not round trip: %v", err)
			}
		}
	}
}
