package orchestrator_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Integration tests in this package exercise cross-runner state and
// cache sharing through the real S3 and Postgres protocols. They are
// gated by:
//
//   - SPARKWING_INTEGRATION_DISABLE=1: hard opt-out (CI smoke runs).
//   - Per-backend availability: Postgres needs SPARKWING_TEST_PG_URL;
//     S3 uses an in-process gofakes3 server so it has no external
//     prerequisite.
//
// Skip reasons are deliberately distinct so a CI test report
// distinguishes "deliberately disabled" from "no Postgres available."

const (
	integrationDisableEnv = "SPARKWING_INTEGRATION_DISABLE"
	pgTestURLEnv          = "SPARKWING_TEST_PG_URL"
)

func skipIfIntegrationDisabled(t *testing.T) {
	t.Helper()
	if os.Getenv(integrationDisableEnv) == "1" {
		t.Skip("integration tests disabled via " + integrationDisableEnv + "=1")
	}
}

// openIntegrationPostgres returns a *store.Store backed by a per-test
// schema on the shared Postgres instance. Each test gets isolation
// without the cost of spinning up a fresh database. Skips when no
// Postgres URL is configured.
func openIntegrationPostgres(t *testing.T) *store.Store {
	t.Helper()
	skipIfIntegrationDisabled(t)
	dsn := os.Getenv(pgTestURLEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping Postgres integration test", pgTestURLEnv)
	}

	schema := "sw_it_" + sanitizeName(t.Name()) + "_" + uniqSuffix()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("admin OpenPostgres: %v", err)
	}
	if _, err := admin.DB().ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	_ = admin.Close()

	scoped := withSearchPath(dsn, schema)
	st, err := store.OpenPostgres(context.Background(), scoped)
	if err != nil {
		t.Fatalf("OpenPostgres against schema %s: %v", schema, err)
	}

	t.Cleanup(func() {
		_ = st.Close()
		drop, e := store.OpenPostgres(context.Background(), dsn)
		if e == nil {
			_, _ = drop.DB().Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
			_ = drop.Close()
		}
	})
	return st
}

// openIntegrationPostgresAt opens a second connection against the
// same per-test schema as src. Used by tests that want two
// independent *store.Store handles sharing one database — the cross-
// runner pattern in production.
func openIntegrationPostgresAt(t *testing.T, src *store.Store) *store.Store {
	t.Helper()
	dsn := os.Getenv(pgTestURLEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping", pgTestURLEnv)
	}
	var searchPath string
	if err := src.DB().QueryRow(`SHOW search_path`).Scan(&searchPath); err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	schema := strings.TrimSpace(strings.Split(searchPath, ",")[0])
	scoped := withSearchPath(dsn, schema)
	st, err := store.OpenPostgres(context.Background(), scoped)
	if err != nil {
		t.Fatalf("OpenPostgres at schema %s: %v", schema, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// openIntegrationS3 returns a fresh ArtifactStore and LogStore backed
// by an in-process gofakes3 server. The server speaks the real S3
// protocol over HTTP; the storage/s3 client code under test is the
// same as production. Returned closer tears down the server when the
// test ends.
//
// gofakes3 is used in preference to a dockerized minio: it exercises
// the same protocol code, avoids the Docker dependency, and runs in
// well under a second. Cross-runner sharing is the assertion target,
// not the durability properties of the object store.
func openIntegrationS3(t *testing.T) (storage.ArtifactStore, storage.LogStore) {
	t.Helper()
	skipIfIntegrationDisabled(t)

	backend := s3mem.New()
	faker := gofakes3.New(backend)
	srv := httptest.NewServer(faker.Server())
	t.Cleanup(srv.Close)

	client := awss3.New(awss3.Options{
		Region:             "us-east-1",
		BaseEndpoint:       aws.String(srv.URL),
		UsePathStyle:       true,
		Credentials:        credentials.NewStaticCredentialsProvider("test", "test", ""),
		EndpointResolverV2: awss3.NewDefaultEndpointResolverV2(),
	})

	bucket := "sw-it-" + bucketSafeSuffix()
	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	art := s3store.NewArtifactStore(bucket, "cache", client)
	logs := s3store.NewLogStore(bucket, "logs", client)
	return art, logs
}

var (
	uniqMu sync.Mutex
	uniqN  int
)

func uniqSuffix() string {
	uniqMu.Lock()
	defer uniqMu.Unlock()
	uniqN++
	return fmt.Sprintf("%d_%d", time.Now().UnixNano()&0xffffff, uniqN)
}

// bucketSafeSuffix returns an S3-bucket-safe identifier (lowercase
// alphanumerics + hyphens only; no underscores).
func bucketSafeSuffix() string {
	uniqMu.Lock()
	defer uniqMu.Unlock()
	uniqN++
	return fmt.Sprintf("%x-%d", time.Now().UnixNano()&0xffffff, uniqN)
}

func sanitizeName(s string) string {
	r := strings.NewReplacer("/", "_", " ", "_", "-", "_", ".", "_", "#", "_", "(", "_", ")", "_", "[", "_", "]", "_")
	out := r.Replace(s)
	if len(out) > 40 {
		out = out[:40]
	}
	return strings.ToLower(out)
}

func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%ssearch_path=%s", dsn, sep, schema)
}
