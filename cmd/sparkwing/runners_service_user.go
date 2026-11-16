package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

// safety: privileged steps reach the machine only through these,
// so a suite can substitute a host that touches nothing.
var (
	serviceUserPrivilege   privilegedRunner = sudoRunner{}
	serviceUserBooted                       = systemdBooted
	serviceUserEUID                         = os.Geteuid
	serviceUserAccountHost                  = defaultServiceAccountHost
	serviceUserUnitDir                      = runnerservice.SystemUnitDir
	serviceUserSystemExec                   = runnerservice.DefaultExec
	serviceUserRunAs                        = runAsServiceAccount
	serviceUserStage                        = stageServiceBinaries
	runnerAccountHome                       = os.UserHomeDir
)

type privilegedRunner interface {
	Check(ctx context.Context) error
	Run(ctx context.Context, args []string, stdin []byte) ([]byte, error)
}

type serviceUserEnrollment struct {
	Runner       userconfig.RunnerConfig `json:"runner"`
	Credential   string                  `json:"credential"`
	RunnerBinary string                  `json:"runner_binary"`
	Path         string                  `json:"path"`
	Force        bool                    `json:"force"`
}

type serviceUserInstalled struct {
	Account     runnerservice.Account `json:"account"`
	Created     bool                  `json:"created"`
	Config      string                `json:"config"`
	Service     runnerServiceResult   `json:"service"`
	DroppedPath []string              `json:"dropped_path,omitempty"`
}

type serviceUserRemoved struct {
	Account     string `json:"account"`
	Home        string `json:"home"`
	Runner      string `json:"runner"`
	Controller  string `json:"controller"`
	TokenPrefix string `json:"token_prefix"`
	Detail      string `json:"detail"`
}

type accountWritten struct {
	Config    string `json:"config"`
	Binary    string `json:"binary"`
	StateHome string `json:"state_home"`
}

type sudoRunner struct{}

func (sudoRunner) Check(ctx context.Context) error {
	cmd := procenv.ControlCommandContext(ctx, controlPath(), "sudo", "-v")
	// safety: sudo prompts on the terminal; stdin may carry the enrollment session.
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("--service-user creates an OS user and a system service, which needs sudo: %w", err)
	}
	return nil
}

func (sudoRunner) Run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	self, err := installsite.Self()
	if err != nil {
		return nil, err
	}
	cmd := procenv.ControlCommandContext(ctx, controlPath(), "sudo", append([]string{"--", self}, args...)...)
	var stdout bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sudo sparkwing %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

func controlPath() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"} }

// safety: sd_booted's test; without systemd as init there is no system manager to install into.
func systemdBooted() bool {
	info, err := os.Stat("/run/systemd/system")
	return err == nil && info.IsDir()
}

func planServiceUser(ctx context.Context, enrollment *runnerEnrollment) error {
	switch runnerServiceGOOS {
	case "linux":
		if !serviceUserBooted() {
			return errors.New("--service-user installs a systemd system service, and systemd is not this machine's init system")
		}
	case "darwin":
	default:
		return fmt.Errorf("--service-user installs a systemd service on Linux or a launchd daemon on macOS, not on %s", runnerServiceGOOS)
	}
	host, err := runnerServiceHost("")
	if err != nil {
		return err
	}
	if host.Binary == "" {
		return errRunnerBinaryMissing()
	}
	enrollment.runnerBinary, enrollment.servicePath = host.Binary, host.Path
	// safety: asking for the password before minting means a refusal leaves no live credential.
	return serviceUserPrivilege.Check(ctx)
}

func finishServiceUserEnrollment(ctx context.Context, manager client.RunnerTokenManager, enrollment runnerEnrollment, minted controllerwire.TokenCreated) error {
	installed, err := installEnrolledServiceUser(ctx, enrollment, minted.Token)
	if err != nil {
		if _, revokeErr := manager.RevokeToken(context.WithoutCancel(ctx), minted.Metadata.Prefix); revokeErr != nil {
			printRunnerRevokeHint(enrollment.profileName, minted.Metadata.Prefix)
			err = errors.Join(err, fmt.Errorf("revoke failed enrollment token %s: %w", minted.Metadata.Prefix, revokeErr))
		}
		return err
	}
	enrollment.path = installed.Config
	return renderRunnerEnrollment(enrollment, minted.Metadata, installed.Service, &installed)
}

func installEnrolledServiceUser(ctx context.Context, enrollment runnerEnrollment, credential string) (serviceUserInstalled, error) {
	payload, err := json.Marshal(serviceUserEnrollment{Runner: enrollment.config, Credential: credential, RunnerBinary: enrollment.runnerBinary, Path: enrollment.servicePath, Force: enrollment.force})
	if err != nil {
		return serviceUserInstalled{}, err
	}
	out, err := serviceUserPrivilege.Run(ctx, []string{"runners", "install-service", "--service-user", enrollment.serviceUser, "--output", "json"}, payload)
	if err != nil {
		return serviceUserInstalled{}, err
	}
	var installed serviceUserInstalled
	if err := json.Unmarshal(out, &installed); err != nil {
		return serviceUserInstalled{}, fmt.Errorf("read the privileged install result: %w", err)
	}
	return installed, nil
}

func renderServiceUserEnrollment(enrollment runnerEnrollment, installed serviceUserInstalled) {
	account := opsview.DisplayText(installed.Account.Name)
	if installed.Created {
		fmt.Printf("created the OS user %s with home %s\n", account, opsview.DisplayText(installed.Account.Home))
	}
	fmt.Printf("wrote the runner section of %s (mode 0600, owned by %s)\n", opsview.DisplayText(installed.Config), account)
	fmt.Printf("service: %s\n", installed.Service.Status)
	fmt.Println(opsview.DisplayText(installed.Service.Detail))
	fmt.Printf("service file: %s\n", opsview.DisplayText(installed.Service.Path))
	limit := "1"
	if enrollment.config.MaxConcurrent != nil {
		limit = strconv.Itoa(*enrollment.config.MaxConcurrent)
	}
	fmt.Printf("local admission is off for a dedicated-user runner, so --max-concurrent is the only cap on its load (now %s)\n", limit)
	if len(installed.DroppedPath) > 0 {
		fmt.Printf("left out of the service PATH because %s cannot reach them: %s\ninstall the toolchains its jobs need system-wide\n", account, opsview.DisplayText(strings.Join(installed.DroppedPath, ", ")))
	}
}

func runRunnersInstallService(inv clisurface.Invocation) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, format)
	}
	if err := requireServiceRoot(runnerServiceGOOS, inv.Value("service-user"), "install-service"); err != nil {
		return err
	}
	var payload serviceUserEnrollment
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&payload); err != nil {
		return fmt.Errorf("read the runner enrollment from stdin (sparkwing runners add --service-user writes it): %w", err)
	}
	installed, err := installServiceUser(context.Background(), runnerServiceGOOS, inv.Value("service-user"), payload)
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(installed)
	}
	fmt.Printf("runner service %s runs as %s; config %s\n", opsview.DisplayText(installed.Service.Path), opsview.DisplayText(installed.Account.Name), opsview.DisplayText(installed.Config))
	return nil
}

func installServiceUser(ctx context.Context, goos, name string, payload serviceUserEnrollment) (serviceUserInstalled, error) {
	if err := requireServiceRoot(goos, name, "install-service"); err != nil {
		return serviceUserInstalled{}, err
	}
	if strings.TrimSpace(payload.Credential) == "" || !filepath.IsAbs(payload.RunnerBinary) {
		return serviceUserInstalled{}, errors.New("the runner enrollment on stdin needs a credential and an absolute runner_binary")
	}
	prefix, err := tokenPrefix(strings.TrimSpace(payload.Credential))
	if err != nil {
		return serviceUserInstalled{}, fmt.Errorf("the runner enrollment on stdin: %w", err)
	}
	account, created, err := runnerservice.EnsureAccount(ctx, serviceUserAccountHost(goos), name)
	if err != nil {
		return serviceUserInstalled{}, err
	}
	self, runner, cleanup, err := serviceUserStage(goos, payload.RunnerBinary)
	if err != nil {
		return serviceUserInstalled{}, err
	}
	defer cleanup()
	stdin, err := json.Marshal(payload)
	if err != nil {
		return serviceUserInstalled{}, err
	}
	out, err := serviceUserRunAs(ctx, account, []string{self, "__runner-account-write", "--runner-binary", runner}, stdin)
	if err != nil {
		return serviceUserInstalled{}, err
	}
	var written accountWritten
	if err := json.Unmarshal(out, &written); err != nil {
		return serviceUserInstalled{}, fmt.Errorf("read what %s wrote: %w", name, err)
	}
	record := serviceUserRemoved{TokenPrefix: prefix}
	if payload.Runner.HolderPrefix != nil {
		record.Runner = *payload.Runner.HolderPrefix
	}
	if payload.Runner.Controller != nil {
		record.Controller = *payload.Runner.Controller
	}
	if err := writeEnrollmentRecord(enrollmentRecordPath(goos, name), record); err != nil {
		return serviceUserInstalled{}, err
	}
	path, dropped := runnerservice.ServicePath(payload.Path, filepath.Dir(written.Binary))
	host := runnerservice.SystemHost{GOOS: goos, Account: account, UnitDir: serviceUserUnitDir(goos), Binary: written.Binary, ConfigPath: written.Config, StateHome: written.StateHome, Path: path, Exec: serviceUserSystemExec}
	if goos == "darwin" {
		// safety: launchd opens log files as root, so they live outside the account's reach.
		host.LogPath = "/Library/Logs/sparkwing-runner-" + name + ".log"
	}
	state, err := runnerservice.InstallSystem(ctx, host)
	if err != nil {
		return serviceUserInstalled{}, err
	}
	return serviceUserInstalled{Account: account, Created: created, Config: written.Config, Service: runnerServiceResult{Status: "started", Detail: state.Detail, Path: state.Path}, DroppedPath: dropped}, nil
}

func runRunnersUninstallService(inv clisurface.Invocation) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, format)
	}
	removed, err := uninstallServiceUser(context.Background(), runnerServiceGOOS, inv.Value("service-user"))
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(removed)
	}
	fmt.Printf("%s; revoke its token %s with sparkwing tokens revoke\n", opsview.DisplayText(removed.Detail), opsview.DisplayText(removed.TokenPrefix))
	return nil
}

func uninstallServiceUser(ctx context.Context, goos, name string) (serviceUserRemoved, error) {
	if err := requireServiceRoot(goos, name, "uninstall-service"); err != nil {
		return serviceUserRemoved{}, err
	}
	account, found, err := runnerservice.LookupAccount(ctx, serviceUserAccountHost(goos), name)
	if err != nil {
		return serviceUserRemoved{}, err
	}
	if !found {
		account = runnerservice.Account{Name: name}
	}
	// safety: jobs run as the account and can delete its config, so the stop
	// never depends on it, and the token comes from the root-owned record first.
	state, err := runnerservice.UninstallSystem(ctx, runnerservice.SystemHost{GOOS: goos, Account: account, UnitDir: serviceUserUnitDir(goos), Exec: serviceUserSystemExec})
	if err != nil {
		return serviceUserRemoved{}, fmt.Errorf("%w; the runner token of %s is still live", err, name)
	}
	removed, recordErr := readEnrollmentRecord(enrollmentRecordPath(goos, name))
	if recordErr != nil && found {
		var readErr error
		removed, readErr = readAccountEnrollment(ctx, goos, account)
		recordErr = errors.Join(recordErr, readErr)
		if readErr == nil {
			recordErr = nil
		}
	}
	removed.Account, removed.Home, removed.Detail = account.Name, account.Home, state.Detail
	if recordErr != nil {
		return removed, fmt.Errorf("%s, but nothing names its runner token (%w); find it with sparkwing tokens list --type runner and revoke it with sparkwing tokens revoke", state.Detail, recordErr)
	}
	return removed, nil
}

func readAccountEnrollment(ctx context.Context, goos string, account runnerservice.Account) (serviceUserRemoved, error) {
	self, _, cleanup, err := serviceUserStage(goos, "")
	if err != nil {
		return serviceUserRemoved{}, err
	}
	defer cleanup()
	out, err := serviceUserRunAs(ctx, account, []string{self, "__runner-account-read"}, nil)
	if err != nil {
		return serviceUserRemoved{}, err
	}
	var removed serviceUserRemoved
	if err := json.Unmarshal(out, &removed); err != nil {
		return serviceUserRemoved{}, fmt.Errorf("read the runner %s holds: %w", account.Name, err)
	}
	return removed, nil
}

// safety: the record sits in a root-only directory beside the account homes, out of reach of the account's jobs.
func enrollmentRecordPath(goos, name string) string {
	return filepath.Join(serviceUserAccountHost(goos).HomeRoot, ".enrollments", name+".json")
}

func writeEnrollmentRecord(path string, record serviceUserRemoved) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, err = file.Write(data)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readEnrollmentRecord(path string) (serviceUserRemoved, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return serviceUserRemoved{}, err
	}
	if !info.Mode().IsRegular() {
		return serviceUserRemoved{}, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return serviceUserRemoved{}, err
	}
	var record serviceUserRemoved
	if err := json.Unmarshal(data, &record); err != nil || record.TokenPrefix == "" {
		return serviceUserRemoved{}, fmt.Errorf("%s holds no token prefix", path)
	}
	return record, nil
}

func requireServiceRoot(goos, name, verb string) error {
	if goos != "linux" && goos != "darwin" {
		return fmt.Errorf("runners %s supports Linux and macOS, not %s", verb, goos)
	}
	if err := runnerservice.ValidAccountName(name); err != nil {
		return err
	}
	if serviceUserEUID() != 0 {
		return fmt.Errorf("runners %s changes OS users and system services, so it runs as root; sparkwing runners add and remove --service-user call it through sudo", verb)
	}
	return nil
}

func defaultServiceAccountHost(goos string) runnerservice.AccountHost {
	uid := -1
	if raw := os.Getenv("SUDO_UID"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			uid = parsed
		}
	}
	return runnerservice.AccountHost{GOOS: goos, HomeRoot: runnerservice.DefaultHomeRoot(goos), InvokerName: os.Getenv("SUDO_USER"), InvokerUID: uid, Exec: runnerservice.DefaultExec}
}

func runRunnerAccountWrite(inv clisurface.Invocation) error {
	var payload serviceUserEnrollment
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&payload); err != nil {
		return err
	}
	written, err := writeRunnerAccount(payload, inv.Value("runner-binary"))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(written)
}

// safety: this runs as the service account, so every file it creates is the account's own
// and no root process writes through paths the account controls.
func writeRunnerAccount(payload serviceUserEnrollment, runnerBinary string) (result accountWritten, err error) {
	if serviceUserEUID() == 0 {
		return result, errors.New("the runner account's files are written as that account, never as root")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return result, err
	}
	state, err := paths.DefaultPaths()
	if err != nil {
		return result, err
	}
	result = accountWritten{Config: filepath.Join(fssecure.ConfigDirIn(home), userconfig.Filename), Binary: filepath.Join(home, ".local", "bin", runnerBinaryName), StateHome: state.Root}
	written, err := writeRunnerConfig(result.Config, payload.Runner, payload.Credential, payload.Force)
	defer func() {
		if err != nil {
			err = errors.Join(err, written.rollback())
		}
	}()
	if err != nil {
		if strings.Contains(err.Error(), "--force") {
			err = fmt.Errorf("%w; rerun sparkwing runners add --service-user with --force", err)
		}
		return result, err
	}
	if err = unitfile.PrepareWorkingDirectory(result.StateHome); err != nil {
		return result, err
	}
	return result, installRunnerBinary(runnerBinary, result.Binary)
}

func runRunnerAccountRead(clisurface.Invocation) error {
	read, err := readRunnerAccount()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(read)
}

func readRunnerAccount() (serviceUserRemoved, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return serviceUserRemoved{}, err
	}
	enrolled, err := readEnrolledCredential(filepath.Join(fssecure.ConfigDirIn(home), userconfig.Filename))
	if err != nil {
		return serviceUserRemoved{}, err
	}
	return serviceUserRemoved{Runner: enrolled.name, Controller: enrolled.controller, TokenPrefix: enrolled.prefix}, nil
}

func removeServiceUserRunner(ctx context.Context, manager controllerwire.TokenManager, removal runnerRemoval) error {
	if err := serviceUserPrivilege.Check(ctx); err != nil {
		return err
	}
	out, err := serviceUserPrivilege.Run(ctx, []string{"runners", "uninstall-service", "--service-user", removal.serviceUser, "--output", "json"}, nil)
	if err != nil {
		return err
	}
	var removed serviceUserRemoved
	if err := json.Unmarshal(out, &removed); err != nil {
		return fmt.Errorf("read the privileged removal result: %w", err)
	}
	hint := fmt.Sprintf("the service is stopped, but its token %s stays live", removed.TokenPrefix)
	configured, err := normalizeControllerURL(removed.Controller)
	if err != nil || configured != removal.controller {
		return fmt.Errorf("%s: the runner of %s belongs to controller %s, not profile %s's; revoke it with sparkwing tokens revoke on that profile", hint, removal.serviceUser, removed.Controller, removal.profileName)
	}
	if removal.name != "" && removal.name != removed.Runner {
		return fmt.Errorf("%s: %s holds runner %q, not --runner %q", hint, removal.serviceUser, removed.Runner, removal.name)
	}
	removal.name, removal.tokenPrefix = removed.Runner, removed.TokenPrefix
	if err := requireRunnerToken(ctx, manager, removal.tokenPrefix, removal.serviceUser+"'s runner config"); err != nil {
		return err
	}
	revoked, err := manager.RevokeToken(ctx, removal.tokenPrefix)
	if err != nil {
		return err
	}
	switch removal.format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(struct {
			Runner      string                      `json:"runner"`
			ServiceUser string                      `json:"service_user"`
			Token       controllerwire.TokenRevoked `json:"token"`
		}{removal.name, removal.serviceUser, revoked})
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, removal.name)
		return err
	}
	fmt.Println(opsview.DisplayText(removed.Detail))
	fmt.Printf("revoked %s on profile %s\n", removal.tokenPrefix, opsview.DisplayText(removal.profileName))
	fmt.Printf("the OS user %s and its home %s remain; delete them with:\n  %s\n", opsview.DisplayText(removal.serviceUser), opsview.DisplayText(removed.Home), deleteAccountCommand(runnerServiceGOOS, removal.serviceUser, removed.Home))
	return nil
}

func deleteAccountCommand(goos, name, home string) string {
	if goos == "darwin" {
		return fmt.Sprintf("sudo dscl . -delete /Users/%s && sudo dscl . -delete /Groups/%s && sudo rm -rf %s", name, name, home)
	}
	return "sudo userdel --remove " + name
}

var credentialMarkers = []string{
	".ssh", ".aws", ".azure", ".config/gcloud", ".kube", ".docker/config.json", ".gnupg", ".netrc", ".git-credentials",
	".mozilla", ".config/google-chrome", ".config/chromium", "Library/Application Support/Google/Chrome", "Library/Application Support/Firefox",
}

func runnerEnrollmentWarnings(enrollment runnerEnrollment) []string {
	var warnings []string
	if !enrollment.personal && enrollment.serviceUser == "" && !enrollment.launcher && enrollment.platform == "" {
		if home, err := runnerAccountHome(); err == nil {
			var found []string
			for _, marker := range credentialMarkers {
				if _, err := os.Lstat(filepath.Join(home, filepath.FromSlash(marker))); err == nil {
					found = append(found, "~/"+marker)
				}
			}
			if len(found) > 0 {
				who := "your own OS account"
				if user := os.Getenv("USER"); user != "" {
					who += " (" + user + ")"
				}
				warnings = append(warnings, fmt.Sprintf("this shared runner runs any pushed branch of its repositories as %s, and that code can read %s. Run it under a dedicated OS user instead: add --service-user sparkwing-runner (sparkwing docs read --topic shared-runners)", who, strings.Join(found, ", ")))
			}
		}
	}
	repos := enrollment.config.Repos
	if len(repos) > 1 || (len(repos) == 1 && strings.ContainsAny(repos[0], "*?[")) {
		warnings = append(warnings, fmt.Sprintf("--repos %s spans several repositories: a job from one can claim the others' jobs and read their secrets. Enroll one runner per repository unless they trust each other", strings.Join(repos, ", ")))
	}
	return warnings
}
