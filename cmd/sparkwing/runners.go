package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the runner loop lives in this binary, so the service runs a second executable.
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
	config      userconfig.RunnerConfig
	force       bool
	service     runnerServicePlan
	profileName string
	format      string
	pools       []string
	personal    bool
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

func runnerTokenManager(prof *namedProfile) (client.RunnerTokenManager, error) {
	return client.NewRunnerTokenManager(client.EngineOptions{URL: prof.ControllerURL(), Bearer: func(context.Context) (string, error) { return prof.ControllerToken(), nil }})
}

func prepareRunnerEnrollment(args []string) (runnerEnrollment, *namedProfile, error) {
	enrollment, prof, err := parseRunnerEnrollment(args)
	if err != nil {
		return enrollment, prof, err
	}
	if err := checkAgentConfigAbsent(enrollment.path, enrollment.force); err != nil {
		return enrollment, prof, err
	}
	if err := validateRunnerEnrollment(enrollment.config); err != nil {
		return enrollment, prof, err
	}
	enrollment.service, err = planRunnerService(enrollment.path, enrollment.service.skipped)
	return enrollment, prof, err
}

func parseRunnerEnrollment(args []string) (runnerEnrollment, *namedProfile, error) {
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
	var maxConcurrent *int
	if inv.Has("max-concurrent") {
		value, parseErr := strconv.Atoi(inv.Value("max-concurrent"))
		if parseErr != nil || value < 1 {
			return enrollment, nil, &clisurface.UsageError{Path: inv.Command.Path, Message: "--max-concurrent requires a positive integer"}
		}
		maxConcurrent = &value
	}
	enrollment.pools = inv.Flags["reserved-for"]
	enrollment.personal = inv.Bool("personal")
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
	controller, localAdmission := strings.TrimRight(prof.ControllerURL(), "/"), true
	enrollment.config = userconfig.RunnerConfig{Controller: &controller, MaxConcurrent: maxConcurrent, HolderPrefix: &name, Labels: inv.Flags["label"], Repos: inv.Flags["repos"], Contribution: &contribution, LocalAdmission: &localAdmission}
	return enrollment, prof, nil
}

func enrollRunner(ctx context.Context, manager client.RunnerTokenManager, enrollment runnerEnrollment) error {
	issued, err := manager.EnrollRunner(ctx, client.EnrollRunnerRequest{Name: *enrollment.config.HolderPrefix, Repos: enrollment.config.Repos, ReservedPools: enrollment.pools, Personal: enrollment.personal})
	if err != nil {
		return err
	}
	if issued == nil || issued.Capability == "" || issued.Prefix == "" {
		return errors.New("the controller returned no runner credential")
	}
	minted := controllerwire.TokenCreated{Token: issued.Capability, Metadata: controllerwire.TokenMetadata{Prefix: issued.Prefix, Kind: "runner", Principal: "runner:" + *enrollment.config.HolderPrefix}}
	for _, grant := range issued.Grants {
		minted.Metadata.Scopes = append(minted.Metadata.Scopes, capability.Grant(grant))
	}
	service, err := finishRunnerEnrollment(enrollment.path, enrollment.config, minted.Token, enrollment.force, enrollment.service, func() error {
		_, revokeErr := manager.RevokeToken(context.WithoutCancel(ctx), minted.Metadata.Prefix)
		if revokeErr != nil {
			printRunnerRevokeHint(enrollment.profileName, minted.Metadata.Prefix)
			return fmt.Errorf("revoke failed enrollment token %s: %w", minted.Metadata.Prefix, revokeErr)
		}
		return nil
	})
	if err != nil {
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
	Personal bool                         `json:"personal"`
	Runner   string                       `json:"runner"`
	Profile  string                       `json:"profile"`
	Config   string                       `json:"config"`
	Token    controllerwire.TokenMetadata `json:"token"`
	Service  runnerServiceResult          `json:"service"`
}

func renderRunnerEnrollment(enrollment runnerEnrollment, token controllerwire.TokenMetadata, service runnerServiceResult) error {
	result := runnerEnrollmentResult{Runner: *enrollment.config.HolderPrefix, Profile: enrollment.profileName, Config: enrollment.path, Token: token, Service: service, Personal: enrollment.personal}
	switch enrollment.format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(result)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, result.Runner)
		return err
	}
	fmt.Printf("minted runner token %s for %s on profile %s\n", token.Prefix, token.Principal, enrollment.profileName)
	if enrollment.personal {
		fmt.Println("personal capacity: accepts only your runs")
	}
	fmt.Printf("wrote the runner section of %s (mode 0600)\n", enrollment.path)
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

func finishRunnerEnrollment(path string, cfg userconfig.RunnerConfig, bearer string, force bool, service runnerServicePlan, revoke func() error) (result runnerServiceResult, err error) {
	written, err := writeRunnerConfig(path, cfg, bearer, force)
	defer func() {
		if err != nil {
			err = errors.Join(err, revoke(), written.rollback())
		}
	}()
	if err != nil {
		return result, err
	}
	return service.start(path)
}

// safety: a failed revocation leaves authority live even after its local file is removed.
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

func prepareRunnerRemoval(args []string) (runnerRemoval, *namedProfile, error) {
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
	cfg, err := userconfig.ReadConfig(path)
	if err != nil {
		return removal, nil, err
	}
	if cfg.Runner == nil {
		return removal, nil, fmt.Errorf("%s has no runner section; sparkwing runners add writes one: %w", path, os.ErrNotExist)
	}
	raw := cfg.Runner
	name := ""
	if raw.HolderPrefix != nil {
		name = *raw.HolderPrefix
	}
	if selectedName := inv.Value("runner"); selectedName != "" && selectedName != name {
		return removal, nil, fmt.Errorf("%s holds runner %q, which does not match --runner %q", path, name, selectedName)
	}
	// safety: a connection for another controller must not revoke a same-prefix credential there.
	if raw.Controller == nil {
		return removal, nil, fmt.Errorf("%s: runner controller is required", path)
	}
	configured, err := normalizeControllerURL(*raw.Controller)
	if err != nil {
		return removal, nil, fmt.Errorf("%s: invalid runner controller URL", path)
	}
	selected, err := normalizeControllerURL(prof.ControllerURL())
	if err != nil || configured != selected {
		return removal, nil, fmt.Errorf("%s: runner controller differs from profile %q", path, prof.Name)
	}
	if raw.CredentialsDir == nil || strings.TrimSpace(*raw.CredentialsDir) == "" {
		return removal, nil, fmt.Errorf("%s: runner.credentials_dir is required", path)
	}
	credential, err := fssecure.OpenPrivateConfig(filepath.Join(*raw.CredentialsDir, "agent-token"))
	if err != nil {
		return removal, nil, err
	}
	value, readErr := io.ReadAll(credential)
	if err := errors.Join(readErr, credential.Close()); err != nil {
		return removal, nil, err
	}
	prefix, err := tokenPrefix(strings.TrimSpace(string(value)))
	if err != nil {
		return removal, nil, fmt.Errorf("%s: %w", path, err)
	}
	return runnerRemoval{path: path, profileName: prof.Name, tokenPrefix: prefix, noService: inv.Bool("no-service"), name: name, format: format}, prof, nil
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
	fmt.Printf("the runner section of %s retains the revoked credential file; sparkwing runners add --force replaces it\n", removal.path)
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

func validateRunnerEnrollment(cfg userconfig.RunnerConfig) error {
	_, _, err := settings.RunnerPreferences(settings.Inputs{Flags: settings.Values{RunnerPreferences: &cfg}})
	return err
}

func agentConfigPath(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	path, err := userconfig.Path()
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func checkAgentConfigAbsent(path string, force bool) error {
	section, err := userconfig.Node(path, userconfig.Runner)
	if err != nil || section == nil || force {
		return err
	}
	return errAgentSectionExists(path)
}

func errAgentSectionExists(path string) error {
	return fmt.Errorf("%s already has a runner section; pass --force to replace it (its token stays live until you revoke it)", path)
}

// safety: the absence check repeats under the settings lock, so a second
// enrollment racing this one cannot overwrite the section it wrote.
func writeRunnerConfig(path string, cfg userconfig.RunnerConfig, bearer string, force bool) (runnerConfigWrite, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return runnerConfigWrite{}, err
	}
	written := runnerConfigWrite{path: path}
	if err := validateRunnerEnrollment(cfg); err != nil {
		return written, err
	}
	if strings.TrimSpace(bearer) == "" {
		return written, errors.New("runner credential is empty")
	}
	var section yaml.Node
	err = userconfig.Update(path, userconfig.Runner, "the runner config", &section, func(found bool) error {
		if found && !force {
			return errAgentSectionExists(path)
		}
		if found {
			written.previous = new(userconfig.RunnerConfig)
			if err := section.Decode(written.previous); err != nil {
				return err
			}
		}
		// safety: a failed config replacement must leave the previous credential intact.
		credentialDir, err := fssecure.MkdirPrivateTemp(filepath.Dir(path), "runner-credentials-")
		if err != nil {
			return err
		}
		written.credentialsDir = credentialDir
		if err := fssecure.WriteFile(filepath.Join(credentialDir, "agent-token"), []byte(bearer)); err != nil {
			return err
		}
		cfg.CredentialsDir = &credentialDir
		section, err = runnerConfigNode(cfg)
		if err != nil {
			return err
		}
		return section.Decode(&written.current)
	})
	return written, err
}

func runnerConfigNode(cfg userconfig.RunnerConfig) (yaml.Node, error) {
	var section yaml.Node
	if err := section.Encode(cfg); err != nil {
		return section, err
	}
	for _, name := range []string{"repos", "labels"} {
		values := cfg.Repos
		if name == "labels" {
			values = cfg.Labels
		}
		if values != nil && len(values) == 0 {
			section.Content = append(section.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
		}
	}
	return section, nil
}

type runnerConfigWrite struct {
	path, credentialsDir string
	previous             *userconfig.RunnerConfig
	current              userconfig.RunnerConfig
}

func (w runnerConfigWrite) rollback() error {
	if w.credentialsDir == "" {
		return nil
	}
	var section yaml.Node
	unchanged := errors.New("runner configuration changed")
	retained := false
	err := userconfig.Update(w.path, userconfig.Runner, "the runner config", &section, func(found bool) error {
		if !found {
			return unchanged
		}
		var current userconfig.RunnerConfig
		if err := section.Decode(&current); err != nil {
			return err
		}
		// safety: another enrollment may have replaced ours while its service was starting.
		if current.CredentialsDir == nil || *current.CredentialsDir != w.credentialsDir {
			return unchanged
		}
		if !reflect.DeepEqual(current, w.current) {
			retained = true
			return unchanged
		}
		section = yaml.Node{}
		if w.previous != nil {
			var err error
			section, err = runnerConfigNode(*w.previous)
			return err
		}
		return nil
	})
	if errors.Is(err, unchanged) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("restore runner configuration before credential cleanup: %w", err)
	}
	if retained {
		return fmt.Errorf("runner configuration changed; retained its credential file in %s", w.credentialsDir)
	}
	for _, path := range []string{filepath.Join(w.credentialsDir, "agent-token"), w.credentialsDir} {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}
	if err != nil {
		return fmt.Errorf("clean failed runner enrollment: %w", err)
	}
	return nil
}

// safety: the decision about this machine's service manager, taken before anything irreversible happens.
type runnerServicePlan struct {
	host    runnerservice.Host
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
	if err := runnerservice.Preflight(context.Background(), host); err != nil {
		if errors.Is(err, runnerservice.ErrUnsupported) {
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
		return runnerServiceResult{Status: status, Command: []string{runnerBinaryName, "start", "--config", configPath}}, nil
	}
	before, err := runnerservice.Inspect(context.Background(), p.host)
	if err != nil {
		return runnerServiceResult{}, err
	}
	state, err := runnerservice.Install(context.Background(), p.host)
	if err != nil {
		if !before.Installed && !before.Foreign {
			after, inspectErr := runnerservice.Inspect(context.Background(), p.host)
			err = errors.Join(err, inspectErr)
			if inspectErr == nil && after.Installed && !after.Foreign && !after.Mismatch {
				err = errors.Join(err, cleanupNewRunnerService(p.host, after.Path))
			}
		}
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
	state, err := runnerservice.Uninstall(context.Background(), host)
	if errors.Is(err, runnerservice.ErrUnsupported) {
		return "", nil
	}
	return state.Detail, err
}

func defaultRunnerServiceHost(configPath string) (runnerservice.Host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return runnerservice.Host{}, fmt.Errorf("resolve the home directory the service is installed under: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	root, err := paths.DefaultPaths()
	if err != nil {
		return runnerservice.Host{}, err
	}
	return runnerservice.Host{
		GOOS:       runnerServiceGOOS,
		Home:       home,
		ConfigHome: configHome,
		Binary:     runnerBinaryPath(),
		ConfigPath: configPath,
		LogPath:    filepath.Join(root.Root, "runner.log"),
		UID:        os.Getuid(),
		Exec:       runnerservice.DefaultExec,
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

func cleanupNewRunnerService(host runnerservice.Host, path string) error {
	published, statErr := os.Lstat(path)
	_, err := runnerservice.Uninstall(context.Background(), host)
	if err == nil {
		return nil
	}
	if statErr != nil {
		return errors.Join(err, statErr)
	}
	current, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return err
	}
	if statErr != nil {
		return errors.Join(err, statErr)
	}
	// safety: manager failure must not strand a new file, but a replacement belongs to another writer.
	if !current.Mode().IsRegular() || !os.SameFile(published, current) {
		return errors.Join(err, fmt.Errorf("runner service file changed during failed enrollment cleanup: %s", path))
	}
	return errors.Join(err, os.Remove(path))
}
