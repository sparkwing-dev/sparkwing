package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type physicalBlobManifests struct{ *blob.Filesystem }

func (p physicalBlobManifests) ReadBlobManifest(ctx context.Context, team Team, digest blob.Digest) ([]blob.Member, error) {
	return p.ReadManifest(ctx, string(team), digest)
}

func (p physicalBlobManifests) VerifyBlobManifest(ctx context.Context, team Team, digest blob.Digest, members []blob.Member) error {
	return p.VerifyManifest(ctx, string(team), digest, members)
}

func TestBlobRecordManifestAdmissionDoesNotBufferObjectPadding(t *testing.T) {
	_, records, intent := blobRecordFixture(t)
	body := strings.Repeat(" \t\r\n", 4<<20) + `{"entries":[]}`
	intent.Digest = blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte(body))))
	intent.Size = int64(len(body))
	if _, err := commitTestBlob(t, records.tenant, intent); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	key, err := blob.ObjectKey(string(records.tenant.team), intent.Digest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
	records.manifests = physicalBlobManifests{objects}
	request := blobOutputRequest(intent)
	request.Key.Kind = blob.KindManifest
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	record, err := records.CommitRecord(t.Context(), request)
	runtime.ReadMemStats(&after)
	if err != nil || record.Digest != intent.Digest {
		t.Fatalf("manifest admission: %+v, %v", record, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("empty manifest commit allocated %d bytes", allocated)
	}
}
