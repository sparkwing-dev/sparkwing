package runspec

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"testing"
	"testing/quick"
)

func unsaltedEnvironmentDigest(values map[string]string) Hash {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([][]byte, 0, len(names))
	for _, name := range names {
		entries = append(entries, []byte(name+"="+values[name]))
	}
	hash, _ := CanonicalHash(struct {
		Version int      `json:"version"`
		Entries [][]byte `json:"entries"`
	}{2, entries})
	return hash
}

func randomEnvironmentSalt() (string, error) {
	salt := make([]byte, 32)
	_, err := rand.Read(salt)
	return hex.EncodeToString(salt), err
}

func TestEnvironmentDigestIsSaltedPerSnapshotProperty(t *testing.T) {
	property := func(name, value string) bool {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return true
		}
		values := map[string]string{name: value}
		first, err := randomEnvironmentSalt()
		if err != nil {
			return false
		}
		second, err := randomEnvironmentSalt()
		if err != nil {
			return false
		}
		a, errA := EnvironmentDigest(values, first)
		b, errB := EnvironmentDigest(values, second)
		if errA != nil || errB != nil || a.Salt != first || a.Digest == b.Digest || a.Digest == unsaltedEnvironmentDigest(values) {
			return false
		}
		if VerifyEnvironment(a, map[string][]byte{name: []byte(value)}) != nil {
			return false
		}
		a.Salt = second
		return VerifyEnvironment(a, map[string][]byte{name: []byte(value)}) != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentDigestRefusesAMissingOrMalformedSalt(t *testing.T) {
	for _, salt := range []string{"", "00", strings.Repeat("g", 64), strings.Repeat("A", 64), strings.Repeat("0", 66)} {
		if _, err := EnvironmentDigest(map[string]string{"A": "b"}, salt); err == nil {
			t.Fatalf("salt %q accepted", salt)
		}
	}
	if VerifyEnvironment(EnvSnapshot{Names: []string{"A"}}, map[string][]byte{"A": []byte("b")}) == nil {
		t.Fatal("unsalted snapshot verified")
	}
}
