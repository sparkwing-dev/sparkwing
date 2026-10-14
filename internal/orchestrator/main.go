package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func Main() {
	if len(os.Args) == 2 && os.Args[1] == protofd.Arg {
		if _, err := bindProjectPipelines(); err != nil {
			fmt.Fprintln(os.Stderr, "sw-node-protocol:", err)
			os.Exit(1)
		}
		os.Exit(sdkserve.Main(sdkserve.Config{SDKVersion: sparkwingModuleVersion(), PlanDoc: planDocument}))
	}
	fmt.Fprintf(os.Stderr, "this pipeline binary answers only %s; run it with `sparkwing run <pipeline>`\n", protofd.Arg)
	os.Exit(2)
}

func bindProjectPipelines() (*projectconfig.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("find the project config: %w", err)
	}
	_, cfg, err := projectconfig.Discover(cwd)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	sparkwing.BindPipelinesFromYAML(&pipelines.Config{Pipelines: cfg.Pipelines})
	return cfg, nil
}

func localRenderer(format string) sparkwing.Logger {
	switch strings.ToLower(format) {
	case "json":
		return NewJSONRenderer()
	case "pretty":
		return NewPrettyRenderer()
	case "quiet":
		return NewQuietRenderer()
	}
	if isInteractiveStdout() {
		return NewPrettyRenderer()
	}
	return NewJSONRenderer()
}

func isInteractiveStdout() bool {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	if os.Getenv("MSYSTEM") != "" {
		return true
	}
	if os.Getenv("TERM_PROGRAM") == "mintty" {
		return true
	}
	switch t := os.Getenv("TERM"); {
	case t == "":
		return false
	case strings.Contains(t, "xterm"), strings.Contains(t, "cygwin"):
		return true
	}
	return false
}

// DebugDirectivesFromEnv reads the pauses `sparkwing debug run` asks for.
func DebugDirectivesFromEnv() DebugDirectives { return readDebugDirectivesFromEnv() }

func readDebugDirectivesFromEnv() DebugDirectives {
	d := DebugDirectives{}
	if v := os.Getenv("SPARKWING_DEBUG_PAUSE_BEFORE"); v != "" {
		d.PauseBefore = splitCommaClean(v)
	}
	if v := os.Getenv("SPARKWING_DEBUG_PAUSE_AFTER"); v != "" {
		d.PauseAfter = splitCommaClean(v)
	}
	if v := os.Getenv("SPARKWING_DEBUG_PAUSE_ON_FAILURE"); v == "1" || v == "true" {
		d.PauseOnFailure = true
	}
	return d
}

func splitCommaClean(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// DetectGit reads the checkout at dir: HEAD, branch, origin's default branch
// and its GitHub repository.
func DetectGit(dir string) *sparkwing.Git {
	g := &sparkwing.Git{}
	if dir != "" {
		g = sparkwing.NewGit(dir, "", "", "", "", "")
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		return cmd.Output()
	}
	if out, err := git("rev-parse", "HEAD"); err == nil {
		g.SHA = strings.TrimSpace(string(out))
	}
	if out, err := git("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch := strings.TrimSpace(string(out))
		if branch != "HEAD" {
			g.Branch = branch
		}
	}
	if out, err := git("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		name := strings.TrimSpace(string(out))
		g.DefaultBranch = strings.TrimPrefix(name, "origin/")
	}
	if out, err := git("remote", "get-url", "origin"); err == nil {
		g.RepoURL = strings.TrimSpace(string(out))
		owner, repo := parseGithubURL(g.RepoURL)
		if owner != "" && repo != "" {
			g.Repo = owner + "/" + repo
		}
	}
	return g
}

func parseGithubURL(url string) (owner, repo string) {
	url = strings.TrimSuffix(url, ".git")
	var path string
	switch {
	case strings.HasPrefix(url, "git@github.com:"):
		path = strings.TrimPrefix(url, "git@github.com:")
	case strings.HasPrefix(url, "https://github.com/"):
		path = strings.TrimPrefix(url, "https://github.com/")
	case strings.HasPrefix(url, "ssh://git@github.com/"):
		path = strings.TrimPrefix(url, "ssh://git@github.com/")
	default:
		return "", ""
	}
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
