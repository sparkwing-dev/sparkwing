package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runRunnersPause(inv clisurface.Invocation) error  { return setRunnerPaused(inv, true, os.Stdout) }
func runRunnersResume(inv clisurface.Invocation) error { return setRunnerPaused(inv, false, os.Stdout) }

func setRunnerPaused(inv clisurface.Invocation, paused bool, out io.Writer) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	path, err := agentConfigPath(inv.Value("config"))
	if err != nil {
		return err
	}
	name, credentials, err := pausableRunner(path)
	if err != nil {
		return err
	}
	changed, err := runners.SetPaused(credentials, paused)
	if err != nil {
		return err
	}
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(struct {
			Runner  string `json:"runner"`
			Paused  bool   `json:"paused"`
			Changed bool   `json:"changed"`
		}{name, paused, changed})
	case "plain":
		_, err := fmt.Fprintln(out, opsview.DisplayText(name))
		return err
	}
	shown := opsview.DisplayText(name)
	switch {
	case paused && changed:
		_, err = fmt.Fprintf(out, "Paused runner %s on this machine: it finishes held jobs and claims no new work.\nThe pause is local to this machine; other machines and the console see the runner idle. Resume with sparkwing runners resume.\n", shown)
	case paused:
		_, err = fmt.Fprintf(out, "Runner %s is already paused on this machine. Resume with sparkwing runners resume.\n", shown)
	case changed:
		_, err = fmt.Fprintf(out, "Resumed runner %s: it claims work again on its next poll.\n", shown)
	default:
		_, err = fmt.Fprintf(out, "Runner %s is not paused.\n", shown)
	}
	return err
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
