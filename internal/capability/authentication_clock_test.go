package capability

import (
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestAuthenticationToleratesBackwardClockStep(t *testing.T) {
	r, presentation, route := authenticationFixture()
	issued := r.snapshot.Now
	for i := range r.snapshot.Authorities {
		r.snapshot.Authorities[i].Principal.Capability.IssuedAt = issued
	}
	for _, offset := range []time.Duration{0, -3500 * time.Millisecond} {
		r.snapshot.Now = issued.Add(offset)
		if _, err := NewAuthenticator(r).Authenticate(t.Context(), presentation, route); err != nil {
			t.Fatalf("clock offset %s: %v", offset, err)
		}
	}
	r.snapshot.Now = issued.Add(time.Minute)
	_, err := NewAuthenticator(r).Authenticate(t.Context(), presentation, route)
	requireAuthenticationCode(t, err, hosterr.Unauthenticated)
}

func TestCredentialClockSkewKeepsDeadlinesStrict(t *testing.T) {
	issued := time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC)
	now := issued.Add(-3500 * time.Millisecond)
	for _, field := range []string{"expiry", "revocation", "binding deadline"} {
		t.Run(field, func(t *testing.T) {
			c := Capability{IssuedAt: issued}
			deadline := now.Add(time.Nanosecond)
			switch field {
			case "expiry":
				c.ExpiresAt = &deadline
			case "revocation":
				c.RevokedAt = &deadline
			case "binding deadline":
				c.Binding.Deadline = &deadline
			}
			if !CredentialAlive(c, now) {
				t.Fatal("credential refused before deadline after backward step")
			}
			if CredentialAlive(c, deadline) || CredentialAlive(c, deadline.Add(time.Nanosecond)) {
				t.Fatal("clock skew extended deadline")
			}
		})
	}
}
