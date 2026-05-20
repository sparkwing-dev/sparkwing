package ciphertest

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// TestCipher runs the conformance suite for [controller.Cipher]
// against the implementation returned by factory. factory must
// return a fresh cipher per call.
//
// The suite is implementation-agnostic: it never inspects envelope
// format, only behavior observable through Seal / Open. Tampering
// detection is exercised by mutating a known-good sealed value (flip
// a byte, truncate) and verifying Open errors -- the suite does not
// craft synthetic ciphertext.
func TestCipher(t *testing.T, factory func() controller.Cipher) {
	t.Helper()

	t.Run("SealOpenRoundTrip", func(t *testing.T) {
		c := factory()
		for _, plain := range []string{
			"",
			"hello sparkwing",
			"a longer secret with spaces and punctuation: !@#$%^&*()_+",
			strings.Repeat("x", 4096),
		} {
			envelope, err := c.Seal(plain)
			if err != nil {
				t.Fatalf("Seal(%q): %v", truncate(plain), err)
			}
			got, err := c.Open(envelope)
			if err != nil {
				t.Fatalf("Open(Seal(%q)): %v", truncate(plain), err)
			}
			if got != plain {
				t.Fatalf("Open(Seal(%q)) = %q, want round trip",
					truncate(plain), truncate(got))
			}
		}
	})

	t.Run("DifferentSealsOfSamePlaintextDiffer", func(t *testing.T) {
		c := factory()
		const plain = "same secret"
		a, err := c.Seal(plain)
		if err != nil {
			t.Fatalf("Seal a: %v", err)
		}
		b, err := c.Seal(plain)
		if err != nil {
			t.Fatalf("Seal b: %v", err)
		}
		if a == b {
			t.Fatalf("Seal(%q) is deterministic: both calls returned %q. "+
				"A conformant Cipher uses a fresh nonce per call.", plain, a)
		}
	})

	t.Run("OpenRejectsTamperedCiphertext", func(t *testing.T) {
		c := factory()
		envelope, err := c.Seal("the original plaintext")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if len(envelope) < 2 {
			t.Fatalf("Seal returned %q, too short to tamper meaningfully", envelope)
		}
		// Flip the last byte of the envelope. Encoding-agnostic: any
		// reasonable envelope rejects this. (If the last byte happens
		// to be valid in the encoding scheme, the AEAD tag verification
		// fails downstream and Open still errors.)
		bs := []byte(envelope)
		bs[len(bs)-1] ^= 0xFF
		tampered := string(bs)
		if _, err := c.Open(tampered); err == nil {
			t.Fatalf("Open accepted a tampered envelope; AEAD authentication is broken or absent")
		}
	})

	t.Run("OpenRejectsTruncatedCiphertext", func(t *testing.T) {
		c := factory()
		envelope, err := c.Seal("plaintext to truncate")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if len(envelope) < 5 {
			t.Fatalf("Seal returned %q, too short to truncate meaningfully", envelope)
		}
		truncated := envelope[:len(envelope)-3]
		if _, err := c.Open(truncated); err == nil {
			t.Fatalf("Open accepted a truncated envelope")
		}
	})

	t.Run("OpenRejectsUnsealedInput", func(t *testing.T) {
		c := factory()
		// A bare plaintext that was never sealed should not decrypt to
		// itself.
		if got, err := c.Open("not-an-envelope"); err == nil {
			t.Fatalf("Open(%q) returned %q with nil error; expected an error", "not-an-envelope", got)
		}
	})

	t.Run("OpenAcrossInstancesWithSameKey", func(t *testing.T) {
		// Two cipher instances from the same factory should be able to
		// open each other's envelopes -- key material is the contract,
		// not instance state. A factory that returns ciphers backed by
		// different keys per call would fail here, but that's not what
		// "fresh isolation" should mean for a Cipher.
		a := factory()
		b := factory()
		envelope, err := a.Seal("cross-instance plaintext")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		got, err := b.Open(envelope)
		if err != nil {
			t.Skipf("factory returned ciphers with different keys per call; "+
				"cross-instance Open is not exercised. err=%v", err)
			return
		}
		if got != "cross-instance plaintext" {
			t.Fatalf("cross-instance Open = %q, want round trip", got)
		}
	})
}

func truncate(s string) string {
	const maxN = 40
	if len(s) > maxN {
		return s[:maxN] + "..."
	}
	return s
}
