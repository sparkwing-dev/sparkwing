package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: this runs as a launcher Job's init container, before any pipeline
// code; the GitHub token it is issued lives only in this process and the git
// and go processes it starts, all gone before the pipeline's container starts.
func runFetchSourceCLI(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: sparkwing-runner fetch-source <run> <node>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctrl := client.NewWithToken(os.Getenv("SPARKWING_CONTROLLER_URL"), nil, os.Getenv("SPARKWING_AGENT_TOKEN"))
	// safety: the pod's first beat opens its node's billing, so the fetch is
	// billed; the lease outlasts the fetch until the node's container beats.
	if _, err := ctrl.HeartbeatClaim(ctx, args[0], args[1], store.MaxLeaseDuration); err != nil {
		return fmt.Errorf("fetch-source: renew the claim: %w", err)
	}
	err := fetchSource(ctx, ctrl, args[0], os.Getenv("SPARKWING_SOURCE_DIR"), os.Getenv("GOMODCACHE"),
		bincache.CheckoutSource, goModDownload)
	if err != nil {
		return reportFetchFailure(ctx, ctrl, args[0], args[1], err)
	}
	return nil
}

type sourceCredentials interface {
	SourceCredential(ctx context.Context, runID string) (*store.SourceCredential, error)
}

type attemptReporter interface {
	ReportAttempt(ctx context.Context, runID, nodeID string, report store.AttemptReport) error
}

// safety: a refusal answers the same on every attempt, so it marks the fetch
// failure no retry can get past.
var errSourceRefused = errors.New("the controller refused the source credential")

// safety: the pipeline's container never starts once this one fails, so the
// attempt is reported here, or its claim holds the node until its lease lapses.
func reportFetchFailure(ctx context.Context, ctrl attemptReporter, runID, nodeID string, cause error) error {
	report := store.AttemptReport{Outcome: string(sparkwing.Failed), Error: cause.Error(), FailureReason: store.FailureSourceFetch}
	if errors.Is(cause, errSourceRefused) {
		report.FailureReason = store.FailureSourceUnavailable
	}
	if err := ctrl.ReportAttempt(context.WithoutCancel(ctx), runID, nodeID, report); err != nil {
		return errors.Join(cause, fmt.Errorf("fetch-source: report the attempt: %w", err))
	}
	return cause
}

type checkoutFunc func(ctx context.Context, repoURL, sha, branch, dest string, cred bincache.DirectCredential,
	o bincache.SourceOptions, submoduleRepos []string) error

func fetchSource(ctx context.Context, ctrl sourceCredentials, runID, dest, modCache string,
	checkout checkoutFunc, download func(ctx context.Context, dir string, env []string) error,
) error {
	if dest == "" || modCache == "" {
		return errors.New("fetch-source: SPARKWING_SOURCE_DIR and GOMODCACHE are required")
	}
	sc, err := askSourceCredential(ctx, ctrl, runID)
	if err != nil {
		return fmt.Errorf("fetch-source: %w", err)
	}
	if len(sc.Repositories) == 0 {
		return errors.New("fetch-source: the controller named no repository")
	}
	cred := bincache.DirectCredential{
		Kind: bincache.CredentialGitHubApp, Host: "github.com",
		Username: "x-access-token", Secret: sc.Token,
	}
	listed := sc.Repositories[1:]
	var ids []string
	for _, slug := range listed {
		id, err := sourceurl.GitHubIdentity(slug)
		if err != nil {
			return fmt.Errorf("fetch-source: %w", err)
		}
		ids = append(ids, id)
	}
	o := bincache.SourceOptions{
		Depth: sc.Source.Depth, Tags: sc.Source.Tags,
		Submodules: sc.Source.Submodules, LFS: sc.Source.LFS,
	}
	if err := checkout(ctx, sc.RepoURL, sc.SHA, sc.Branch, dest, cred, o, ids); err != nil {
		return err
	}
	sparkwingDir := filepath.Join(dest, ".sparkwing")
	info, err := os.Lstat(sparkwingDir)
	if err != nil {
		return fmt.Errorf("fetch-source: inspect .sparkwing: %w", err)
	}
	if !info.IsDir() {
		return errors.New("fetch-source: .sparkwing must be a real directory")
	}
	for _, dir := range []string{dest, sparkwingDir} {
		path := filepath.Join(dir, "go.mod")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("fetch-source: inspect %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fetch-source: %s must not be a symlink", path)
		}
	}
	// safety: a private module can only live in a listed repository, so a run
	// with none downloads nothing here and its public modules come later.
	if len(ids) > 0 {
		env := privateModuleEnv(os.Environ(), ids, sc.Token)
		for _, dir := range []string{dest, sparkwingDir} {
			// #nosec G703 -- dir is under the SPARKWING_SOURCE_DIR the launcher Job set
			if _, err := os.Lstat(filepath.Join(dir, "go.mod")); err != nil {
				continue
			}
			if err := download(ctx, dir, env); err != nil {
				return fmt.Errorf("fetch-source: download modules in %s: %w", dir, err)
			}
		}
		// safety: the pipeline's build finds these modules in the cache and
		// must not ask a public proxy or checksum database about them.
		goEnv := "GOPRIVATE=" + strings.Join(ids, ",") + "\n"
		// #nosec G703 -- beside the SPARKWING_SOURCE_DIR the launcher Job set
		if err := os.WriteFile(filepath.Join(filepath.Dir(dest), orchestrator.GoEnvFile), []byte(goEnv), 0o644); err != nil {
			return fmt.Errorf("fetch-source: %w", err)
		}
	}
	// safety: everything under the scratch volume's root was written by this
	// container, and the pipeline's container reads all of it.
	return tokenAbsent(sc.Token, filepath.Dir(dest))
}

// perf: short, so a flaky mint costs the pod seconds; the third ask waits twice it.
var sourceMintBackoff = time.Second

// safety: a claim may be issued store.MaxSourceMints credentials so a mint that
// failed can be asked for again; only a network failure or a 5xx is retried,
// since a 4xx refusal answers the same way every time.
func askSourceCredential(ctx context.Context, ctrl sourceCredentials, runID string) (*store.SourceCredential, error) {
	for attempt := 1; ; attempt++ {
		sc, err := ctrl.SourceCredential(ctx, runID)
		var transport *url.Error
		transient := errors.Is(err, client.ErrControllerFailed) || errors.As(err, &transport)
		if err != nil && !transient && ctx.Err() == nil {
			return nil, fmt.Errorf("%w: %w", errSourceRefused, err)
		}
		if err == nil || attempt == store.MaxSourceMints {
			return sc, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sourceMintBackoff * time.Duration(attempt)):
		}
	}
}

// safety: go fetches only the listed repositories directly, with a helper that
// answers github.com alone from this process's environment, and reads no git
// config or toolchain the checkout could name.
func privateModuleEnv(base, ids []string, token string) []string {
	env := make([]string, 0, len(base)+10)
	for _, kv := range base {
		if name, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(name, "GIT_") && !strings.HasPrefix(name, "GOPRIVATE") &&
			name != "GOFLAGS" && name != "GONOSUMDB" && name != "GONOPROXY" && name != "GOTOOLCHAIN" && name != "GOWORK" {
			env = append(env, kv)
		}
	}
	const helper = `!f() { test "$1" = get && printf 'username=x-access-token\npassword=%s\n' "$SPARKWING_SOURCE_TOKEN"; }; f`
	return append(env, "GOPRIVATE="+strings.Join(ids, ","), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOWORK=off",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ALLOW_PROTOCOL=https",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.https://github.com.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1="+helper,
		"SPARKWING_SOURCE_TOKEN="+token)
}

func goModDownload(ctx context.Context, dir string, env []string) error {
	cmd := exec.CommandContext(ctx, "go", "mod", "download")
	cmd.Dir, cmd.Env = dir, env
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = os.Stderr, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// safety: the token's bytes in any file left on the shared volume, LFS logs and module cache included, fail
// the pod before the pipeline's container starts. Files are streamed, so memory stays bounded, and the
// volume's size limit bounds the time.
func tokenAbsent(token, root string) error {
	needle := []byte(token)
	buf := make([]byte, 1<<20)
	// #nosec G703 -- root is the scratch volume this container wrote
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		carry := 0
		for {
			n, rerr := f.Read(buf[carry:])
			if bytes.Contains(buf[:carry+n], needle) {
				return fmt.Errorf("fetch-source: %s holds the source credential", path)
			}
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					return nil
				}
				return rerr
			}
			keep := min(len(needle)-1, carry+n)
			copy(buf, buf[carry+n-keep:carry+n])
			carry = keep
		}
	})
}
