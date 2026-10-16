package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
)

func FuzzObjectOutputRequiresVerifiedJSON(f *testing.F) {
	for variant := uint8(0); variant < 6; variant++ {
		f.Add([]byte("output"), variant)
	}
	f.Fuzz(func(t *testing.T, input []byte, variant uint8) {
		body, err := json.Marshal(string(input))
		if err != nil {
			t.Fatal(err)
		}
		if variant%6 == 4 {
			body = []byte("null")
		}
		if variant%6 == 5 {
			body = []byte("invalid JSON")
		}
		sum := sha256.Sum256(body)
		object := Blob{Team: "team", Digest: Digest(hex.EncodeToString(sum[:])), Size: int64(len(body))}
		stored := bytes.Clone(body)
		switch variant % 6 {
		case 1:
			stored[0] ^= 1
		case 2:
			stored = append(stored, 'x')
		case 3:
			object.Size++
		}
		r := newVerifiedReader(t.Context(), io.NopCloser(bytes.NewReader(stored)), SignedTransfer{Digest: object.Digest, Size: int64(len(body))})
		got, err := readObjectOutput(r, nil, object)
		valid := variant%6 == 0 || variant%6 == 4
		if valid && (err != nil || !bytes.Equal(got, body)) || !valid && (err == nil || got != nil) {
			t.Fatalf("variant %d: output=%q error=%v", variant, got, err)
		}
	})
}
