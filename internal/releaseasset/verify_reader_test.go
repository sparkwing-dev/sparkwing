package releaseasset

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"
)

func readerFixture(t testing.TB, body []byte) ([]ed25519.PublicKey, []byte, []byte, Target, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Binary: Sparkwing, GOOS: "linux", GOARCH: "arm64"}
	names, err := NamesForVersion("v0.72.1")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	var manifest strings.Builder
	for _, name := range names {
		fmt.Fprintf(&manifest, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	text := []byte(manifest.String())
	return []ed25519.PublicKey{public}, text, ed25519.Sign(private, text), target, ed25519.Sign(private, body)
}

func TestVerifyReaderSnapshot(t *testing.T) {
	body := []byte("authenticated executable")
	keys, manifest, signature, target, assetSignature := readerFixture(t, body)
	verified, err := VerifyReaderForVersion(keys, "v0.72.1", manifest, signature, target, bytes.NewReader(body), int64(len(body)), assetSignature)
	if err != nil {
		t.Fatal(err)
	}
	body[0] ^= 1
	var installed bytes.Buffer
	if _, err := verified.WriteTo(&installed); err != nil {
		t.Fatal(err)
	}
	if installed.String() != "authenticated executable" {
		t.Fatal("reader mutation changed authenticated snapshot")
	}
	copied := verified.Bytes()
	copied[0] ^= 1
	if verified.Bytes()[0] == copied[0] {
		t.Fatal("Bytes exposed mutable authenticated data")
	}
}

func TestVerifyReaderRefusesChangedAssets(t *testing.T) {
	body := []byte("authenticated executable")
	keys, manifest, signature, target, assetSignature := readerFixture(t, body)
	for _, test := range []struct {
		name string
		body []byte
		size int64
	}{
		{"tampered", []byte("Authenticated executable"), int64(len(body))},
		{"truncated", body[:len(body)-1], int64(len(body))},
		{"swapped", bytes.Repeat([]byte("x"), len(body)), int64(len(body))},
		{"grown", append(bytes.Clone(body), 'x'), int64(len(body))},
		{"short-declared", body, int64(len(body) - 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := VerifyReaderForVersion(keys, "v0.72.1", manifest, signature, target, bytes.NewReader(test.body), test.size, assetSignature); err == nil {
				t.Fatal("changed asset accepted")
			}
		})
	}
}

func BenchmarkVerifyReader(b *testing.B) {
	body := bytes.Repeat([]byte("x"), 100<<20)
	keys, manifest, signature, target, assetSignature := readerFixture(b, body)
	for _, stream := range []bool{false, true} {
		name := "before-clone"
		if stream {
			name = "after-reader"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				var verified Verified
				var err error
				if stream {
					verified, err = VerifyReaderForVersion(keys, "v0.72.1", manifest, signature, target, bytes.NewReader(body), int64(len(body)), assetSignature)
				} else {
					input := make([]byte, len(body))
					if _, err = io.ReadFull(bytes.NewReader(body), input); err == nil {
						verified, err = VerifyForVersion(keys, "v0.72.1", manifest, signature, target, input, assetSignature)
					}
				}
				if err != nil || verified.Digest() == "" {
					b.Fatalf("verification: %v", err)
				}
			}
		})
	}
}

func TestVerifyReaderRefusesOversizedSnapshot(t *testing.T) {
	for _, size := range []int64{1 << 62, (1 << 30) + 1, -1} {
		_, err := VerifyReaderForVersion(nil, "v0.72.1", nil, nil, Target{}, bytes.NewReader(nil), size, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid release asset size") {
			t.Fatalf("size %d: expected size refusal before reading, got %v", size, err)
		}
	}
}
