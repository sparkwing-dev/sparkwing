package acceptance_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func checkStorageFilesystemPublication(t *testing.T) {
	objects, _ := storageFilesystem(t)
	checkStoragePublication(t, objects, blob.TransferClient{Transport: storageObjectHTTP{objects}})
}

func checkStorageS3Publication(t *testing.T) {
	objects, transport := storageS3(t)
	checkStoragePublication(t, objects, blob.TransferClient{Transport: transport})
}

func checkStoragePublication(t *testing.T, objects blob.ObjectBackend, transfers blob.HTTPClient) {
	storageProperty(t, func(seed [16]byte, ordinal uint8) bool {
		identity, authority := storageIdentity(seed, uint32(ordinal))
		identity.Kind = blob.UploadPublication
		authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: identity}
		authority.Repo = "gh:1"
		authority.Ref.Scope.Ref = "main"
		signer := &storageMaterializationSigner{storageCacheSigner: storageCacheSigner{objects: objects, team: identity.Team, bodies: make(map[blob.Digest][]byte)}, tickets: make(map[string]blob.UploadIntent)}
		store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer, HTTPClient: transfers})
		records := &storageMaterializationRecords{results: make(map[blob.RecordKey]blob.RecordLookupResult)}
		budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
		output := blob.OutputData{Bytes: store, Records: records, Authority: authority, Identity: identity, Pipeline: "build", Budget: budget}
		data := artifacts.Data{Bytes: store, Records: records, Authority: authority, Identity: identity, Pipeline: "build", Budget: budget}
		workspace := t.TempDir()
		payload := bytes.Clone(seed[:ordinal%17])
		file := filepath.Join(workspace, "result")
		if err := os.WriteFile(file, payload, 0o640); err != nil {
			t.Fatal(err)
		}
		checkStoragePublicationSelectors(t, output, data, workspace, payload)
		if len(signer.tickets) != 0 || len(records.results) != 0 {
			t.Fatal("forged publication identity reached storage")
		}
		result, err := output.Put(t.Context(), payload)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := artifacts.Capture(t.Context(), data, workspace, []string{"result"}, func(warning string) { t.Fatal(warning) })
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range []blob.Record{result, manifest} {
			if record.Source != identity || record.Key.Name != identity.RunID || record.Key.Scope.Job != identity.Attempt.Value.NodeID || record.Key.Scope.Key != "" || record.Key.Scope.Ref.Verified {
				return false
			}
		}
		got, err := output.Read(t.Context(), result.Key)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("published output: %q, %v", got, err)
		}
		destination := t.TempDir()
		if n, err := artifacts.Stage(t.Context(), data, destination, []artifacts.Edge{{Record: manifest.Key}}); err != nil || n != 1 {
			t.Fatalf("published artifact: %d, %v", n, err)
		}
		if got, err := os.ReadFile(filepath.Join(destination, "result")); err != nil || !bytes.Equal(got, payload) {
			return false
		}
		records.commitErr = blob.ErrDigestNotReadable
		if record, err := output.Put(t.Context(), append(bytes.Clone(payload), ordinal)); record.Digest != "" || !errors.Is(err, records.commitErr) {
			t.Fatalf("refused publication output: %+v, %v", record, err)
		}
		if err := os.WriteFile(file, append(bytes.Clone(payload), ordinal), 0o640); err != nil {
			t.Fatal(err)
		}
		if record, err := artifacts.Capture(t.Context(), data, workspace, []string{"result"}, func(warning string) { t.Fatal(warning) }); record.Digest != "" || !errors.Is(err, records.commitErr) {
			t.Fatalf("refused publication manifest: %+v, %v", record, err)
		}
		got, err = output.Read(t.Context(), result.Key)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("output after refused publication: %q, %v", got, err)
		}
		local, err := os.ReadFile(file)
		return err == nil && bytes.Equal(local, append(bytes.Clone(payload), ordinal)) && len(records.results) == 2 && records.results[result.Key].Record == result && records.results[manifest.Key].Record == manifest
	})
}

func checkStoragePublicationSelectors(t *testing.T, output blob.OutputData, data artifacts.Data, workspace string, payload []byte) {
	t.Helper()
	for fault := range 10 {
		badOutput, badData := output, data
		bad := output.Identity
		switch fault {
		case 0:
			bad.Team += "-other"
		case 1:
			bad.RunID += "-other"
		case 2:
			bad.Attempt.Value.NodeID += "-other"
		case 3:
			bad.Attempt.Value.Attempt++
		case 4:
			bad.Attempt.Value.Generation++
		case 5:
			bad.Principal += "-other"
		case 6:
			bad.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
		case 7:
			bad.CapabilityRef += "-other"
		case 8:
			bad.Kind = blob.UploadJob
		case 9:
			badOutput.Authority.Upload = runspec.Optional[blob.UploadIdentity]{}
			badData.Authority.Upload = runspec.Optional[blob.UploadIdentity]{}
		}
		badOutput.Identity, badData.Identity = bad, bad
		if _, err := badOutput.Put(t.Context(), payload); !errors.Is(err, blob.ErrDigestNotReadable) {
			t.Fatalf("publication output selector %d: %v", fault, err)
		}
		if _, err := artifacts.Capture(t.Context(), badData, workspace, []string{"result"}, func(warning string) { t.Fatal(warning) }); !errors.Is(err, blob.ErrDigestNotReadable) {
			t.Fatalf("publication artifact selector %d: %v", fault, err)
		}
	}
}
