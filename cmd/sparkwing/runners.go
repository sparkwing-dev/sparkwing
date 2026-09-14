package main

import (
	"bytes"
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

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/agentservice"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the scope set docs/auth.md gives a runner; a wider one hands a workstation reads no claim loop needs.
var runnerTokenScopes = []string{"nodes.claim", "triggers.claim", "runs.state", "secrets.read", "logs.write"}

// safety: the agent loop lives in this binary, not in `sparkwing`, so the service runs a second executable.
const runnerBinaryName = "sparkwing-runner"

// safety: the machine is reached only through these two, so a suite can substitute a host that touches nothing.
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
	// safety: this refusal runs before the mint, so the common rerun strands no live credential.
	if err := checkAgentConfigAbsent(path, *force); err != nil {
		return err
	}
	cfg := agentFileConfig{
		Controller:     strings.TrimRight(prof.ControllerURL(), "/"),
		Logs:           firstNonBlank(*logsURL, profileLogsURL(prof)),
		MaxConcurrent:  *maxConcurrent,
		HolderPrefix:   *name,
		Labels:         splitCSV(*labels),
		Contribution:   *contribution,
		LocalAdmission: true,
	}
	if err := validateAgentFileConfig(cfg); err != nil {
		return err
	}
	service, err := planRunnerService(path, *noService)
	if err != nil {
		return err
	}

	principal := "agent:" + *name
	minted, err := mintRunnerToken(prof.ControllerURL(), prof.ControllerToken(), principal)
	if err != nil {
		return err
	}
	fmt.Printf("minted runner token %s for %s on profile %s\n", minted.Prefix, principal, prof.Name)
	cfg.Token = minted.Raw

	if err := finishRunnerEnrollment(path, cfg, service); err != nil {
		printRunnerRevokeHint(prof.Name, minted.Prefix)
		return err
	}
	fmt.Println()
	fmt.Println("revoke this runner with:")
	fmt.Printf("  sparkwing cluster runners remove --profile %s\n", prof.Name)
	fmt.Println("or revoke the token alone with:")
	fmt.Printf("  sparkwing cluster tokens revoke --profile %s --prefix %s\n", prof.Name, minted.Prefix)
	return nil
}

func finishRunnerEnrollment(path string, cfg agentFileConfig, service runnerServicePlan) error {
	if err := writeAgentConfig(path, cfg); err != nil {
		return err
	}
	fmt.Printf("wrote %s (mode 0600)\n", path)
	return service.start(path)
}

// safety: the credential outlives every failure after the mint, so the operator is always told how to kill it.
func printRunnerRevokeHint(profileName, prefix string) {
	fmt.Println()
	fmt.Printf("the token %s is live; revoke it with:\n", prefix)
	fmt.Printf("  sparkwing cluster tokens revoke --profile %s --prefix %s\n", profileName, prefix)
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
	raw, err := agentconfig.Load(path)
	if err != nil {
		return err
	}
	prefix, err := tokenPrefix(raw.Token)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// safety: the config names a prefix, not a promise about it; revoking the wrong kind
	// would cut an operator or a service off its controller.
	if err := requireRunnerToken(prof.ControllerURL(), prof.ControllerToken(), prefix, path); err != nil {
		return err
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

func requireRunnerToken(controller, adminToken, prefix, configPath string) error {
	resp, err := tokensGet(controller, adminToken, "/api/v1/tokens/"+prefix)
	if err != nil {
		return fmt.Errorf("look up the token %s named by %s: %w", prefix, configPath, err)
	}
	var found tokenListItem
	if err := json.Unmarshal(resp, &found); err != nil {
		return fmt.Errorf("decode the token %s: %w", prefix, err)
	}
	if found.Kind != store.TokenKindRunner {
		return fmt.Errorf("%s names the token %s, which is a %s token held by %q, not a runner token; "+
			"revoke it with `sparkwing cluster tokens revoke` if that is what you meant",
			configPath, prefix, found.Kind, found.Principal)
	}
	return nil
}

func validateAgentFileConfig(cfg agentFileConfig) error {
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	var parsed agentconfig.Config
	if err := decoder.Decode(&parsed); err != nil {
		return fmt.Errorf("the assembled agent config does not parse: %w", err)
	}
	if _, err := agentconfig.Validate(parsed); err != nil {
		return err
	}
	return nil
}

// safety: the claim-mode subset install/service-install.sh writes.
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
	return agentconfig.DefaultPath()
}

func checkAgentConfigAbsent(path string, force bool) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; sparkwing writes a credential only to a regular file, "+
			"so move it aside and run this again", path)
	}
	if force {
		return nil
	}
	return fmt.Errorf("%s already exists; pass --force to replace it (its token stays live until you revoke it)", path)
}

// safety: the config is replaced by a rename, so an interrupted write leaves the
// previous credential intact rather than a truncated file no agent can read.
func writeAgentConfig(path string, cfg agentFileConfig) error {
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := fssecure.EnsureConfigDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; sparkwing writes a credential only to a regular file", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agent-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "sparkwing: could not clear the temporary file %s: %v\n", name, err)
		}
	}()
	if err := fssecure.TightenOpen(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return fssecure.SecurePrivateConfig(path)
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

// safety: the decision about this machine's service manager, taken before anything irreversible happens.
type runnerServicePlan struct {
	host    agentservice.Host
	skipped bool
	manual  bool
}

// safety: resolving the binary and probing the service manager here means a
// machine that cannot supervise a runner fails before a credential exists.
func planRunnerService(configPath string, skip bool) (runnerServicePlan, error) {
	if skip {
		return runnerServicePlan{skipped: true}, nil
	}
	if runnerServiceGOOS == "windows" {
		return runnerServicePlan{manual: true}, nil
	}
	host, err := runnerServiceHost(configPath)
	if err != nil {
		return runnerServicePlan{}, err
	}
	if host.Binary == "" {
		return runnerServicePlan{}, errRunnerBinaryMissing()
	}
	if err := agentservice.Preflight(host); err != nil {
		if errors.Is(err, agentservice.ErrUnsupported) {
			return runnerServicePlan{manual: true}, nil
		}
		return runnerServicePlan{}, err
	}
	return runnerServicePlan{host: host}, nil
}

func (p runnerServicePlan) start(configPath string) error {
	switch {
	case p.skipped:
		fmt.Printf("skipped the service install; start the runner with `%s agent --config %s`\n", runnerBinaryName, configPath)
		return nil
	case p.manual:
		printWindowsServiceSteps(configPath)
		return nil
	}
	state, err := agentservice.Install(p.host)
	if err != nil {
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
	state, err := agentservice.Uninstall(host)
	if err != nil {
		if errors.Is(err, agentservice.ErrUnsupported) {
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

func defaultRunnerServiceHost(configPath string) (agentservice.Host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return agentservice.Host{}, fmt.Errorf("resolve the home directory the service is installed under: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	root, err := paths.DefaultPaths()
	if err != nil {
		return agentservice.Host{}, err
	}
	return agentservice.Host{
		GOOS:       runnerServiceGOOS,
		Home:       home,
		ConfigHome: configHome,
		Binary:     runnerBinaryPath(),
		ConfigPath: configPath,
		LogPath:    filepath.Join(root.Root, "runner.log"),
		UID:        os.Getuid(),
		Exec:       agentservice.DefaultExec,
	}, nil
}

// safety: a systemd or launchd job inherits no PATH, so the absolute path is baked in.
func runnerBinaryPath() string {
	if found, err := exec.LookPath(runnerBinaryName); err == nil {
		if abs, err := filepath.Abs(found); err == nil {
			return abs
		}
	}
	if self, err := installsite.Self(); err == nil && self != "" {
		beside := filepath.Join(filepath.Dir(self), runnerBinaryName)
		if info, err := os.Stat(beside); err == nil && !info.IsDir() {
			return beside
		}
	}
	return ""
}

func errRunnerBinaryMissing() error {
	return fmt.Errorf("%s is not on PATH and does not sit beside this binary. "+
		"Install it with `go install github.com/sparkwing-dev/sparkwing/cmd/%s@latest` or from the release assets, "+
		"then run this command again; `--no-service` writes the config without it",
		runnerBinaryName, runnerBinaryName)
}
