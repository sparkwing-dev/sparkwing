package blob_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type memoReader struct {
	io.Reader
	verified, closed bool
	closeErr         error
}

func (r *memoReader) Close() error   { r.closed = true; return r.closeErr }
func (r *memoReader) Verified() bool { return r.verified }

func TestReadMemoClosesEveryReturnedReader(t *testing.T) {
	refused := errors.New("reader refused")
	for _, fault := range []string{"none", "nil", "get", "unverified", "close"} {
		t.Run(fault, func(t *testing.T) {
			reader := &memoReader{Reader: strings.NewReader("output"), verified: fault != "unverified"}
			if fault == "close" {
				reader.closeErr = refused
			}
			get := func(context.Context, blob.Digest) (blob.VerifiedReader, error) {
				if fault == "nil" {
					return nil, nil
				}
				if fault == "get" {
					return reader, refused
				}
				return reader, nil
			}
			hit := blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: digest("output")}}
			output, manifest, err := blob.ReadMemo(t.Context(), get, "team", hit)
			if (err == nil) != (fault == "none") || fault != "nil" && !reader.closed || len(manifest) != 0 {
				t.Fatalf("fault=%s output=%s closed=%v error=%v", fault, output, reader.closed, err)
			}
			if fault == "none" && string(output) != "output" || fault != "none" && output != nil {
				t.Fatalf("unverified output escaped: %s", output)
			}
			if (fault == "get" || fault == "close") && !errors.Is(err, refused) {
				t.Fatal("reader failure discarded:", err)
			}
		})
	}
}

func TestReadMemoReturnsVerifiedManifestAndEmptyOutput(t *testing.T) {
	artifact := "artifact"
	memberDigest := digest(artifact)
	manifest := fmt.Sprintf(`{"entries":[{"path":"first","digest":"%s","size":8,"mode":420},{"path":"second","digest":"%s","size":8,"mode":420}]}`, memberDigest, memberDigest)
	manifestDigest := digest(manifest)
	bodies := map[blob.Digest]string{digest(""): "", memberDigest: artifact, manifestDigest: manifest}
	reads := make(map[blob.Digest]int)
	get := func(_ context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
		reads[digest]++
		return &memoReader{Reader: strings.NewReader(bodies[digest]), verified: true}, nil
	}
	hit := blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: digest("")}, Manifest: runspec.Optional[blob.Digest]{Set: true, Value: manifestDigest}, Members: []blob.Member{
		{Team: "team", ManifestDigest: manifestDigest, Digest: memberDigest, Path: "first", Size: 8, Mode: 0o644},
		{Team: "team", ManifestDigest: manifestDigest, Digest: memberDigest, Path: "second", Size: 8, Mode: 0o644},
	}}
	output, body, err := blob.ReadMemo(t.Context(), get, "team", hit)
	if err != nil || len(output) != 0 || string(body) != manifest || reads[memberDigest] != 1 {
		t.Fatalf("output=%s manifest=%s reads=%v error=%v", output, body, reads, err)
	}
	for _, mutation := range []string{"foreign_team", "member_size"} {
		t.Run(mutation, func(t *testing.T) {
			broken := hit
			broken.Members = append([]blob.Member(nil), hit.Members...)
			if mutation == "foreign_team" {
				broken.Members[0].Team = "foreign"
			} else {
				bodies[memberDigest] = artifact + "x"
			}
			if _, _, err := blob.ReadMemo(t.Context(), get, "team", broken); !errors.Is(err, blob.ErrIntegrity) {
				t.Fatal("invalid member accepted:", err)
			}
		})
	}
}
