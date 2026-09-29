package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	return fetchSource(ctx, ctrl, args[0], os.Getenv("SPARKWING_SOURCE_DIR"), os.Getenv("GOMODCACHE"),
		bincache.CheckoutSource, goModDownload)
}

type sourceCredentials interface {
	SourceCredential(ctx context.Context, runID string) (*store.SourceCredential, error)
}

type checkoutFunc func(ctx context.Context, repoURL, sha, branch, dest string, cred bincache.DirectCredential,
	o bincache.SourceOptions, submoduleRepos []string) error

func fetchSource(ctx context.Context, ctrl sourceCredentials, runID, dest, modCache string,
	checkout checkoutFunc, download func(ctx context.Context, dir string, env []string) error,
) error {
	if dest == "" || modCache == "" {
		return errors.New("fetch-source: SPARKWING_SOURCE_DIR and GOMODCACHE are required")
	}
	sc, err := ctrl.SourceCredential(ctx, runID)
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
	// safety: a private module can only live in a listed repository, so a run
	// with none downloads nothing here and its public modules come later.
	if len(ids) > 0 {
		env := privateModuleEnv(os.Environ(), ids, sc.Token)
		for _, dir := range []string{dest, filepath.Join(dest, ".sparkwing")} {
			if _, err := os.Lstat(filepath.Join(dir, "go.mod")); err != nil {
				continue
			}
			if err := download(ctx, dir, env); err != nil {
				return fmt.Errorf("fetch-source: download modules in %s: %w", dir, err)
			}
		}
	}
	return credentialScrubbed(sc.Token, filepath.Join(dest, ".git"), filepath.Join(modCache, "cache", "vcs"))
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

// safety: nothing here writes the token or a helper, so finding either in the
// git config the pipeline's container inherits means something else did, and
// the pod fails before that container starts.
func credentialScrubbed(token string, roots ...string) error {
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil || d.IsDir() || d.Name() != "config" {
				return err
			}
			body, err := os.ReadFile(path)
			if err == nil && (bytes.Contains(body, []byte(token)) || bytes.Contains(body, []byte("[credential"))) {
				return fmt.Errorf("fetch-source: %s holds a git credential", path)
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
