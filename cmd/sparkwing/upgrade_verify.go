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
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type upgradeVerifier struct {
	environment []string
	install     func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error)
	buildEnv    func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error)
	exec        func(context.Context, string, procenv.Env, string, ...string) ([]byte, error)
}

func (v *upgradeVerifier) Verify(ctx context.Context, checkout string, selected engine.Resolved) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	sdk := selected.Identity.Pin.SDK
	if sdk != engineid.GoSDK && sdk != engineid.TypeScriptSDK {
		return errors.New("upgrade: selected engine has no supported SDK")
	}
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
	masker := secrets.NewMasker()
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	base := procenv.Base{Local: true, Captured: v.environment, Masker: masker}
	installEnv, err := v.buildEnv(procenv.Install, base, procenv.Runtime{})
	if err != nil {
		return err
	}
	install := v.install
	if install == nil {
		install = func(ctx context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			return selected.InstallSDK(ctx, request)
		}
	}
	command, err := install(ctx, selected, engine.SDKInstallRequest{Directory: checkout, PipelineDir: ".sparkwing", SDK: sdk, Environment: slices.Collect(installEnv.Values())})
	defer func() { resultErr = errors.Join(resultErr, command.Close()) }()
	if err := errors.Join(err, ctx.Err()); err != nil {
		return fmt.Errorf("upgrade: install SDK: %w", err)
	}
	if command.Directory != dir || len(command.Command) == 0 || !filepath.IsAbs(command.Command[0]) {
		return errors.New("upgrade: selected installer returned an invalid SDK command")
	}
	if err := v.checkTypes(ctx, dir, installEnv, sdk); err != nil {
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

func (v *upgradeVerifier) checkTypes(ctx context.Context, dir string, env procenv.Env, sdk engineid.SDKKind) error {
	var command []string
	switch sdk {
	case engineid.GoSDK:
		// safety: a parent workspace must not substitute another checkout's SDK pin.
		var err error
		env, err = env.GoTool("GOWORK=off")
		if err != nil {
			return err
		}
		command = []string{"go", "build", "-mod=readonly", "-o", os.DevNull, "./..."}
	case engineid.TypeScriptSDK:
		command = []string{"npm", "exec", "--offline", "--no", "--", "tsc", "--noEmit"}
	default:
		return fmt.Errorf("upgrade: unsupported SDK %q", sdk)
	}
	if _, err := v.exec(ctx, dir, env, command[0], command[1:]...); err != nil {
		return fmt.Errorf("upgrade: %s: %w", strings.Join(command, " "), err)
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

func executeUpgradeCheck(ctx context.Context, dir string, env procenv.Env, program string, args ...string) ([]byte, error) {
	var command *exec.Cmd
	if program == "npm" {
		var err error
		command, err = pipelineentry.NPMCommand(ctx, env, runtime.GOOS, exec.LookPath, args...)
		if err != nil {
			return nil, err
		}
	} else {
		// #nosec G204 -- native tool argv or the resolver's verified executable, never a shell.
		command = procenv.CommandContext(ctx, env, program, args...)
	}
	command.Dir = dir
	var out, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostics
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()+"\n"+diagnostics.String()))
	}
	return out.Bytes(), nil
}
