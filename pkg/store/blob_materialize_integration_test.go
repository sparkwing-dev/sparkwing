package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type materializationController struct {
	controllerwire.Controller
	engine *controller.EngineController
	owner  capability.Authorized
}

func (c materializationController) ReconcileJob(ctx context.Context, run, job string, request controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	owner := c.owner
	owner.Target.RunID, owner.Target.NodeID = run, job
	response, err := c.engine.ReconcileJob(ctx, owner, request)
	if err != nil {
		return response, err
	}
	body, err := json.Marshal(response)
	if err == nil {
		err = json.Unmarshal(body, &response)
	}
	return response, err
}

type materializationTransfers struct{ objects *blob.Filesystem }

func (c materializationTransfers) Transfer(ctx context.Context, transfer blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	response := httptest.NewRecorder()
	c.objects.ServeHTTP(response, httptest.NewRequest(http.MethodGet, transfer.URL, body).WithContext(ctx))
	if response.Code != http.StatusOK {
		return nil, fmt.Errorf("signed GET: %d %s", response.Code, response.Body)
	}
	return io.NopCloser(bytes.NewReader(response.Body.Bytes())), nil
}

type materializationSigner struct {
	blob.DataSigner
	tenant  *store.Tenant
	objects *blob.Filesystem
	sizes   map[blob.Digest]int64
}

func (s materializationSigner) Download(ctx context.Context, request blob.GetRequest) (blob.SignedTransfer, error) {
	present, err := s.tenant.ReadableBlobs(ctx, blob.HasManyRequest{Authority: request.Authority, Digests: []blob.Digest{request.Digest}})
	if err != nil || len(present.Present) != 1 {
		return blob.SignedTransfer{}, fmt.Errorf("unauthorized download: %w", err)
	}
	key, err := blob.ObjectKey("a", request.Digest)
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	signed, err := s.objects.Sign(ctx, capability.ObjectToSign{Method: http.MethodGet, Key: key, Size: s.sizes[request.Digest], SHA256: string(request.Digest), ExpiresAt: time.Now().Add(time.Minute)})
	return blob.SignedTransfer{URL: signed.URL, Size: signed.Size, Digest: request.Digest}, err
}

func TestMaterializedResultCopiesBytesThroughControllerAndHost(t *testing.T) {
	for _, damage := range []string{"none", "missing_output", "corrupt_output", "corrupt_manifest"} {
		t.Run(damage, func(t *testing.T) { checkMaterializedResultBytes(t, damage) })
	}
}

func checkMaterializedResultBytes(t *testing.T, damage string) {
	s, tenant, request := store.MaterializationTestFixture(t)
	dir := t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	output := []byte(`{"image":"sha256:copied"}`)
	digest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(output)))
	manifest, err := json.Marshal(blob.Manifest{Entries: []blob.ManifestEntry{{Path: "result", Digest: digest, Size: int64(len(output)), Mode: 0o640}}})
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[blob.Digest]int64{}
	for kind, body := range map[blob.Kind][]byte{blob.KindOutput: output, blob.KindManifest: manifest} {
		d := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(body)))
		sizes[d] = int64(len(body))
		key, err := blob.ObjectKey("a", d)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec("INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,?,1)", d, len(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec("UPDATE blob_records SET digest=? WHERE kind=? OR (?='output' AND kind='memo')", d, kind, kind); err != nil {
			t.Fatal(err)
		}
	}
	manifestDigest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(manifest)))
	if _, err := s.DB().Exec("UPDATE blob_records SET manifest_digest=? WHERE kind='memo'", manifestDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES ('a',?,?)", manifestDigest, digest); err != nil {
		t.Fatal(err)
	}
	s.SetResultBlobRecords(storage.BlobManifests{Objects: objects})
	principal, err := s.ResolveAuthority(t.Context(), request.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	remote := &nodehost.RemoteOwnerStore{Controller: materializationController{engine: &controller.EngineController{Store: s}, owner: capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "target", NodeID: "job"}}}}
	if damage != "none" {
		broken := digest
		if damage == "corrupt_manifest" {
			broken = manifestDigest
		}
		key, err := blob.ObjectKey("a", broken)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, filepath.FromSlash(key))
		if damage == "missing_output" {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request.MaterializeOnly = true
	result, err := remote.ReconcileNode(t.Context(), request)
	if damage != "none" {
		if err != nil || result.Kind != store.Dispatch || result.Node.OutputRecord != nil || result.Node.ManifestRecord != nil {
			t.Fatalf("bad bytes materialized: %+v: %v", result, err)
		}
		return
	}

	if err != nil || result.Kind != store.Materialize || result.Node.Outcome != store.NodeMemoized || result.Node.Attempt != 0 || result.Node.OutputRecord == nil || result.Node.ManifestRecord == nil {
		t.Fatalf("controller materialization: %+v: %v", result, err)
	}
	if _, err := s.DB().Exec("DELETE FROM runs WHERE team='a' AND id='run'"); err != nil {
		t.Fatal(err)
	}
	authority := blob.Authority{CapabilityRef: request.Actor.CapabilityPrefix}
	reader := nodehost.OutputRecords{Blobs: blob.NewSignedStore(blob.SignedStoreOptions{Signer: materializationSigner{tenant: tenant, objects: objects, sizes: sizes}, HTTPClient: materializationTransfers{objects}}), Records: tenant}
	got, err := reader.ReadResult(t.Context(), result.Node, authority)
	if err != nil || !bytes.Equal(got, output) {
		t.Fatalf("copied bytes: %s: %v", got, err)
	}
	hit, err := tenant.LookupRecord(t.Context(), blob.RecordLookup{Authority: authority, Key: *result.Node.ManifestRecord})
	if err != nil || !hit.Found || len(hit.Members) != 1 || hit.Members[0].Digest != digest {
		t.Fatalf("copied manifest: %+v: %v", hit, err)
	}
}
