package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"golang.org/x/mod/modfile"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// safety: -C may name a subdirectory, so the nearest project .sparkwing/
// upward is the module; with none, the caller refuses rather than writing a
// .sparkwing/ into the working directory or the runtime home.
func defaultSparkwingDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir, ok, err := nearestDotSparkwing(cwd)
	if err != nil {
		return "", err
	}
	if ok {
		return dir, nil
	}
	return "", fmt.Errorf("no .sparkwing/ project (sparkwing.yaml or go.mod) in %s or above it; run from a checkout or pass -C DIR", cwd)
}

type sparkListEntry struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Declared string `json:"declared"`
	Resolved string `json:"resolved,omitempty"`
	Error    string `json:"error,omitempty"`
}

type sparkListLine struct {
	Kind string `json:"kind"`
	sparkListEntry
}

func runSparksList(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	outFmt := inv.Output(color.IsInteractiveStdout())
	noResolve := inv.Bool("no-resolve")
	format, err := resolveOutputFormat(outFmt, "spark list")
	if err != nil {
		return err
	}
	sparkwingDir, err := defaultSparkwingDir()
	if err != nil {
		return err
	}
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil {
		return err
	}
	entries := []sparkListEntry{}
	if m != nil {
		ctx := gotoolchain.WithSession(context.Background(), nil, nil)
		var resolver *sparks.Resolver
		if !noResolve {
			resolver = sparks.NewResolverFromEnv(ctx)
			resolver.Dir = sparkwingDir
		}
		for _, lib := range m.Libraries {
			e := sparkListEntry{Name: lib.Name, Source: lib.Source, Declared: lib.Version}
			if !noResolve {
				resolved, rerr := resolver.Resolve(ctx, &sparks.Manifest{Libraries: []sparks.Library{lib}})
				if rerr != nil {
					e.Error = rerr.Error()
				} else {
					e.Resolved = resolved[lib.Source]
				}
			}
			entries = append(entries, e)
		}
	}
	switch format {
	case "json":

		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(map[string]any{
			"kind": "summary", "sparkwing_dir": sparkwingDir, "library_count": len(entries),
		}); err != nil {
			return err
		}
		for _, e := range entries {
			if err := enc.Encode(sparkListLine{Kind: "library", sparkListEntry: e}); err != nil {
				return err
			}
		}
		return nil
	case "plain":
		for _, e := range entries {
			ver := e.Resolved
			if ver == "" {
				ver = e.Declared
			}
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", e.Name, e.Source, ver)
		}
		return nil
	default:
		if m == nil {
			fmt.Fprintf(os.Stdout, "no sparks declared in %s/%s\n", sparkwingDir, projectconfig.Filename)
			return nil
		}
		if len(entries) == 0 {
			fmt.Fprintln(os.Stdout, "(no libraries declared)")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSOURCE\tDECLARED\tRESOLVED")
		for _, e := range entries {
			resolved := e.Resolved
			if resolved == "" {
				if e.Error != "" {
					resolved = "error: " + shortErr(e.Error)
				} else {
					resolved = "-"
				}
			}
			name := e.Name
			if name == "" {
				name = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, e.Source, e.Declared, resolved)
		}
		return tw.Flush()
	}
}

func shortErr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

type sparkManifest struct {
	Name          string                `json:"name"`
	Description   string                `json:"description"`
	Author        string                `json:"author"`
	Version       string                `json:"version"`
	SDKMinVersion string                `json:"sdk_min_version"`
	Stability     string                `json:"stability"`
	Packages      []sparkManifestEntry  `json:"packages"`
	Modules       []sparkManifestEntry  `json:"modules"`
	Dependencies  []sparkManifestDepRaw `json:"dependencies"`
}

type sparkManifestEntry struct {
	Path        string `json:"path"`
	Module      string `json:"module"`
	Description string `json:"description"`
	Stability   string `json:"stability"`
}

type sparkManifestDepRaw struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Version string `json:"version"`
}

func runSparksLint(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	pathFlag := inv.Value("path")
	if pathFlag == "" {
		pathFlag = "."
	}
	target := pathFlag
	libDir, manifestPath, err := resolveSparkJSONPath("spark lint", target)
	if err != nil {
		return err
	}
	// #nosec G703 -- lint reads the manifest path explicitly selected by the CLI operator
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("spark lint: read %s: %w", manifestPath, err)
	}
	var m sparkManifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			if err2 := json.Unmarshal(raw, &m); err2 == nil {
				fmt.Fprintf(os.Stderr, "warn: %s: %v\n", manifestPath, err)
			} else {
				return fmt.Errorf("spark lint: %s: invalid JSON: %w", manifestPath, err2)
			}
		} else {
			return fmt.Errorf("spark lint: %s: invalid JSON: %w", manifestPath, err)
		}
	}
	var problems []string
	if strings.TrimSpace(m.Name) == "" {
		problems = append(problems, "missing required field 'name'")
	}
	if strings.TrimSpace(m.Description) == "" {
		problems = append(problems, "missing required field 'description'")
	}
	if strings.TrimSpace(m.Author) == "" {
		problems = append(problems, "missing required field 'author'")
	}
	field, entries, shapeProblem := sparkManifestShape(m)
	if shapeProblem != "" {
		problems = append(problems, shapeProblem)
	}
	problems = append(problems, lintSparkEntries(libDir, field, entries)...)
	if m.Stability != "" && !validStability(m.Stability) {
		problems = append(problems, fmt.Sprintf(
			"stability must be experimental|beta|stable, got %q", m.Stability,
		))
	}
	for i, d := range m.Dependencies {
		if d.Source == "" {
			problems = append(problems, fmt.Sprintf("dependencies[%d]: 'source' is required", i))
		}
		if d.Version == "" {
			problems = append(problems, fmt.Sprintf(
				"dependencies[%d] (%s): 'version' is required", i, d.Source,
			))
		}
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "spark lint: %s: %d problem(s)\n", manifestPath, len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		return fmt.Errorf("spark lint: %d problem(s) in %s", len(problems), manifestPath)
	}
	switch inv.Output(color.IsInteractiveStdout()) {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(struct {
			Path    string `json:"path"`
			Entries int    `json:"entries"`
			Kind    string `json:"kind"`
		}{manifestPath, len(entries), field})
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, manifestPath)
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "ok: %s (%d %s%s)\n", manifestPath, len(entries), strings.TrimSuffix(field, "s"), pluralS(len(entries)))
	return err
}

func sparkManifestShape(m sparkManifest) (field string, entries []sparkManifestEntry, problem string) {
	switch {
	case len(m.Packages) > 0 && len(m.Modules) > 0:
		return "packages", nil, "declares both 'packages' and 'modules'; use exactly one " +
			"('packages' for a library that is one Go module, 'modules' for a monorepo of independently tagged modules)"
	case len(m.Modules) > 0:
		return "modules", m.Modules, ""
	case len(m.Packages) > 0:
		return "packages", m.Packages, ""
	default:
		return "packages", nil, "exactly one of 'packages' (a library that is one Go module) or " +
			"'modules' (a monorepo of independently tagged modules) must be a non-empty array"
	}
}

func lintSparkEntries(libDir, field string, entries []sparkManifestEntry) []string {
	wantModule := field == "modules"
	var problems []string
	seen := map[string]int{}
	for i, e := range entries {
		if strings.TrimSpace(e.Path) == "" {
			problems = append(problems, fmt.Sprintf("%s[%d]: 'path' is required", field, i))
		} else {
			abs := filepath.Join(libDir, e.Path)
			// #nosec G703 -- lint checks directory references in the manifest selected by the CLI operator
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				problems = append(problems, fmt.Sprintf(
					"%s[%d] (%s): directory %s does not exist", field, i, e.Path, abs,
				))
			} else if wantModule {
				problems = append(problems, lintEntryGoMod(abs, field, i, e)...)
			}
			if prev, ok := seen[e.Path]; ok {
				problems = append(problems, fmt.Sprintf(
					"%s[%d] (%s): duplicate path; first seen at %s[%d]",
					field, i, e.Path, field, prev,
				))
			}
			seen[e.Path] = i
		}
		switch {
		case wantModule && strings.TrimSpace(e.Module) == "":
			problems = append(problems, fmt.Sprintf(
				"%s[%d] (%s): 'module' is required (the Go module path this directory declares)",
				field, i, e.Path,
			))
		case !wantModule && strings.TrimSpace(e.Module) != "":
			problems = append(problems, fmt.Sprintf(
				"%s[%d] (%s): 'module' belongs to a 'modules' entry; a 'packages' entry shares the library's own module path",
				field, i, e.Path,
			))
		}
		if strings.TrimSpace(e.Description) == "" {
			problems = append(problems, fmt.Sprintf("%s[%d] (%s): 'description' is required", field, i, e.Path))
		}
		if e.Stability != "" && !validStability(e.Stability) {
			problems = append(problems, fmt.Sprintf(
				"%s[%d] (%s): stability must be experimental|beta|stable, got %q",
				field, i, e.Path, e.Stability,
			))
		}
	}
	return problems
}

func lintEntryGoMod(dir, field string, i int, e sparkManifestEntry) []string {
	declared := strings.TrimSpace(e.Module)
	if declared == "" {
		return nil
	}
	goModPath := filepath.Join(dir, "go.mod")
	// #nosec G703 -- lint reads module metadata referenced by the operator-selected manifest
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return nil
	}
	actual := modfile.ModulePath(data)
	if actual == "" {
		return []string{fmt.Sprintf(
			"%s[%d] (%s): %s has no module line", field, i, e.Path, goModPath,
		)}
	}
	if actual != declared {
		return []string{fmt.Sprintf(
			"%s[%d] (%s): 'module' is %q but %s declares %q",
			field, i, e.Path, declared, goModPath, actual,
		)}
	}
	return nil
}

func validStability(s string) bool {
	switch s {
	case "experimental", "beta", "stable":
		return true
	}
	return false
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func resolveSparkJSONPath(verb, target string) (libDir, manifestPath string, err error) {
	// #nosec G703 -- this CLI accepts a manifest or library directory explicitly selected by the operator
	info, err := os.Stat(target)
	if err != nil {
		return "", "", fmt.Errorf("%s: %s: %w", verb, target, err)
	}
	if info.IsDir() {
		manifestPath = filepath.Join(target, "spark.json")
		// #nosec G703 -- spark.json is read inside the library directory selected by the CLI operator
		if _, err := os.Stat(manifestPath); err != nil {
			return "", "", fmt.Errorf("%s: %s has no spark.json", verb, target)
		}
		return target, manifestPath, nil
	}
	return filepath.Dir(target), target, nil
}

func runSparksUpdate(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}

	sparkwingDir, err := defaultSparkwingDir()
	if err != nil {
		return err
	}
	m, path, err := loadManifestForWrite(sparkwingDir)
	if err != nil {
		return err
	}
	if len(m.Libraries) == 0 {
		return fmt.Errorf("spark update: %s has no libraries", path)
	}
	ctx := context.Background()
	changed, err := sparksResolveAndWrite(ctx, sparkwingDir)
	if err != nil {
		return err
	}
	path = filepath.Join(sparkwingDir, sparks.OverlayModfileName)
	switch inv.Output(color.IsInteractiveStdout()) {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(struct {
			Path    string `json:"path"`
			Changed bool   `json:"changed"`
		}{path, changed})
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, path)
		return err
	}
	if changed {
		_, err = fmt.Fprintln(os.Stdout, "overlay updated:", path)
	} else {
		_, err = fmt.Fprintln(os.Stdout, "up-to-date (no overlay changes)")
	}
	return err
}

func runSparksAdd(inv clisurface.Invocation) error {
	output := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, output)
	}
	source := strings.TrimSpace(inv.Value("source"))
	if source == "" {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--source must not be blank"}
	}
	version := inv.Value("version")
	if version == "" {
		version = "latest"
	}
	libName := inv.Value("spark")
	if libName == "" {
		libName = filepath.Base(source)
	}

	sparkwingDir, err := defaultSparkwingDir()
	if err != nil {
		return err
	}
	m, path, err := loadManifestForWrite(sparkwingDir)
	if err != nil {
		return err
	}
	for _, lib := range m.Libraries {
		if lib.Source == source || lib.Name == libName {
			return fmt.Errorf("spark add: %s already declares %s (%s@%s)",
				path, libName, lib.Source, lib.Version)
		}
	}
	m.Libraries = append(m.Libraries, sparks.Library{
		Name: libName, Source: source, Version: version,
	})
	if err := writeSparksYAML(path, m); err != nil {
		return err
	}
	return writeSparkMutation("added", m.Libraries[len(m.Libraries)-1], path, output)
}

func runSparksRemove(inv clisurface.Invocation) error {
	output := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, output)
	}
	target := strings.TrimSpace(inv.Value("spark"))
	if target == "" {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--spark must not be blank"}
	}

	sparkwingDir, err := defaultSparkwingDir()
	if err != nil {
		return err
	}
	m, path, err := loadManifestForWrite(sparkwingDir)
	if err != nil {
		return err
	}
	idx := -1
	for i, lib := range m.Libraries {
		if lib.Name == target || lib.Source == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("spark remove: %s has no library named %q", path, target)
	}
	removed := m.Libraries[idx]
	m.Libraries = append(m.Libraries[:idx], m.Libraries[idx+1:]...)
	if err := writeSparksYAML(path, m); err != nil {
		return err
	}
	return writeSparkMutation("removed", removed, path, output)
}

func writeSparkMutation(action string, library sparks.Library, path, output string) error {
	switch output {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(struct {
			Action  string `json:"action"`
			Spark   string `json:"spark"`
			Source  string `json:"source"`
			Version string `json:"version"`
			Path    string `json:"path"`
		}{action, library.Name, library.Source, library.Version, path})
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, library.Name)
		return err
	default:
		_, err := fmt.Fprintf(os.Stdout, "%s %s (%s@%s) in %s\n", action, library.Name, library.Source, library.Version, path)
		return err
	}
}

const sparksCoreModulePrefix = "github.com/sparkwing-dev/sparks-core/"

func resolveVendorModulePath(module string) string {
	module = strings.TrimSpace(module)
	if strings.Contains(module, "/") {
		return module
	}
	return sparksCoreModulePrefix + module
}

func runSparksInflate(inv clisurface.Invocation) error {
	return runSparksInflateCommand(context.Background(), inv, os.Stdout, defaultSparkwingDir, sparks.Vendor)
}

func runSparksInflateCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer,
	locate func() (string, error), vendor func(context.Context, string, string) (*sparks.VendorResult, error),
) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	module := strings.TrimSpace(inv.Value("library"))
	if module == "" {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--library is required; list choices with sparkwing pipeline sparks catalog"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sparkwingDir, err := locate()
	if err != nil {
		return err
	}
	modulePath := resolveVendorModulePath(module)

	res, err := vendor(ctx, sparkwingDir, modulePath)
	if err != nil {
		return err
	}

	if format == "json" {
		enc := json.NewEncoder(out)
		return enc.Encode(map[string]any{
			"module":  res.ModulePath,
			"version": res.Version,
			"dest":    res.Dest,
			"replace": res.RelReplace,
		})
	}
	if format == "plain" {
		_, err = fmt.Fprintln(out, res.Dest)
		return err
	}
	_, err = fmt.Fprintf(out, "vendored %s@%s\n  source copied to %s\n  added `replace %s => %s` to %s and ran go mod tidy\n\nImports now resolve to the editable local source.\nTo undo, delete %s and drop its replace directive.\n",
		res.ModulePath, res.Version, res.Dest, res.ModulePath, res.RelReplace, filepath.Join(sparkwingDir, "go.mod"), res.Dest)
	return err
}

func loadManifestForWrite(sparkwingDir string) (*sparks.Manifest, string, error) {
	if sparkwingDir == "" {
		return nil, "", errors.New(".sparkwing directory must not be empty")
	}
	if info, err := os.Stat(sparkwingDir); err != nil {
		return nil, "", fmt.Errorf("%s: %w (run from the repository root, or pass -C DIR)", sparkwingDir, err)
	} else if !info.IsDir() {
		return nil, "", fmt.Errorf("%s is not a directory", sparkwingDir)
	}
	path := filepath.Join(sparkwingDir, projectconfig.Filename)
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil {
		return nil, path, err
	}
	if m == nil {
		m = &sparks.Manifest{}
	}
	return m, path, nil
}

func sparksResolveAndWrite(ctx context.Context, sparkwingDir string) (bool, error) {
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil {
		return false, err
	}
	return sparks.ResolveAndWrite(ctx, sparkwingDir, m)
}

func writeSparksYAML(path string, m *sparks.Manifest) error {
	return projectconfig.WriteSparksSection(path, m.Libraries)
}
