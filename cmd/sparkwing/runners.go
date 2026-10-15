package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/agentservice"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

type runnerEnrollment struct {
	path        string
	config      agentFileConfig
	force       bool
	service     runnerServicePlan
	profileName string
	format      string
}

func runRunnersAdd(args []string) error {
	enrollment, prof, err := prepareRunnerEnrollment(args)
	if errors.Is(err, errHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	manager, err := runnerTokenManager(prof)
	if err != nil {
		return err
	}
	return enrollRunner(context.Background(), manager, enrollment)
}

func runnerTokenManager(prof *profile.Profile) (controllerwire.TokenManager, error) {
	return client.NewTokenManager(client.EngineOptions{URL: prof.ControllerURL(), Bearer: func(context.Context) (string, error) { return prof.ControllerToken(), nil }}, false)
}

func prepareRunnerEnrollment(args []string) (runnerEnrollment, *profile.Profile, error) {
	enrollment, prof, err := parseRunnerEnrollment(args)
	if err != nil {
		return enrollment, prof, err
	}
	if err := checkAgentConfigAbsent(enrollment.path, enrollment.force); err != nil {
		return enrollment, prof, err
	}
	if err := validateAgentFileConfig(enrollment.config); err != nil {
		return enrollment, prof, err
	}
	enrollment.service, err = planRunnerService(enrollment.path, enrollment.service.skipped)
	return enrollment, prof, err
}

func parseRunnerEnrollment(args []string) (runnerEnrollment, *profile.Profile, error) {
	var enrollment runnerEnrollment
	inv, err := clisurface.Parse("sparkwing", append([]string{"runners", "add"}, args...))
	if err != nil {
		return enrollment, nil, err
	}
	enrollment.format = inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		if err := writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, enrollment.format); err != nil {
			return enrollment, nil, err
		}
		return enrollment, nil, errHelpRequested
	}
	name := inv.Value("runner")
	if strings.TrimSpace(name) == "" {
		return enrollment, nil, &clisurface.UsageError{Path: inv.Command.Path, Message: "--runner is required"}
	}
	maxConcurrent := 2
	if inv.Has("max-concurrent") {
		maxConcurrent, err = strconv.Atoi(inv.Value("max-concurrent"))
		if err != nil || maxConcurrent < 1 {
			return enrollment, nil, &clisurface.UsageError{Path: inv.Command.Path, Message: "--max-concurrent requires a positive integer"}
		}
	}
	if inv.Has("reserved-for") {
		return enrollment, nil, &apierr.Error{Code: apierr.PolicyMissing, Message: "runner pool enrollment is unavailable", Hint: "the controller must support team-admin runner registration"}
	}
	prof, err := resolveProfile(inv.Value("profile"))
	if err != nil {
		return enrollment, nil, err
	}
	if err := requireController(prof, "runners add"); err != nil {
		return enrollment, nil, err
	}
	enrollment.path, err = agentConfigPath(inv.Value("config"))
	if err != nil {
		return enrollment, nil, err
	}
	enrollment.profileName, enrollment.force = prof.Name, inv.Bool("force")
	enrollment.service.skipped = inv.Bool("no-service")
	contribution := "50%,50%"
	if inv.Has("contribution") {
		contribution = inv.Value("contribution")
	}
	enrollment.config = agentFileConfig{Controller: strings.TrimRight(prof.ControllerURL(), "/"), MaxConcurrent: maxConcurrent, HolderPrefix: name, Labels: inv.Flags["label"], AllowRepos: inv.Flags["repos"], Contribution: contribution, LocalAdmission: true}
	return enrollment, prof, nil
}

func enrollRunner(ctx context.Context, manager controllerwire.TokenManager, enrollment runnerEnrollment) error {
	principal := "runner:" + enrollment.config.HolderPrefix
	minted, err := manager.CreateToken(ctx, controllerwire.TokenCreate{Kind: "runner", Principal: principal, Scopes: []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.RunsState}, IdempotencyKey: rand.Text()})
	if err != nil {
		return err
	}
	enrollment.config.Token = minted.Token
	service, err := finishRunnerEnrollment(enrollment.path, enrollment.config, enrollment.force, enrollment.service)
	if err != nil {
		printRunnerRevokeHint(enrollment.profileName, minted.Metadata.Prefix)
		return err
	}
	return renderRunnerEnrollment(enrollment, minted.Metadata, service)
}

type runnerServiceResult struct {
	Status  string   `json:"status"`
	Detail  string   `json:"detail,omitempty"`
	Path    string   `json:"path,omitempty"`
	Command []string `json:"command,omitempty"`
}

type runnerEnrollmentResult struct {
	Runner  string                       `json:"runner"`
	Profile string                       `json:"profile"`
	Config  string                       `json:"config"`
	Token   controllerwire.TokenMetadata `json:"token"`
	Service runnerServiceResult          `json:"service"`
}

func renderRunnerEnrollment(enrollment runnerEnrollment, token controllerwire.TokenMetadata, service runnerServiceResult) error {
	result := runnerEnrollmentResult{Runner: enrollment.config.HolderPrefix, Profile: enrollment.profileName, Config: enrollment.path, Token: token, Service: service}
	switch enrollment.format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(result)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, result.Runner)
		return err
	}
	fmt.Printf("minted runner token %s for %s on profile %s\n", token.Prefix, token.Principal, enrollment.profileName)
	fmt.Printf("wrote the agent section of %s (mode 0600)\n", enrollment.path)
	fmt.Printf("service: %s\n", service.Status)
	if service.Detail != "" {
		fmt.Println(service.Detail)
	}
	if service.Path != "" {
		fmt.Printf("service file: %s\n", service.Path)
	}
	if len(service.Command) > 0 {
		fmt.Printf("start the runner with argv: %q\n", service.Command)
	}
	fmt.Println("what this machine accepts and isolates: sparkwing docs read --topic threat-model")
	fmt.Printf("revoke this runner with:\n  sparkwing runners remove --profile %s --config %s\n", enrollment.profileName, enrollment.path)
	fmt.Printf("or revoke its token with:\n  sparkwing tokens revoke --profile %s --token %s\n", enrollment.profileName, token.Prefix)
	return nil
}

func finishRunnerEnrollment(path string, cfg agentFileConfig, force bool, service runnerServicePlan) (runnerServiceResult, error) {
	if err := writeAgentConfig(path, cfg, force); err != nil {
		return runnerServiceResult{}, err
	}
	return service.start(path)
}

// safety: the credential outlives every failure after the mint, so the operator is always told how to kill it.
func printRunnerRevokeHint(profileName, prefix string) {
	fmt.Fprintf(os.Stderr, "the token %s is live; revoke it with:\n  sparkwing tokens revoke --profile %s --token %s\n", prefix, profileName, prefix)
}

type runnerRemoval struct {
	path, profileName, tokenPrefix, name, format string
	noService                                    bool
}

func runRunnersRemove(args []string) error {
	removal, prof, err := prepareRunnerRemoval(args)
	if errors.Is(err, errHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	manager, err := runnerTokenManager(prof)
	if err != nil {
		return err
	}
	return removeRunner(context.Background(), manager, removal)
}

func prepareRunnerRemoval(args []string) (runnerRemoval, *profile.Profile, error) {
	var removal runnerRemoval
	inv, err := clisurface.Parse("sparkwing", append([]string{"runners", "remove"}, args...))
	if err != nil {
		return removal, nil, err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		if err := writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, format); err != nil {
			return removal, nil, err
		}
		return removal, nil, errHelpRequested
	}
	prof, err := resolveProfile(inv.Value("profile"))
	if err != nil {
		return removal, nil, err
	}
	if err := requireController(prof, "runners remove"); err != nil {
		return removal, nil, err
	}
	path, err := agentConfigPath(inv.Value("config"))
	if err != nil {
		return removal, nil, err
	}
	raw, err := agentconfig.Load(path)
	if err != nil {
		return removal, nil, err
	}
	if name := inv.Value("runner"); name != "" && name != raw.HolderPrefix {
		return removal, nil, fmt.Errorf("%s holds runner %q, which does not match --runner %q", path, raw.HolderPrefix, name)
	}
	// safety: a connection for another controller must not revoke a same-prefix credential there.
	configured, err := normalizeControllerURL(raw.Controller)
	if err != nil {
		return removal, nil, fmt.Errorf("%s: invalid runner controller URL", path)
	}
	selected, err := normalizeControllerURL(prof.ControllerURL())
	if err != nil || configured != selected {
		return removal, nil, fmt.Errorf("%s: runner controller differs from profile %q", path, prof.Name)
	}
	prefix, err := tokenPrefix(raw.Token)
	if err != nil {
		return removal, nil, fmt.Errorf("%s: %w", path, err)
	}
	return runnerRemoval{path: path, profileName: prof.Name, tokenPrefix: prefix, noService: inv.Bool("no-service"), name: raw.HolderPrefix, format: format}, prof, nil
}

func removeRunner(ctx context.Context, manager controllerwire.TokenManager, removal runnerRemoval) error {
	if err := requireRunnerToken(ctx, manager, removal.tokenPrefix, removal.path); err != nil {
		return err
	}
	// safety: stop before revocation so supervised work can release its claims with a live root.
	if _, err := stopRunnerService(removal.path, removal.noService); err != nil {
		return err
	}
	revoked, err := manager.RevokeToken(ctx, removal.tokenPrefix)
	if err != nil {
		return err
	}
	if removal.format == "json" {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Runner string                      `json:"runner"`
			Token  controllerwire.TokenRevoked `json:"token"`
		}{removal.name, revoked})
	}
	if removal.format == "plain" {
		_, err := fmt.Fprintln(os.Stdout, removal.name)
		return err
	}
	fmt.Printf("revoked %s on profile %s\n", removal.tokenPrefix, removal.profileName)
	fmt.Printf("the agent section of %s retains the revoked token; sparkwing runners add --force replaces it\n", removal.path)
	return nil
}

func tokenPrefix(raw string) (string, error) {
	if len(raw) < store.PrefixLen {
		return "", errors.New("token is too short to carry a prefix")
	}
	return raw[:store.PrefixLen], nil
}

func requireRunnerToken(ctx context.Context, manager controllerwire.TokenManager, prefix, configPath string) error {
	found, err := manager.ReadToken(ctx, prefix)
	if err != nil {
		return fmt.Errorf("look up token %s named by %s: %w", prefix, configPath, err)
	}
	// safety: principal spelling cannot grant runner identity or justify revoking a member.
	if found.Kind != "runner" {
		return fmt.Errorf("%s names token %s, which is a %s token held by %q, not a runner token; use sparkwing tokens revoke if required", configPath, prefix, found.Kind, found.Principal)
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
	Token          string   `yaml:"token"`
	MaxConcurrent  int      `yaml:"max_concurrent"`
	HolderPrefix   string   `yaml:"holder_prefix"`
	Labels         []string `yaml:"labels,omitempty"`
	AllowRepos     []string `yaml:"allow_repos,omitempty"`
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
	section, err := userconfig.Node(path, userconfig.Agent)
	if err != nil || section == nil || force {
		return err
	}
	return errAgentSectionExists(path)
}

func errAgentSectionExists(path string) error {
	return fmt.Errorf("%s already has an agent section; pass --force to replace it (its token stays live until you revoke it)", path)
}

// safety: the absence check repeats under the settings lock, so a second
// enrollment racing this one cannot overwrite the section it wrote.
func writeAgentConfig(path string, cfg agentFileConfig, force bool) error {
	var section yaml.Node
	return userconfig.Update(path, userconfig.Agent, "the agent config", &section, func(found bool) error {
		if found && !force {
			return errAgentSectionExists(path)
		}
		section = yaml.Node{}
		return section.Encode(cfg)
	})
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

func (p runnerServicePlan) start(configPath string) (runnerServiceResult, error) {
	if p.skipped || p.manual {
		status := "skipped"
		if p.manual {
			status = "manual"
		}
		return runnerServiceResult{Status: status, Command: []string{runnerBinaryName, "agent", "--config", configPath}}, nil
	}
	state, err := agentservice.Install(p.host)
	if err != nil {
		return runnerServiceResult{}, err
	}
	return runnerServiceResult{Status: "started", Detail: state.Detail, Path: state.Path}, nil
}

func stopRunnerService(configPath string, skip bool) (string, error) {
	if skip || runnerServiceGOOS == "windows" {
		return "", nil
	}
	host, err := runnerServiceHost(configPath)
	if err != nil {
		return "", err
	}
	state, err := agentservice.Uninstall(host)
	if errors.Is(err, agentservice.ErrUnsupported) {
		return "", nil
	}
	return state.Detail, err
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
