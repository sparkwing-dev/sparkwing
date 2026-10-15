package acceptance_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStorageFilesystemManifests(t *testing.T) {
	objects, dir := storageFilesystem(t)
	checkStorageManifests(t, objects, blob.TransferClient{Transport: storageFilesystemHTTP(objects)}, func(key string, body []byte) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(key)), body, 0o600); err != nil {
			t.Fatal(err)
		}
	})
}

func checkStorageS3Manifests(t *testing.T) {
	objects, transport := storageS3(t)
	checkStorageManifests(t, objects, blob.TransferClient{Transport: transport}, func(key string, body []byte) {
		transport.objects[key] = bytes.Clone(body)
	})
}

func checkStorageFilesystemManifestRecords(t *testing.T) {
	objects, _ := storageFilesystem(t)
	if err := checkBlobManifestRecords(t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: storageFilesystemHTTP(objects)}); err != nil {
		t.Fatal(err)
	}
}

func checkStorageS3ManifestRecords(t *testing.T) {
	objects, transport := storageS3(t)
	if err := checkBlobManifestRecords(t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: transport}); err != nil {
		t.Fatal(err)
	}
}

func checkStorageManifests(t *testing.T, objects blob.ObjectBackend, transfers blob.HTTPClient, corrupt func(string, []byte)) {
	reader := storage.BlobManifests{Objects: objects}
	storageProperty(t, func(seed [16]byte, count uint8) bool {
		team := "team-" + hex.EncodeToString(seed[:])
		manifest := blob.Manifest{Entries: make([]blob.ManifestEntry, int(count%16))}
		for i := range manifest.Entries {
			manifest.Entries[i] = blob.ManifestEntry{Path: fmt.Sprintf("dir/%d", i), Digest: storageDigest(seed[:]), Size: int64(i), Mode: uint32(0o600 | i%8)}
		}
		body, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		digest := storageDigest(body)
		putManifestObject(t, objects, transfers, team, body)
		members := make([]blob.Member, len(manifest.Entries))
		for i, e := range manifest.Entries {
			members[i] = blob.Member{Team: team, ManifestDigest: digest, Digest: e.Digest, Path: e.Path, Size: e.Size, Mode: e.Mode}
		}
		if err := reader.VerifyBlobManifest(t.Context(), store.Team(team), digest, members); err != nil {
			t.Logf("verify manifest: %v", err)
			return false
		}
		if len(members) != 0 {
			badMembers := append([]blob.Member(nil), members...)
			badMembers[0].Mode++
			if err := reader.VerifyBlobManifest(t.Context(), store.Team(team), digest, badMembers); !errors.Is(err, blob.ErrIntegrity) {
				return false
			}
		}
		got, err := reader.ReadBlobManifest(t.Context(), store.Team(team), digest)
		if err != nil || !reflect.DeepEqual(got, members) {
			t.Logf("read manifest: %+v: %v", got, err)
			return false
		}
		if got, err := reader.ReadBlobManifest(t.Context(), store.Team(team+"-other"), digest); got != nil || !errors.Is(err, blob.ErrNotFound) {
			return false
		}
		for _, badTeam := range []store.Team{"../" + store.Team(team), store.Team(team) + "/../" + store.Team(team), store.Team(team) + "\\other"} {
			if got, err := reader.ReadBlobManifest(t.Context(), badTeam, digest); got != nil || err == nil {
				return false
			}
		}
		key, err := blob.ObjectKey(team, digest)
		if err != nil {
			t.Fatal(err)
		}
		bad := bytes.Clone(body)
		bad[0] ^= 1
		for _, payload := range [][]byte{bad, body[:len(body)-1], append(bytes.Clone(body), ' ')} {
			corrupt(key, payload)
			if got, err := reader.ReadBlobManifest(t.Context(), store.Team(team), digest); got != nil || !errors.Is(err, blob.ErrIntegrity) {
				return false
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		got, err = reader.ReadBlobManifest(ctx, store.Team(team), digest)
		return got == nil && errors.Is(err, context.Canceled)
	})
	for _, body := range []string{
		`{"entries":[{"path":"x","digest":"bad","size":1,"mode":420}]}`,
		fmt.Sprintf(`{"entries":[{"path":"x","digest":%q,"size":-1,"mode":420}]}`, storageDigest(nil)),
		fmt.Sprintf(`{"entries":[{"path":"x","digest":%q,"size":0,"mode":420},{"path":"x","digest":%q,"size":0,"mode":420}]}`, storageDigest(nil), storageDigest(nil)),
		`{"entries":`,
	} {
		putManifestObject(t, objects, transfers, "malformed", []byte(body))
		if got, err := reader.ReadBlobManifest(t.Context(), "malformed", storageDigest([]byte(body))); got != nil || !errors.Is(err, blob.ErrIntegrity) {
			t.Fatalf("invalid manifest accepted: %+v: %v", got, err)
		}
	}
}

func putManifestObject(t *testing.T, objects blob.ObjectBackend, transfers blob.HTTPClient, team string, body []byte) {
	t.Helper()
	digest := storageDigest(body)
	pending := "teams/" + team + "/pending/" + string(digest)
	signed, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: pending, Size: int64(len(body)), SHA256: string(digest), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	response, err := transfers.Transfer(t.Context(), blob.SignedTransfer{URL: signed.URL, Headers: signed.Headers, Size: signed.Size, Digest: digest}, bytes.NewReader(body))
	if response != nil {
		err = errors.Join(err, response.Close())
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := objects.CommitObject(t.Context(), pending, blob.Blob{Team: team, Digest: digest, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if err := objects.DeleteObject(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
}
