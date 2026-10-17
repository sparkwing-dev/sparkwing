package blob_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/quick"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestS3ManifestRequiresCompleteClosedBytes(t *testing.T) {
	body := `{"entries":[]}`
	closeErr := errors.New("close failed")
	for _, tc := range []struct {
		name, payload string
		length        int64
		closeErr      error
		cancel        bool
		want          error
	}{
		{name: "verified", payload: body, length: int64(len(body))},
		{name: "corrupt", payload: `{"entries":{}}`, length: int64(len(body)), want: blob.ErrIntegrity},
		{name: "short", payload: body[:len(body)-1], length: int64(len(body)), want: blob.ErrIntegrity},
		{name: "long", payload: body + " ", length: int64(len(body)), want: blob.ErrIntegrity},
		{name: "length", payload: body, length: int64(len(body)) + 1, want: blob.ErrIntegrity},
		{name: "close", payload: body, length: int64(len(body)), closeErr: closeErr, want: closeErr},
		{name: "canceled response", payload: body, length: int64(len(body)), cancel: true, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closes := 0
			client := s3.New(s3.Options{
				Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
				}),
				HTTPClient: s3HTTP(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.Path != "/bucket/teams/team/cas/"+string(digest(body)) {
						t.Fatalf("manifest read escaped its key: %s %s", r.Method, r.URL.Path)
					}
					if tc.cancel {
						cancel()
					}
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Length": {fmt.Sprint(tc.length)}}, ContentLength: tc.length,
						Body: closeCounter{Reader: strings.NewReader(tc.payload), closeErr: tc.closeErr, count: &closes}, Request: r,
					}, nil
				}),
			})
			objects, err := blob.NewS3(client, "bucket", "")
			if err != nil {
				t.Fatal(err)
			}
			err = objects.VerifyManifest(ctx, "team", digest(body), nil)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("empty manifest: %v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("read: %v, want %v", err, tc.want)
			}
			if closes != 1 {
				t.Fatalf("closed %d streams, want one", closes)
			}
		})
	}
}

func manifestStreamObjects(t *testing.T, body string) (*blob.Filesystem, blob.Digest) {
	t.Helper()
	root := t.TempDir()
	want := digest(body)
	key, err := blob.ObjectKey("team", want)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: root, Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	return objects, want
}

func TestManifestAdmissionDoesNotBufferObjectPadding(t *testing.T) {
	padding := strings.Repeat(" ", 16<<20)
	for name, body := range map[string]string{
		"prefix": padding + `{"entries":[]}`,
		"suffix": `{"entries":[]}` + padding,
		"inside": `{"entries":` + padding + `[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			objects, want := manifestStreamObjects(t, body)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := objects.VerifyManifest(t.Context(), "team", want, nil)
			members, readErr := objects.ReadManifest(t.Context(), "team", want)
			runtime.ReadMemStats(&after)
			if err != nil || readErr != nil || len(members) != 0 {
				t.Fatalf("padded empty manifest: %v, %v, %v", members, err, readErr)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
				t.Fatalf("empty manifest admission/read allocated %d bytes for padding", allocated)
			}
		})
	}
}

func TestManifestAdmissionRejectsOversizedStructureWithoutBuffering(t *testing.T) {
	large := strings.Repeat("x", 16<<20)
	for name, body := range map[string]string{
		"field name":    `{"` + large + `":[]}`,
		"field value":   `{"entries":[{"path":"` + large + `"}]}`,
		"member array":  `{"entries":[{}` + strings.Repeat(`,{}`, 1<<20) + `]}`,
		"unknown field": `{"entries":[],"ignored":"` + large + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			objects, want := manifestStreamObjects(t, body)
			members := []blob.Member{{Team: "team", ManifestDigest: want, Digest: digest("file"), Path: "file", Size: 4, Mode: 420}}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := objects.VerifyManifest(t.Context(), "team", want, members)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, blob.ErrIntegrity) {
				t.Fatalf("invalid structure: %v", err)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
				t.Fatalf("invalid manifest admission allocated %d bytes", allocated)
			}
		})
	}
}

func FuzzManifestVerificationMatchesSubmittedMembers(f *testing.F) {
	f.Add("file", uint8(0))
	f.Add("dir/quotes\"\\ and whitespace \t\n", uint8(255))
	f.Add("日本語", uint8(17))
	f.Fuzz(func(t *testing.T, path string, padding uint8) {
		path = strings.ToValidUTF8(path, "?")
		entry := blob.ManifestEntry{Path: path, Digest: digest("file"), Size: 4, Mode: uint32(padding)}
		encoded, err := json.MarshalIndent(blob.Manifest{Entries: []blob.ManifestEntry{entry}}, "", "\t")
		if err != nil {
			t.Fatal(err)
		}
		body := strings.Repeat(" \t\r\n", int(padding)*64) + string(encoded)
		objects, want := manifestStreamObjects(t, body)
		member := blob.Member{Team: "team", ManifestDigest: want, Path: path, Digest: entry.Digest, Size: 4, Mode: entry.Mode}
		if err := objects.VerifyManifest(t.Context(), "team", want, []blob.Member{member}); err != nil {
			t.Fatalf("matching members: %v", err)
		}
		for _, change := range []func(*blob.Member){
			func(m *blob.Member) { m.Team = "foreign" },
			func(m *blob.Member) { m.ManifestDigest = digest("different") },
			func(m *blob.Member) { m.Digest = digest("different") },
			func(m *blob.Member) { m.Path += "x" },
			func(m *blob.Member) { m.Size++ },
			func(m *blob.Member) { m.Mode++ },
		} {
			bad := member
			change(&bad)
			if err := objects.VerifyManifest(t.Context(), "team", want, []blob.Member{bad}); !errors.Is(err, blob.ErrIntegrity) {
				t.Fatalf("mismatched members: %v", err)
			}
		}
		for _, members := range [][]blob.Member{nil, {member, member}} {
			if err := objects.VerifyManifest(t.Context(), "team", want, members); !errors.Is(err, blob.ErrIntegrity) {
				t.Fatalf("mismatched membership: %v", err)
			}
		}
	})
}

func TestManifestWhitespaceCannotJoinInvalidTokens(t *testing.T) {
	fileDigest := digest("file")
	for _, tc := range []struct {
		body    string
		members []blob.Member
	}{
		{body: `{"entries":n ull}`},
		{body: fmt.Sprintf(`{"entries":[{"path":"file","digest":%q,"size":1 2,"mode":420}]}`, fileDigest), members: []blob.Member{{Path: "file", Digest: fileDigest, Size: 12, Mode: 420}}},
		{body: `{"ent ries":[]}`},
		{body: `{"entries":[]} {}`},
	} {
		objects, want := manifestStreamObjects(t, tc.body)
		for i := range tc.members {
			tc.members[i].Team, tc.members[i].ManifestDigest = "team", want
		}
		if err := objects.VerifyManifest(t.Context(), "team", want, tc.members); !errors.Is(err, blob.ErrIntegrity) {
			t.Fatalf("invalid JSON accepted: %v", err)
		}
	}
}

func TestManifestVerificationAcceptsEscapedSchema(t *testing.T) {
	escape := func(value string) string {
		var out strings.Builder
		for _, c := range value {
			fmt.Fprintf(&out, `\u%04x`, c)
		}
		return `"` + out.String() + `"`
	}
	path := strings.Repeat("a", 1024)
	fileDigest := digest("file")
	body := fmt.Sprintf(`{%s:[{%s:%s,%s:%s,%s:9223372036854775807,%s:4294967295}]}`, escape("entries"), escape("path"), escape(path), escape("digest"), escape(string(fileDigest)), escape("size"), escape("mode"))
	objects, want := manifestStreamObjects(t, body)
	member := blob.Member{Team: "team", ManifestDigest: want, Path: path, Digest: fileDigest, Size: 9223372036854775807, Mode: 4294967295}
	if err := objects.VerifyManifest(t.Context(), "team", want, []blob.Member{member}); err != nil {
		t.Fatalf("escaped schema: %v", err)
	}
}

func TestManifestAdmissionMemoryTracksSubmittedMetadata(t *testing.T) {
	property := func(seed uint32, malformed bool) bool {
		padding := strings.Repeat(" ", (1<<20)+int(seed%(3<<20)))
		body := padding + `{"entries":[]}`
		if malformed {
			body = `{"entries":[{"path":"` + strings.ReplaceAll(padding, " ", "x") + `"}]}`
		}
		objects, want := manifestStreamObjects(t, body)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := objects.VerifyManifest(t.Context(), "team", want, nil)
		runtime.ReadMemStats(&after)
		if malformed && !errors.Is(err, blob.ErrIntegrity) || !malformed && err != nil {
			return false
		}
		return after.TotalAlloc-before.TotalAlloc < 512<<10
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(1792103906))}); err != nil {
		t.Fatal(err)
	}
}
