// `sparkwing configure init` is the laptop-level setup + status
// command. Pairs with the per-project flow: `sparkwing pipeline new`
// scaffolds .sparkwing/ in your repo, configure init prepares
// ~/.config/sparkwing/ + reports what's already there. Idempotent --
// running it on a fresh laptop creates the dir; running again is a
// status report.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/profile"
	"github.com/sparkwing-dev/sparkwing/repos"
)

// ConfigureInit is the JSON shape of `sparkwing configure init -o
// json`. Stable contract: agents parse this directly. Field renames
// are breaking changes.
type ConfigureInit struct {
	ConfigDir   string                 `json:"config_dir"`
	Created     bool                   `json:"created"`
	ConfigFiles []ConfigureInitFile    `json:"config_files"`
	Toolchain   ConfigureInitToolchain `json:"toolchain"`
	NextSteps   []InfoNextStep         `json:"next_steps"`
}

type ConfigureInitFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	// Summary is a one-line human description: "0 profiles", "3
	// repos", etc. Empty when the file is absent or the package
	// doesn't expose a count.
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
	output := fs.StringP("output", "o", "", "output format: table | json | plain (default: table)")
	asJSON := fs.Bool("json", false, "alias for --output json")
	dryRun := fs.Bool("dry-run", false, "probe + report without creating ~/.config/sparkwing/")
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
	format, err := resolveOutputFormat(*output, *asJSON, cmdConfigureInit.Path)
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
		// One next-step command per line, same convention as `sparkwing
		// info -o plain` -- pipe to head -n1 for "what should I do
		// next?" in shell wrappers.
		for _, ns := range info.NextSteps {
			fmt.Println(ns.Command)
		}
		return nil
	default:
		printConfigureInitTable(info)
		return nil
	}
}

// gatherConfigureInit performs the side effect (mkdir on the config
// dir) when not in dry-run, then probes everything. Errors from any
// best-effort probe (file existence, version lookups) are absorbed
// into "not present" / empty fields so the command always returns a
// useful report.
func gatherConfigureInit(dryRun bool) (ConfigureInit, error) {
	out := ConfigureInit{}

	// Resolve the config dir via profile.DefaultPath()'s parent, so
	// any future relocation (XDG override, env var) flows through one
	// helper. profile + repos use the same conventions.
	profilesPath, err := profile.DefaultPath()
	if err != nil {
		return out, fmt.Errorf("configure init: resolve config dir: %w", err)
	}
	configDir := filepath.Dir(profilesPath)
	out.ConfigDir = configDir

	if !dirExists(configDir) {
		if !dryRun {
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				return out, fmt.Errorf("configure init: create %s: %w", configDir, err)
			}
			out.Created = true
		}
	}

	out.ConfigFiles = surveyConfigFiles(configDir, profilesPath)
	out.Toolchain = probeToolchain()
	out.NextSteps = configureInitNextSteps()
	return out, nil
}

// surveyConfigFiles walks the four files we know about today and
// reports presence + a one-line summary. Adding a new ~/.config/
// sparkwing/<file> requires an entry here -- intentional, so the
// status report stays curated rather than auto-listing every dotfile
// the user may have stashed there.
func surveyConfigFiles(configDir, profilesPath string) []ConfigureInitFile {
	reposPath, _ := repos.DefaultPath()
	configYAMLPath := filepath.Join(configDir, "config.yaml")
	secretsEnvPath := filepath.Join(configDir, "secrets.env")

	files := []ConfigureInitFile{
		{Name: "profiles.yaml", Path: profilesPath, Summary: profileSummary(profilesPath)},
		{Name: "repos.yaml", Path: reposPath, Summary: repoSummary(reposPath)},
		{Name: "config.yaml", Path: configYAMLPath, Summary: "wing-flag presets (sparkwing.wingconfig)"},
		{Name: "secrets.env", Path: secretsEnvPath, Summary: "laptop-local masked secrets"},
	}
	for i := range files {
		_, err := os.Stat(files[i].Path)
		files[i].Present = err == nil
	}
	return files
}

// profileSummary returns a one-line count of configured profiles, or
// a generic placeholder when the file's missing/unreadable. Failures
// fall through silently -- this is a status command, not a parser.
func profileSummary(path string) string {
	cfg, err := profile.Load(path)
	if err != nil || cfg == nil {
		return "remote-cluster profiles for `--on <name>` dispatch"
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
	if err != nil || cfg == nil {
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
		{Command: "sparkwing configure profiles", Purpose: "manage remote-cluster profiles for `--on <name>`"},
		{Command: "sparkwing info", Purpose: "current project + tooling cheat sheet"},
	}
}

func printConfigureInitTable(info ConfigureInit) {
	if info.Created {
		fmt.Printf("Laptop config root: %s/  (created)\n", info.ConfigDir)
	} else {
		fmt.Printf("Laptop config root: %s/\n", info.ConfigDir)
	}
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
		if f.Present {
			state = "present"
		}
		fmt.Printf("  %-*s  [%s]  %s\n", nameWidth, f.Name, state, f.Summary)
	}
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
