package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type upgradeVerifier struct {
	environment []string
	buildEnv    func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
	exec        func(context.Context, string, []string, string, ...string) ([]byte, error)
}

func (v *upgradeVerifier) Verify(ctx context.Context, checkout string, selected engine.Resolved) error {
	if !filepath.IsAbs(selected.Executable) {
		return errors.New("upgrade: verified engine executable must be an absolute path")
	}
	dir, err := findSparkwingDirFrom(checkout)
	if err != nil {
		return err
	}
	if dir != filepath.Join(checkout, ".sparkwing") {
		return errors.New("upgrade: staged checkout contains no pipeline")
	}
	base := procenv.Base{Local: true, Captured: v.environment}
	installEnv, err := v.buildEnv(procenv.Install, base, procenv.Runtime{})
	if err != nil {
		return err
	}
	if err := v.checkDependencies(ctx, dir, installEnv, selected.Identity.Pin.SDK); err != nil {
		return err
	}
	hostEnv, err := v.buildEnv(procenv.HostChild, base, procenv.Runtime{})
	if err != nil {
		return err
	}
	// safety: every SDK operation stays in the staged checkout under the selected engine.
	raw, err := v.exec(ctx, checkout, hostEnv, selected.Executable, "pipeline", "list", "--all", "--output=json")
	if err != nil {
		return fmt.Errorf("upgrade: list pipelines: %w", err)
	}
	names, err := upgradePipelineNames(raw)
	if err != nil {
		return err
	}
	for _, name := range names {
		for _, operation := range []string{"describe", "plan"} {
			if _, err := v.exec(ctx, checkout, hostEnv, selected.Executable, "pipeline", operation, "--pipeline="+name, "--output=json"); err != nil {
				return fmt.Errorf("upgrade: %s pipeline %q: %w", operation, name, err)
			}
		}
	}
	return nil
}

func (v *upgradeVerifier) checkDependencies(ctx context.Context, dir string, env []string, sdk engine.SDKKind) error {
	var commands [][]string
	switch sdk {
	case engine.GoSDK:
		// safety: a parent workspace must not substitute another checkout's SDK pin.
		env = setEnv(env, "GOWORK", "off")
		commands = [][]string{{"go", "mod", "download"}, {"go", "build", "-mod=readonly", "-o", os.DevNull, "./..."}}
	case engine.TypeScriptSDK:
		commands = [][]string{{"npm", "ci", "--no-audit", "--no-fund"}, {"npm", "exec", "--offline", "--no", "--", "tsc", "--noEmit"}}
	default:
		return fmt.Errorf("upgrade: unsupported SDK %q", sdk)
	}
	for _, command := range commands {
		if _, err := v.exec(ctx, dir, env, command[0], command[1:]...); err != nil {
			return fmt.Errorf("upgrade: %s: %w", strings.Join(command, " "), err)
		}
	}
	return nil
}

func upgradePipelineNames(raw []byte) ([]string, error) {
	names := []string{}
	seen := map[string]bool{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var pipeline struct {
			Name string `json:"name"`
		}
		err := decoder.Decode(&pipeline)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("upgrade: decode pipeline list: %w", err)
		}
		if pipeline.Name == "" || seen[pipeline.Name] {
			return nil, errors.New("upgrade: engine returned an empty or duplicate pipeline name")
		}
		seen[pipeline.Name] = true
		names = append(names, pipeline.Name)
	}
	if len(names) == 0 {
		return nil, errors.New("upgrade: engine returned no pipelines to verify")
	}
	return names, nil
}

func executeUpgradeCheck(ctx context.Context, dir string, env []string, program string, args ...string) ([]byte, error) {
	var command *exec.Cmd
	if program == "npm" {
		var err error
		command, err = pipelineentry.NPMCommand(ctx, runtime.GOOS, exec.LookPath, args...)
		if err != nil {
			return nil, err
		}
	} else {
		// #nosec G204 -- native tool argv or the resolver's verified executable, never a shell.
		command = exec.CommandContext(ctx, program, args...)
	}
	command.Dir, command.Env = dir, env
	var out, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostics
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()+"\n"+diagnostics.String()))
	}
	return out.Bytes(), nil
}
