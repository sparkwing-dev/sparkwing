package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
)

type ConfigureInit struct {
	ConfigDir   string                 `json:"config_dir"`
	Created     bool                   `json:"created"`
	Mode        string                 `json:"mode,omitempty"`
	Exposed     bool                   `json:"exposed,omitempty"`
	ConfigFiles []ConfigureInitFile    `json:"config_files"`
	Toolchain   ConfigureInitToolchain `json:"toolchain"`
	NextSteps   []InfoNextStep         `json:"next_steps"`
}

type ConfigureInitFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Mode    string `json:"mode,omitempty"`
	Exposed bool   `json:"exposed,omitempty"`

	Summary string `json:"summary,omitempty"`
}

type ConfigureInitToolchain struct {
	CLIVersion string `json:"cli_version"`
	CLIPath    string `json:"cli_path,omitempty"`
	GoVersion  string `json:"go_version,omitempty"`
	GoOnPath   bool   `json:"go_on_path"`
}

func runConfigureInit(args []string) error {
	fs := flag.NewFlagSet(cmdConfigureInit.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "", "output format: pretty | json | plain (default: table)")
	dryRun := fs.Bool("dry-run", false, "probe + report without creating or tightening ~/.config/sparkwing/")
	if err := parseAndCheck(cmdConfigureInit, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		PrintHelp(cmdConfigureInit, os.Stderr)
		return fmt.Errorf("configure init: unexpected positional %q", fs.Arg(0))
	}
	format, err := resolveOutputFormat(*output, cmdConfigureInit.Path)
	if err != nil {
		return err
	}

	info, err := gatherConfigureInit(*dryRun)
	if err != nil {
		return err
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	case "plain":
		for _, ns := range info.NextSteps {
			fmt.Println(ns.Command)
		}
		return nil
	default:
		printConfigureInitTable(info)
		return nil
	}
}

func gatherConfigureInit(dryRun bool) (ConfigureInit, error) {
	out := ConfigureInit{}

	profilesPath, err := profile.DefaultPath()
	if err != nil {
		return out, fmt.Errorf("configure init: resolve config dir: %w", err)
	}
	configDir := filepath.Dir(profilesPath)
	out.ConfigDir = configDir

	existed := dirExists(configDir)
	if !dryRun {
		if err := fssecure.EnsureConfigDir(configDir); err != nil {
			return out, fmt.Errorf("configure init: prepare %s: %w", configDir, err)
		}
		out.Created = !existed
	}
	out.Mode, out.Exposed = pathExposure(configDir)

	out.ConfigFiles = surveyConfigFiles(configDir, profilesPath)
	out.Toolchain = probeToolchain()
	out.NextSteps = configureInitNextSteps()
	return out, nil
}

func surveyConfigFiles(configDir, profilesPath string) []ConfigureInitFile {
	reposPath, _ := repos.DefaultPath()
	secretsEnvPath := filepath.Join(configDir, "secrets.env")

	files := []ConfigureInitFile{
		{Name: "profiles.yaml", Path: profilesPath, Summary: profileSummary(profilesPath)},
		{Name: "repos.yaml", Path: reposPath, Summary: repoSummary(reposPath)},
		{Name: "secrets.env", Path: secretsEnvPath, Summary: "laptop-local masked secrets"},
	}
	for i := range files {
		_, err := os.Stat(files[i].Path)
		files[i].Present = err == nil
		if files[i].Present {
			files[i].Mode, files[i].Exposed = pathExposure(files[i].Path)
		}
	}
	return files
}

func pathExposure(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	perm := info.Mode().Perm()
	return fmt.Sprintf("%04o", perm), perm&0o077 != 0
}

func profileSummary(path string) string {
	cfg, err := profile.Load(path)
	if err != nil || cfg == nil {
		return "remote-cluster profiles for `--profile <name>` dispatch"
	}
	n := len(cfg.Profiles)
	if n == 0 {
		return "0 profiles defined"
	}
	if n == 1 {
		return "1 profile defined"
	}
	return fmt.Sprintf("%d profiles defined", n)
}

func repoSummary(path string) string {
	cfg, err := repos.Load(path)
	if err != nil {
		return "repository registry is unreadable"
	}
	if cfg == nil {
		return "registered laptop checkouts for cross-repo pipeline lookup"
	}
	n := len(cfg.Repos)
	if n == 0 {
		return "0 repos registered"
	}
	if n == 1 {
		return "1 repo registered"
	}
	return fmt.Sprintf("%d repos registered", n)
}

func printConfigureInitExposure(info ConfigureInit) {
	var exposed []string
	if info.Exposed {
		exposed = append(exposed, fmt.Sprintf("  ! %s is readable by group or other users; run: chmod 700 %s", info.ConfigDir, info.ConfigDir))
	}
	for _, f := range info.ConfigFiles {
		if f.Exposed {
			exposed = append(exposed, fmt.Sprintf("  ! %s is readable by group or other users; run: chmod 600 %s", f.Name, f.Path))
		}
	}
	if len(exposed) == 0 {
		return
	}
	fmt.Println()
	for _, line := range exposed {
		fmt.Println(line)
	}
}

func probeToolchain() ConfigureInitToolchain {
	tc := ConfigureInitToolchain{
		CLIVersion: installedVersion(),
	}
	if path, err := os.Executable(); err == nil {
		tc.CLIPath = path
	} else if path, err := exec.LookPath("sparkwing"); err == nil {
		tc.CLIPath = path
	}
	if v := userGoVersion(); v != "" {
		tc.GoVersion = v
		tc.GoOnPath = true
	}
	return tc
}

func configureInitNextSteps() []InfoNextStep {
	return []InfoNextStep{
		{Command: "cd <repo> && sparkwing pipeline new --name release", Purpose: "auto-bootstrap .sparkwing/ + scaffold a single-node pipeline"},
		{Command: "sparkwing configure profiles", Purpose: "manage remote-cluster profiles for `--profile <name>`"},
		{Command: "sparkwing info", Purpose: "current project + tooling cheat sheet"},
	}
}

func printConfigureInitTable(info ConfigureInit) {
	fmt.Printf("Laptop config root: %s/", info.ConfigDir)
	if info.Mode != "" {
		fmt.Printf("  mode %s", info.Mode)
	}
	if info.Created {
		fmt.Printf("  (created)")
	}
	fmt.Println()
	fmt.Println()

	fmt.Println("CONFIG FILES")
	nameWidth := 0
	for _, f := range info.ConfigFiles {
		if n := len(f.Name); n > nameWidth {
			nameWidth = n
		}
	}
	for _, f := range info.ConfigFiles {
		state := "absent "
		mode := "    "
		if f.Present {
			state = "present"
			mode = f.Mode
		}
		fmt.Printf("  %-*s  [%s]  %s  %s\n", nameWidth, f.Name, state, mode, f.Summary)
	}
	printConfigureInitExposure(info)
	fmt.Println()

	fmt.Println("TOOLCHAIN")
	fmt.Printf("  sparkwing:  %s", info.Toolchain.CLIVersion)
	if info.Toolchain.CLIPath != "" {
		fmt.Printf("  (%s)", info.Toolchain.CLIPath)
	}
	fmt.Println()
	if info.Toolchain.GoOnPath {
		fmt.Printf("  go:         %s on PATH\n", info.Toolchain.GoVersion)
	} else {
		fmt.Printf("  go:         not found on PATH\n")
		fmt.Printf("              %s\n", goInstallHintForce())
	}
	fmt.Println()

	fmt.Println("NEXT STEPS")
	width := 0
	for _, ns := range info.NextSteps {
		if n := len(ns.Command); n > width {
			width = n
		}
	}
	for _, ns := range info.NextSteps {
		fmt.Printf("  %-*s  %s\n", width, ns.Command, ns.Purpose)
	}
}
