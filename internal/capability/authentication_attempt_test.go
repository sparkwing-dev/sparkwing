package capability

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"testing/quick"
)

func TestControllerClaimProjectsOnlyItsCurrentAttemptProperty(t *testing.T) {
	property := func(ordinal uint16, mutation uint8) bool {
		r, presentation, route := authenticationFixture()
		c := &r.snapshot.Authorities[0].Principal.Capability
		c.Binding.Attempt = 0
		c.Data = &DataScope{Use: DataClaim, RunID: "run", NodeID: "job", Generation: c.Binding.Generation}
		current := &r.snapshot.Binding.Current
		current.Attempt = int64(ordinal) + 1
		valid := mutation%5 == 0
		switch mutation % 5 {
		case 1:
			current.Generation++
		case 2:
			current.Holder += "-other"
		case 3:
			c.Binding.Attempt = current.Attempt + 1
		case 4:
			current.NodeID += "-other"
		}
		auth, err := NewAuthenticator(r).Authenticate(t.Context(), presentation, route)
		if !valid {
			return err != nil
		}
		if err != nil || auth.Principal.Capability.Binding.Attempt != current.Attempt || auth.Principal.Capability.Data.Attempt != current.Attempt || c.Binding.Attempt != 0 || c.Data.Attempt != 0 {
			return false
		}
		secret := sha256.Sum256([]byte("attempt-bound process"))
		presentation.Raw = NodeHostPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
		c.Digest, c.Audience = sha256.Sum256([]byte(presentation.Raw)), NodeHost
		r.snapshot.Binding.ProcessAlive = true
		if _, err := NewAuthenticator(r).Authenticate(t.Context(), presentation, route); err == nil {
			return false
		}
		c.Binding.Attempt = current.Attempt
		if _, err := NewAuthenticator(r).Authenticate(t.Context(), presentation, route); err != nil {
			return false
		}
		current.Attempt++
		_, err = NewAuthenticator(r).Authenticate(t.Context(), presentation, route)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}
