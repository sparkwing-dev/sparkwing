package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Integration struct{ sparkwing.Base }

func (Integration) ShortHelp() string {
	return "Run the Postgres/S3 integration suite against Dockerized backends"
}

func (Integration) Help() string {
	return "Spins up Postgres + MinIO in Docker, waits for readiness via a Verify gate, " +
		"runs the env-gated integration tests (SPARKWING_TEST_PG_URL + SPARKWING_S3_* ) with " +
		"SPARKWING_REQUIRE_PG=1 so a Postgres suite that skips fails the run instead, " +
		"and tears the containers down whether the run passes or fails. " +
		"Requires Docker, go, curl (the MinIO readiness probe), and the aws CLI (creates the test bucket)."
}

const (
	itPGName    = "sw-it-pg"
	itMinioName = "sw-it-minio"
	itPGPort    = "5433"
	itMinioPort = "9100"
	itBucket    = "sw-it"
	itPGURL     = "postgres://postgres:postgres@localhost:" + itPGPort + "/postgres?sslmode=disable"
	itS3Endpt   = "http://localhost:" + itMinioPort
)

func (Integration) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	fixtures := sparkwing.Job(plan, "fixtures", startFixtures).
		Verify(fixturesReady).
		OnFailure("teardown-on-fixture-fail", func(ctx context.Context, _ sparkwing.Failure) error {
			return teardownFixtures(ctx)
		})

	sparkwing.Job(plan, "test", runIntegrationSuite).
		Needs(fixtures).
		Timeout(20 * time.Minute).
		AfterRun(func(ctx context.Context, _ error) { _ = teardownFixtures(ctx) })

	return nil
}

func startFixtures(ctx context.Context) error {
	_ = run(ctx, "", "docker", "rm", "-f", itPGName, itMinioName)
	sparkwing.Info(ctx, "starting postgres (%s) on :%s", itPGName, itPGPort)
	if err := run(ctx, "", "docker", "run", "-d", "--name", itPGName,
		"-e", "POSTGRES_PASSWORD=postgres", "-e", "POSTGRES_USER=postgres",
		"-p", itPGPort+":5432", "postgres:17"); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	sparkwing.Info(ctx, "starting minio (%s) on :%s", itMinioName, itMinioPort)
	if err := run(ctx, "", "docker", "run", "-d", "--name", itMinioName,
		"-e", "MINIO_ROOT_USER=minioadmin", "-e", "MINIO_ROOT_PASSWORD=minioadmin",
		"-p", itMinioPort+":9000", "minio/minio", "server", "/data"); err != nil {
		return fmt.Errorf("start minio: %w", err)
	}
	return nil
}

func fixturesReady(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		pgOK := run(ctx, "", "docker", "exec", itPGName, "pg_isready", "-U", "postgres") == nil
		minioOK := run(ctx, "", "curl", "-sf", itS3Endpt+"/minio/health/ready") == nil
		if pgOK && minioOK {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fixtures not ready within 90s (pg=%v minio=%v)", pgOK, minioOK)
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	mb := exec.CommandContext(ctx, "aws", "--endpoint-url", itS3Endpt, "--region", "us-east-1",
		"s3", "mb", "s3://"+itBucket)
	mb.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID=minioadmin", "AWS_SECRET_ACCESS_KEY=minioadmin")
	if out, err := mb.CombinedOutput(); err != nil && !strings.Contains(string(out), "BucketAlreadyOwnedByYou") {
		sparkwing.Info(ctx, "mb s3://%s: %s", itBucket, strings.TrimSpace(string(out)))
	}
	sparkwing.Annotate(ctx, "postgres + minio ready; bucket "+itBucket+" present")
	return nil
}

func runIntegrationSuite(ctx context.Context) error {
	root, err := mainModuleRoot()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_PG_URL="+itPGURL,
		"SPARKWING_REQUIRE_PG=1",
		"SPARKWING_S3_TEST_BUCKET="+itBucket,
		"SPARKWING_S3_ENDPOINT="+itS3Endpt,
		"AWS_ACCESS_KEY_ID=minioadmin",
		"AWS_SECRET_ACCESS_KEY=minioadmin",
		"AWS_REGION=us-east-1",
	)
	sparkwing.Info(ctx, "go test ./... with integration backends (root=%s)", root)
	out, err := cmd.CombinedOutput()
	sparkwing.Info(ctx, "%s", strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("integration tests failed: %w", err)
	}
	sparkwing.Annotate(ctx, "integration suite passed against postgres + minio")
	return nil
}

func teardownFixtures(ctx context.Context) error {
	sparkwing.Info(ctx, "tearing down fixtures")
	_ = run(ctx, "", "docker", "rm", "-f", itPGName, itMinioName)
	return nil
}

func run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Run()
}

func mainModuleRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		gomod := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(gomod); err == nil &&
			strings.Contains(string(b), "module github.com/sparkwing-dev/sparkwing\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate the sparkwing module root walking up from %s", wd)
		}
		dir = parent
	}
}

func init() {
	sparkwing.Register("integration", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Integration{} })
}
