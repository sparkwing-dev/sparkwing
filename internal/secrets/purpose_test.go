package secrets

import (
	"testing"
	"testing/quick"
)

var allPurposes = []Purpose{PurposeSecret, PurposeGitCredential, PurposeRunEnvironment, PurposeSetting}

func TestBoundEnvelopeOpensOnlyForItsPurposeProperty(t *testing.T) {
	c, err := NewCipher(make([]byte, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	property := func(team, name, scope, plain string, shared, masked bool, sealedAs, openedAs uint8) bool {
		seal, open := allPurposes[int(sealedAs)%len(allPurposes)], allPurposes[int(openedAs)%len(allPurposes)]
		env, err := c.SealBound(seal, team, name, scope, shared, masked, plain)
		if err != nil {
			return false
		}
		got, err := c.OpenBound(open, team, name, scope, shared, masked, env)
		if seal == open {
			return err == nil && got == plain
		}
		return err != nil && got == ""
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 2000}); err != nil {
		t.Fatal(err)
	}
}

func TestBoundEnvelopeRefusesAnUnknownPurpose(t *testing.T) {
	c, err := NewCipher(make([]byte, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SealBound("", "team", "TOKEN", "", false, true, "value"); err == nil {
		t.Fatal("sealed without a purpose")
	}
	env, err := c.SealBound(PurposeSecret, "team", "TOKEN", "", false, true, "value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenBound("other", "team", "TOKEN", "", false, true, env); err == nil {
		t.Fatal("opened under an unknown purpose")
	}
}
