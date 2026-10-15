package capability

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"testing/quick"
)

func TestProvisionSeedOriginProperty(t *testing.T) {
	property := func(secret [32]byte) bool {
		raw := ControllerPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
		for _, kind := range []ProvisionKind{ProvisionBootstrap, ProvisionMember, ProvisionMachine, ProvisionService, ProvisionActions, ""} {
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
