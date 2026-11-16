package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type runnerPauseResult struct {
	Runner  string `json:"runner"`
	Paused  bool   `json:"paused"`
	Changed bool   `json:"changed"`
}

func runRunnersPause(inv clisurface.Invocation) error  { return setRunnerPaused(inv, true, os.Stdout) }
func runRunnersResume(inv clisurface.Invocation) error { return setRunnerPaused(inv, false, os.Stdout) }

func setRunnerPaused(inv clisurface.Invocation, paused bool, out io.Writer) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	var result runnerPauseResult
	var err error
	serviceUser := inv.Value("service-user")
	if inv.Has("service-user") {
		result, err = setServiceUserPaused(context.Background(), runnerServiceGOOS, serviceUser, paused)
	} else {
		var path string
		if path, err = agentConfigPath(inv.Value("config")); err == nil {
			result, err = setConfigPaused(path, paused)
		}
	}
	if err != nil {
		return err
	}
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(result)
	case "plain":
		_, err := fmt.Fprintln(out, opsview.DisplayText(result.Runner))
		return err
	}
	shown, resume := opsview.DisplayText(result.Runner), "sparkwing runners resume"
	if serviceUser != "" {
		resume += " --service-user " + opsview.DisplayText(serviceUser)
	}
	switch {
	case paused && result.Changed:
		_, err = fmt.Fprintf(out, "Paused runner %s on this machine: it finishes held jobs and claims no new work.\nThe pause is local to this machine; other machines and the console see the runner idle. Resume with %s.\n", shown, resume)
	case paused:
		_, err = fmt.Fprintf(out, "Runner %s is already paused on this machine. Resume with %s.\n", shown, resume)
	case result.Changed:
		_, err = fmt.Fprintf(out, "Resumed runner %s: it claims work again on its next poll.\n", shown)
	default:
		_, err = fmt.Fprintf(out, "Runner %s is not paused.\n", shown)
	}
	return err
}

func setConfigPaused(path string, paused bool) (runnerPauseResult, error) {
	name, credentials, err := pausableRunner(path)
	if err != nil {
		return runnerPauseResult{}, err
	}
	changed, err := runners.SetPaused(credentials, paused)
	if err != nil {
		return runnerPauseResult{}, err
	}
	return runnerPauseResult{Runner: name, Paused: paused, Changed: changed}, nil
}

// safety: the pause marker sits in the account's credentials directory, so
// root only relays the request and the account writes its own file.
func setServiceUserPaused(ctx context.Context, goos, name string, paused bool) (runnerPauseResult, error) {
	if err := runnerservice.ValidAccountName(name); err != nil {
		return runnerPauseResult{}, err
	}
	verb := "pause"
	if !paused {
		verb = "resume"
	}
	if serviceUserEUID() != 0 {
		if err := serviceUserPrivilege.Check(ctx); err != nil {
			return runnerPauseResult{}, err
		}
		out, err := serviceUserPrivilege.Run(ctx, []string{"runners", verb, "--service-user", name, "--output", "json"}, nil)
		if err != nil {
			return runnerPauseResult{}, err
		}
		var result runnerPauseResult
		if err := json.Unmarshal(out, &result); err != nil {
			return runnerPauseResult{}, fmt.Errorf("read the privileged %s result: %w", verb, err)
		}
		return result, nil
	}
	if goos != "linux" && goos != "darwin" {
		return runnerPauseResult{}, fmt.Errorf("runners %s --service-user supports Linux and macOS, not %s", verb, goos)
	}
	account, found, err := runnerservice.LookupAccount(ctx, serviceUserAccountHost(goos), name)
	if err != nil {
		return runnerPauseResult{}, err
	}
	if !found {
		return runnerPauseResult{}, fmt.Errorf("no OS user %s holds a runner; sparkwing runners add --service-user %s enrolls one: %w", name, name, os.ErrNotExist)
	}
	self, _, cleanup, err := serviceUserStage(goos, "")
	if err != nil {
		return runnerPauseResult{}, err
	}
	defer cleanup()
	args := []string{self, "__runner-account-pause"}
	if !paused {
		args = append(args, "--resume")
	}
	out, err := serviceUserRunAs(ctx, account, args, nil)
	if err != nil {
		return runnerPauseResult{}, err
	}
	var result runnerPauseResult
	if err := json.Unmarshal(out, &result); err != nil {
		return runnerPauseResult{}, fmt.Errorf("read the %s result of %s: %w", verb, name, err)
	}
	return result, nil
}

func runRunnerAccountPause(inv clisurface.Invocation) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	result, err := setConfigPaused(filepath.Join(fssecure.ConfigDirIn(home), userconfig.Filename), !inv.Bool("resume"))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func pausableRunner(path string) (name, credentials string, err error) {
	cfg, err := userconfig.ReadConfig(path)
	if err != nil {
		return "", "", err
	}
	if cfg.Runner == nil {
		return "", "", fmt.Errorf("%s has no runner section; sparkwing runners add enrolls this machine: %w", path, os.ErrNotExist)
	}
	if cfg.Runner.HolderPrefix != nil {
		name = *cfg.Runner.HolderPrefix
	}
	if cfg.Runner.CredentialsDir == nil || strings.TrimSpace(*cfg.Runner.CredentialsDir) == "" {
		return "", "", fmt.Errorf("%s: runner.credentials_dir is required", path)
	}
	return name, *cfg.Runner.CredentialsDir, nil
}
