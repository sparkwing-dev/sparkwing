package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	flag "github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/runnersvc"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the scope set docs/auth.md gives a runner; a wider one hands a workstation reads no claim loop needs.
var runnerTokenScopes = []string{"nodes.claim", "triggers.claim", "runs.state", "secrets.read", "logs.write"}

// safety: the agent loop lives in this binary, not in `sparkwing`, so the service runs a second executable.
const runnerBinaryName = "sparkwing-runner"

// safety: tests replace these with a fake machine.
var (
	runnerServiceHost = defaultRunnerServiceHost
	runnerServiceGOOS = runtime.GOOS
)

func runRunners(args []string) error {
	if handleParentHelp(cmdRunners, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdRunners, os.Stderr)
		return errors.New("runners: subcommand required (add|remove)")
	}
	switch args[0] {
	case "add":
		return runRunnersAdd(args[1:])
	case "remove":
		return runRunnersRemove(args[1:])
	default:
		PrintHelp(cmdRunners, os.Stderr)
		return fmt.Errorf("runners: unknown subcommand %q", args[0])
	}
}

func runRunnersAdd(args []string) error {
	fs := flag.NewFlagSet(cmdRunnersAdd.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	name := fs.String("name", "", "runner name, shown in the dashboard")
	maxConcurrent := fs.Int("max-concurrent", 2, "concurrent jobs this machine accepts")
	contribution := fs.String("contribution", "50%,50%", "CPU and memory this machine contributes")
	labels := fs.String("labels", "", "comma-separated self-asserted placement labels")
	logsURL := fs.String("logs", "", "logs service URL (default: the profile's logs surface)")
	configPath := fs.String("config", "", "agent config to write (default: ~/.config/sparkwing/agent.yaml)")
	force := fs.Bool("force", false, "replace an existing agent config")
	noService := fs.Bool("no-service", false, "write the config without installing or starting the service")
	if err := parseAndCheck(cmdRunnersAdd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("runners add: --name is required")
	}
	if *maxConcurrent < 1 {
		return fmt.Errorf("runners add: --max-concurrent must be at least 1, got %d", *maxConcurrent)
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "runners add"); err != nil {
		return err
	}
	path, err := agentConfigPath(*configPath)
	if err != nil {
		return err
	}
	// safety: the token is minted only once the destination is known writable,
	// so a refused write never leaves a live credential nobody holds.
	if err := checkAgentConfigAbsent(path, *force); err != nil {
		return err
	}

	principal := "agent:" + *name
	minted, err := mintRunnerToken(prof.ControllerURL(), prof.ControllerToken(), principal)
	if err != nil {
		return err
	}
	cfg := agentFileConfig{
		Controller:     strings.TrimRight(prof.ControllerURL(), "/"),
		Logs:           firstNonBlank(*logsURL, profileLogsURL(prof)),
		Token:          minted.Raw,
		MaxConcurrent:  *maxConcurrent,
		HolderPrefix:   *name,
		Labels:         splitCSV(*labels),
		Contribution:   *contribution,
		LocalAdmission: true,
	}
	if err := writeAgentConfig(path, cfg); err != nil {
		return err
	}

	fmt.Printf("minted runner token %s for %s on profile %s\n", minted.Prefix, principal, prof.Name)
	fmt.Printf("wrote %s (mode 0600)\n", path)
	if err := startRunnerService(path, *noService); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("revoke this runner with:")
	fmt.Printf("  sparkwing cluster runners remove --profile %s\n", prof.Name)
	fmt.Println("or revoke the token alone with:")
	fmt.Printf("  sparkwing cluster tokens revoke --profile %s --prefix %s\n", prof.Name, minted.Prefix)
	return nil
}

func runRunnersRemove(args []string) error {
	fs := flag.NewFlagSet(cmdRunnersRemove.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	configPath := fs.String("config", "", "agent config to read the token from (default: ~/.config/sparkwing/agent.yaml)")
	noService := fs.Bool("no-service", false, "revoke the token without touching the service")
	if err := parseAndCheck(cmdRunnersRemove, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "runners remove"); err != nil {
		return err
	}
	path, err := agentConfigPath(*configPath)
	if err != nil {
		return err
	}
	raw, err := cluster.LoadAgentConfig(path)
	if err != nil {
		return err
	}
	prefix, err := tokenPrefix(raw.Token)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	// safety: stop the runner before the credential dies so an in-flight claim
	// finishes against a token that still authenticates.
	if err := stopRunnerService(path, *noService); err != nil {
		return err
	}
	if _, err := tokensDelete(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/tokens/"+prefix); err != nil {
		return err
	}
	fmt.Printf("revoked %s on profile %s\n", prefix, prof.Name)
	fmt.Printf("%s still holds that dead token; `sparkwing cluster runners add --force` replaces it\n", path)
	return nil
}

type mintedToken struct {
	Raw    string
	Prefix string
}

func mintRunnerToken(controller, adminToken, principal string) (mintedToken, error) {
	resp, err := tokensPost(controller, adminToken, "/api/v1/tokens", map[string]any{
		"kind":      "runner",
		"principal": principal,
		"scopes":    runnerTokenScopes,
	})
	if err != nil {
		return mintedToken{}, err
	}
	var out struct {
		Token    string `json:"token"`
		Metadata struct {
			Prefix string `json:"prefix"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return mintedToken{}, fmt.Errorf("decode minted token: %w", err)
	}
	if out.Token == "" {
		return mintedToken{}, errors.New("the controller returned no token")
	}
	minted := mintedToken{Raw: out.Token, Prefix: out.Metadata.Prefix}
	if minted.Prefix == "" {
		if minted.Prefix, err = tokenPrefix(out.Token); err != nil {
			return mintedToken{}, err
		}
	}
	return minted, nil
}

func tokenPrefix(raw string) (string, error) {
	if len(raw) < store.PrefixLen {
		return "", errors.New("token is too short to carry a prefix")
	}
	return raw[:store.PrefixLen], nil
}

// safety: the claim-mode subset install/service-install.sh writes. A name or a coordinator here would select
// enrolled mode, which the agent refuses to start.
type agentFileConfig struct {
	Controller     string   `yaml:"controller"`
	Logs           string   `yaml:"logs,omitempty"`
	Token          string   `yaml:"token"`
	MaxConcurrent  int      `yaml:"max_concurrent"`
	HolderPrefix   string   `yaml:"holder_prefix"`
	Labels         []string `yaml:"labels,omitempty"`
	Contribution   string   `yaml:"contribution,omitempty"`
	LocalAdmission bool     `yaml:"local_admission"`
}

func agentConfigPath(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	return cluster.DefaultAgentConfigPath()
}

func checkAgentConfigAbsent(path string, force bool) error {
	_, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case force:
		return nil
	}
	return fmt.Errorf("%s already exists; pass --force to replace it (its token stays live until you revoke it)", path)
}

func writeAgentConfig(path string, cfg agentFileConfig) error {
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := fssecure.EnsureConfigDir(filepath.Dir(path)); err != nil {
		return err
	}
	return fssecure.WriteFile(path, body)
}

func profileLogsURL(p *profile.Profile) string {
	if p == nil || p.Logs == nil {
		return ""
	}
	return p.Logs.URL
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func startRunnerService(configPath string, skip bool) error {
	if skip {
		fmt.Printf("skipped the service install; start the runner with `%s agent --config %s`\n", runnerBinaryName, configPath)
		return nil
	}
	if runnerServiceGOOS == "windows" {
		printWindowsServiceSteps(configPath)
		return nil
	}
	host, err := runnerServiceHost(configPath)
	if err != nil {
		return err
	}
	state, err := runnersvc.Install(host)
	if err != nil {
		if errors.Is(err, runnersvc.ErrUnsupported) {
			printWindowsServiceSteps(configPath)
			return nil
		}
		return err
	}
	fmt.Printf("started the runner service: %s\n", state.Detail)
	fmt.Printf("service file: %s\n", state.Path)
	return nil
}

func stopRunnerService(configPath string, skip bool) error {
	if skip || runnerServiceGOOS == "windows" {
		return nil
	}
	host, err := runnerServiceHost(configPath)
	if err != nil {
		return err
	}
	state, err := runnersvc.Uninstall(host)
	if err != nil {
		if errors.Is(err, runnersvc.ErrUnsupported) {
			return nil
		}
		return err
	}
	fmt.Println(state.Detail)
	return nil
}

func printWindowsServiceSteps(configPath string) {
	fmt.Println()
	fmt.Println("this installer creates no Windows service. Supervise the runner yourself:")
	fmt.Printf("  1. put %s.exe on PATH\n", runnerBinaryName)
	fmt.Printf("  2. run `%s.exe agent --config %s`\n", runnerBinaryName, configPath)
	fmt.Println("  3. register that command with your service manager so it restarts at logon")
}

func defaultRunnerServiceHost(configPath string) (runnersvc.Host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return runnersvc.Host{}, fmt.Errorf("resolve the home directory the service is installed under: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	binary, err := runnerBinaryPath()
	if err != nil {
		return runnersvc.Host{}, err
	}
	root, err := paths.DefaultPaths()
	if err != nil {
		return runnersvc.Host{}, err
	}
	return runnersvc.Host{
		GOOS:       runnerServiceGOOS,
		Home:       home,
		ConfigHome: configHome,
		Binary:     binary,
		ConfigPath: configPath,
		LogPath:    filepath.Join(root.Root, "runner.log"),
		UID:        os.Getuid(),
		Exec:       runnersvc.DefaultExec,
	}, nil
}

// safety: a systemd or launchd job inherits no PATH, so the absolute path is baked in.
func runnerBinaryPath() (string, error) {
	if found, err := exec.LookPath(runnerBinaryName); err == nil {
		if abs, err := filepath.Abs(found); err == nil {
			return abs, nil
		}
	}
	if self, err := installsite.Self(); err == nil && self != "" {
		beside := filepath.Join(filepath.Dir(self), runnerBinaryName)
		if info, err := os.Stat(beside); err == nil && !info.IsDir() {
			return beside, nil
		}
	}
	return "", fmt.Errorf("%s is not on PATH. Install it with `go install github.com/sparkwing-dev/sparkwing/cmd/%s@latest`, "+
		"or download it from the release assets, then run this command again", runnerBinaryName, runnerBinaryName)
}
