package capability

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestProvisionSeedOriginProperty(t *testing.T) {
	property := func(secret [32]byte) bool {
		raw := ControllerPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
		for _, kind := range []ProvisionKind{ProvisionBootstrap, ProvisionMember, ProvisionTeamAdmin, ProvisionMachine, ProvisionService, ProvisionActions, ""} {
			spec := ProvisionSpec{Kind: kind, Seed: raw}
			if (ValidateProvisionSeed(spec) == nil) != (kind == ProvisionBootstrap) {
				return false
			}
			encoded, err := json.Marshal(spec)
			if err != nil || strings.Contains(string(encoded), raw) {
				return false
			}
			var decoded ProvisionSpec
			if json.Unmarshal([]byte(`{"Seed":"`+raw+`","seed":"`+raw+`"}`), &decoded) != nil || decoded.Seed != "" {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionSeedRefusesConstantPayloadsWithGenerator(t *testing.T) {
	for value := range 256 {
		raw := ControllerPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(value)}, 32))
		err := ValidateProvisionSeed(ProvisionSpec{Kind: ProvisionBootstrap, Seed: raw})
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.BadRequest {
			t.Errorf("constant payload %d accepted or misclassified: %v", value, err)
			continue
		}
		if !strings.Contains(refusal.Message, "random") || !strings.Contains(refusal.Hint, "openssl rand -base64 32") || !strings.Contains(refusal.Hint, "bootstrap-admin-token") {
			t.Errorf("constant payload %d lacks repair: %+v", value, refusal)
		}
		if strings.Contains(refusal.Message+refusal.Hint, raw) {
			t.Fatal("refusal exposes bootstrap token")
		}
	}
	for range 16 {
		var payload [32]byte
		if _, err := rand.Read(payload[:]); err != nil {
			t.Fatal(err)
		}
		raw := ControllerPrefix + base64.RawURLEncoding.EncodeToString(payload[:])
		if err := ValidateProvisionSeed(ProvisionSpec{Kind: ProvisionBootstrap, Seed: raw}); err != nil {
			t.Fatalf("random seed refused: %v", err)
		}
	}
}
