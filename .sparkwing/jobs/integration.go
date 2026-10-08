package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Integration struct{ sparkwing.Base }

func (Integration) ShortHelp() string {
	return "Run the Postgres/S3 integration suite against Dockerized backends"
}

func (Integration) Help() string {
	return "Builds a local MinIO fixture image from a pinned upstream source commit, then spins up Postgres + MinIO in Docker, waits for readiness via a Verify gate, " +
		"runs the env-gated integration tests (SPARKWING_TEST_PG_URL + SPARKWING_S3_* ) with " +
		"SPARKWING_REQUIRE_PG=1, re-runs the Postgres-gated tests verbosely and fails on any " +
		"reported skip, " +
		"and tears the containers down whether the run passes or fails. " +
		"Requires Docker, go, curl (the MinIO readiness probe), and the aws CLI (creates the test bucket)."
}

const (
	itMinioCommit = "07c3a429bfed433e49018cb0f78a52145d4bedeb"
	itMinioImage  = "sparkwing-integration-minio:" + itMinioCommit
	itPGName      = "sw-it-pg"
	itMinioName   = "sw-it-minio"
	itPGPort      = "5433"
	itMinioPort   = "9100"
	itBucket      = "sw-it"
	itPGURL       = "postgres://postgres:postgres@localhost:" + itPGPort + "/postgres?sslmode=disable"
	itS3Endpt     = "http://localhost:" + itMinioPort
)

func (Integration) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	fixtures := sparkwing.Job(plan, "fixtures", startFixtures).
		Verify(fixturesReady).
		OnFailure("teardown-on-fixture-fail", func(ctx context.Context, _ sparkwing.Failure) error {
			return teardownFixtures(ctx)
		})

	sparkwing.Job(plan, "test", runIntegrationSuite).
		Needs(fixtures).
		Timeout(60 * time.Minute).
		AfterRun(func(ctx context.Context, _ error) { _ = teardownFixtures(ctx) })

	return nil
}

func startFixtures(ctx context.Context) error {
	if err := buildMinioFixture(ctx); err != nil {
		return err
	}
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
		"-p", itMinioPort+":9000", itMinioImage, "server", "/data"); err != nil {
		return fmt.Errorf("start minio: %w", err)
	}
	return nil
}

func buildMinioFixture(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "sw-it-minio-build-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			sparkwing.Warn(ctx, "remove MinIO build scratch: %v", err)
		}
	}()
	moduleCache, err := exec.CommandContext(ctx, "go", "env", "GOMODCACHE").Output()
	if err != nil {
		return fmt.Errorf("locate Go module cache: %w", err)
	}
	if err := run(ctx, "", "env", "GOENV=off", "GOBIN=", "GOPATH="+dir, "GOMODCACHE="+strings.TrimSpace(string(moduleCache)), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "go", "install", "github.com/minio/minio@"+itMinioCommit); err != nil {
		return fmt.Errorf("build pinned MinIO: %w", err)
	}
	binary := "bin/minio"
	if runtime.GOOS != "linux" {
		binary = "bin/linux_" + runtime.GOARCH + "/minio"
	}
	dockerfile := "FROM scratch\nCOPY " + binary + " /minio\nENTRYPOINT [\"/minio\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		return err
	}
	return run(ctx, "", "docker", "build", "--platform", "linux/"+runtime.GOARCH, "--label", "org.opencontainers.image.source=https://github.com/minio/minio", "--label", "org.opencontainers.image.revision="+itMinioCommit, "-t", itMinioImage, dir)
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

const pgGatedRun = "Postgres|Pg"

var pgGatedPackages = []string{"./pkg/store", "./internal/backend", "./internal/orchestrator"}

func runIntegrationSuite(ctx context.Context) error {
	root, err := mainModuleRoot()
	if err != nil {
		return err
	}
	env := append(os.Environ(),
		"SPARKWING_TEST_PG_URL="+itPGURL,
		"SPARKWING_REQUIRE_PG=1",
		"SPARKWING_S3_TEST_BUCKET="+itBucket,
		"AWS_ENDPOINT_URL_S3="+itS3Endpt,
		"AWS_ACCESS_KEY_ID=minioadmin",
		"AWS_SECRET_ACCESS_KEY=minioadmin",
		"AWS_REGION=us-east-1",
	)
	sparkwing.Info(ctx, "go test ./... with integration backends (root=%s)", root)
	if _, err := goTest(ctx, root, env, []string{"./..."}); err != nil {
		return fmt.Errorf("integration tests failed: %w", err)
	}
	sparkwing.Info(ctx, "re-running the Postgres-gated tests verbosely")
	args := append([]string{"-v", "-count=1", "-run", pgGatedRun}, pgGatedPackages...)
	out, err := goTest(ctx, root, env, args)
	if err != nil {
		return fmt.Errorf("postgres-gated tests failed: %w", err)
	}
	if skipped := skippedTests(out); len(skipped) > 0 {
		return fmt.Errorf("postgres-gated tests skipped against a live postgres:\n%s",
			strings.Join(skipped, "\n"))
	}
	sparkwing.Annotate(ctx, "integration suite passed against postgres + minio")
	return nil
}

func goTest(ctx context.Context, root string, env, args []string) (string, error) {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	var output string
	err := withProductTestHome(func(home string) error {
		command := boundedGoCommand(currentHost(), "test", "-timeout "+productGoTestTimeout+" "+strings.Join(quoted, " "))
		script := productTestScript(command, home)
		cmd := exec.CommandContext(ctx, "bash", "-c", script)
		cmd.Dir = root
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		output = string(out)
		return err
	})
	sparkwing.Info(ctx, "%s", strings.TrimSpace(output))
	return output, err
}

func skippedTests(out string) []string {
	var skipped []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "--- SKIP") {
			skipped = append(skipped, strings.TrimSpace(line))
		}
	}
	return skipped
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
