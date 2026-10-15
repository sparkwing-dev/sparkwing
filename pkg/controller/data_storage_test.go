package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type failedDataDeletion struct{ blob.ObjectBackend }

func (failedDataDeletion) DeleteBlob(context.Context, blob.Blob) error {
	return errors.New("object service unavailable")
}

func TestDataStorageCollectionFailureStillResetsZeroRateBilling(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now()
	pub, priv := licensetest.NewKey(t)
	raw := licensetest.Sign(t, priv, licensetest.Terms{Features: []string{license.FeatureMetering}, IssuedTo: "fixture", IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
	server := New(st, nil).WithDataObjects(failedDataDeletion{}).WithLicense(license.Resolve(raw, pub, now, nil))
	if !server.Metering() {
		t.Fatal("fixture has no metering authority")
	}
	rate := int64(0)
	if _, err := st.AsOperator().SetCreditSettings(t.Context(), store.CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if _, err := st.DB().ExecContext(t.Context(), "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,1,?)", store.DefaultTeam, digest, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(t.Context(), "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES (?,?,?)", store.DefaultTeam, digest, now.Add(-2*limits.BlobGCGrace).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(t.Context(), "INSERT INTO team_storage(team,store,storage_charged_through,updated_at) VALUES (?,'cache',123,?)", store.DefaultTeam, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	server.maintainRetainedData(t.Context())
	var through int64
	if err := st.DB().QueryRowContext(t.Context(), "SELECT storage_charged_through FROM team_storage WHERE team=? AND store='cache'", store.DefaultTeam).Scan(&through); err != nil {
		t.Fatal(err)
	}
	if through != 0 {
		t.Fatalf("collection failure skipped zero-rate billing reset: %d", through)
	}
}

func TestDataStorageMaintenancePreservesReachableTenantBytes(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{
		Directory: filepath.Join(root, "data"), Endpoint: "https://controller.invalid",
		SigningKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	server := New(st, nil).WithDataObjects(objects)
	random := rand.New(rand.NewPCG(66, 33))
	type object struct {
		path string
		keep bool
		body []byte
	}
	var seeded []object
	now := time.Now()
	for teamIndex := range 2 {
		team := fmt.Sprintf("maintenance-%d", teamIndex)
		if _, err := st.DB().ExecContext(t.Context(), "INSERT INTO teams(name,created_at,updated_at) VALUES (?,?,?)", team, now.UnixNano(), now.UnixNano()); err != nil {
			t.Fatal(err)
		}
		for i := range 32 {
			body := []byte(fmt.Sprintf("payload-%d", i))
			digest := fmt.Sprintf("%x", sha256.Sum256(body))
			path := filepath.Join(root, "data", "teams", team, "cas", digest)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			live := random.Uint32N(2) == 1
			seeded = append(seeded, object{path, live || i%3 != 0, body})
			if _, err := st.DB().ExecContext(t.Context(), "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?)", team, digest, len(body), now.UnixNano()); err != nil {
				t.Fatal(err)
			}
			if i%3 != 2 {
				marked := now
				if i%3 == 0 {
					marked = now.Add(-2 * limits.BlobGCGrace)
				}
				if _, err := st.DB().ExecContext(t.Context(), "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES (?,?,?)", team, digest, marked.UnixNano()); err != nil {
					t.Fatal(err)
				}
			}
			if live {
				if _, err := st.DB().ExecContext(t.Context(), `INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at) VALUES (?,'source_bundle','root',?,?,'gh:1','manual:main','fixture','local',?,'publication','retired',0,0,?,?)`, team, fmt.Sprint(i), digest, []byte(`{}`), []byte(`{}`), now.UnixNano()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	server.maintainRetainedData(t.Context())
	for _, item := range seeded {
		body, err := os.ReadFile(item.path)
		if item.keep {
			if err != nil || string(body) != string(item.body) {
				t.Fatalf("retained bytes: %q, %v", body, err)
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("unreachable bytes remain: %q, %v", body, err)
		}
	}
	server.maintainRetainedData(t.Context())
}

func TestDataStorageMaintenanceHonorsForeignLease(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: filepath.Join(root, "data"), Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	key, err := blob.LogObjectKey(blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "tenant", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: 1}}, "r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "data", filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("expired"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "other", time.Hour); err != nil {
		t.Fatal(err)
	}
	New(st, nil).WithDataObjects(objects).maintainRetainedData(t.Context())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("foreign holder removed bytes: %v", err)
	}
	if _, err := st.DB().ExecContext(t.Context(), "UPDATE leases SET until=0 WHERE name='maintenance'"); err != nil {
		t.Fatal(err)
	}
	New(st, nil).WithDataObjects(objects).maintainRetainedData(t.Context())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired bytes remain: %v", err)
	}
}
