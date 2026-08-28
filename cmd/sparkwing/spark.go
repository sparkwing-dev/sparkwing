package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/modfile"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func defaultSparkwingDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ".sparkwing"
	}
	return filepath.Join(cwd, ".sparkwing")
}

func runSparks(args []string) error {
	if handleParentHelp(cmdSparks, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdSparks, os.Stderr)
		return errors.New("spark: subcommand required (list|lint|resolve|update|add|remove|warmup|vendor)")
	}
	switch args[0] {
	case "list", "ls":
		return runSparksList(args[1:])
	case "lint":
		return runSparksLint(args[1:])
	case "resolve":
		return runSparksResolve(args[1:])
	case "update":
		return runSparksUpdate(args[1:])
	case "add":
		return runSparksAdd(args[1:])
	case "remove", "rm":
		return runSparksRemove(args[1:])
	case "warmup":
		return runSparksWarmup(args[1:])
	case "inflate":
		return runSparksInflate(args[1:])
	default:
		PrintHelp(cmdSparks, os.Stderr)
		return fmt.Errorf("spark: unknown subcommand %q", args[0])
	}
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

func runSparksList(args []string) error {
	fs := flag.NewFlagSet(cmdSparksList.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: table)")
	noResolve := fs.Bool("no-resolve", false, "skip module-proxy lookups; only print declared versions")
	if err := parseAndCheck(cmdSparksList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*outFmt, "spark list")
	if err != nil {
		return err
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
	}
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil {
		return err
	}
	entries := []sparkListEntry{}
	if m != nil {
		ctx := context.Background()
		for _, lib := range m.Libraries {
			e := sparkListEntry{Name: lib.Name, Source: lib.Source, Declared: lib.Version}
			if !*noResolve {
				resolved, rerr := sparks.Resolve(ctx, &sparks.Manifest{Libraries: []sparks.Library{lib}})
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

func runSparksLint(args []string) error {
	fs := flag.NewFlagSet(cmdSparksLint.Path, flag.ContinueOnError)
	pathFlag := fs.String("path", ".", "path to a sparks library or its parent dir")
	if err := parseAndCheck(cmdSparksLint, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	target := *pathFlag
	if rest := fs.Args(); len(rest) > 0 {
		if len(rest) > 1 {
			return fmt.Errorf("spark lint: unexpected positional %q (lint takes one path)", rest[1])
		}
		if fs.Changed("path") {
			return fmt.Errorf("spark lint: --path %s and positional %q both given (pass one)", *pathFlag, rest[0])
		}
		target = rest[0]
	}
	libDir, manifestPath, err := resolveSparkJSONPath(target)
	if err != nil {
		return err
	}
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
	fmt.Fprintf(os.Stdout, "ok: %s (%d %s%s)\n",
		manifestPath, len(entries), strings.TrimSuffix(field, "s"), pluralS(len(entries)))
	return nil
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

func resolveSparkJSONPath(target string) (libDir, manifestPath string, err error) {
	info, err := os.Stat(target)
	if err != nil {
		return "", "", fmt.Errorf("spark lint: %s: %w", target, err)
	}
	if info.IsDir() {
		manifestPath = filepath.Join(target, "spark.json")
		if _, err := os.Stat(manifestPath); err != nil {
			return "", "", fmt.Errorf("spark lint: %s has no spark.json", target)
		}
		return target, manifestPath, nil
	}
	return filepath.Dir(target), target, nil
}

func runSparksResolve(args []string) error {
	fs := flag.NewFlagSet(cmdSparksResolve.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	quiet := fs.BoolP("quiet", "q", false, "suppress progress output; print only changes")
	if err := parseAndCheck(cmdSparksResolve, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
	}
	ctx := context.Background()
	changed, err := sparksResolveAndWrite(ctx, sparkwingDir)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(os.Stdout, "overlay written: %s\n",
			filepath.Join(sparkwingDir, sparks.OverlayModfileName))
		return nil
	}
	if !*quiet {
		fmt.Fprintln(os.Stdout, "up-to-date (no overlay changes)")
	}
	return nil
}

func runSparksUpdate(args []string) error {
	fs := flag.NewFlagSet(cmdSparksUpdate.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	name := fs.String("name", "", "restrict update to a single library (by name or source)")
	if err := parseAndCheck(cmdSparksUpdate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("spark update: unexpected positional %q (use --name)", rest[0])
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
	}
	only := *name
	m, path, err := loadManifestForWrite(sparkwingDir)
	if err != nil {
		return err
	}
	if len(m.Libraries) == 0 {
		return fmt.Errorf("spark update: %s has no libraries", path)
	}
	if only != "" {
		found := false
		for _, lib := range m.Libraries {
			if lib.Name == only || lib.Source == only {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("spark update: no library named %q in %s", only, path)
		}
	}
	ctx := context.Background()
	changed, err := sparksResolveAndWrite(ctx, sparkwingDir)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(os.Stdout, "overlay updated: %s\n",
			filepath.Join(sparkwingDir, sparks.OverlayModfileName))
	} else {
		fmt.Fprintln(os.Stdout, "up-to-date (no overlay changes)")
	}
	return nil
}

func runSparksAdd(args []string) error {
	fs := flag.NewFlagSet(cmdSparksAdd.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	sourceFlag := fs.String("source", "", "library source path (e.g. github.com/user/lib)")
	version := fs.String("version", "latest", "declared version ('latest', exact tag, or semver range)")
	name := fs.String("name", "", "short library name (default: last path segment of --source)")
	if err := parseAndCheck(cmdSparksAdd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("spark add: unexpected positional %q (use --source)", rest[0])
	}
	source := strings.TrimSpace(*sourceFlag)
	if source == "" {
		return errors.New("spark add: --source is required (e.g. --source github.com/user/lib)")
	}
	libName := *name
	if libName == "" {
		libName = filepath.Base(source)
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
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
		Name: libName, Source: source, Version: *version,
	})
	if err := writeSparksYAML(path, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "added %s (%s@%s) to %s\n", libName, source, *version, path)
	return nil
}

func runSparksRemove(args []string) error {
	fs := flag.NewFlagSet(cmdSparksRemove.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	nameFlag := fs.String("name", "", "library name (or source) to remove")
	if err := parseAndCheck(cmdSparksRemove, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("spark remove: unexpected positional %q (use --name)", rest[0])
	}
	target := strings.TrimSpace(*nameFlag)
	if target == "" {
		return errors.New("spark remove: --name is required")
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
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
	fmt.Fprintf(os.Stdout, "removed %s (%s) from %s\n", removed.Name, removed.Source, path)
	return nil
}

func runSparksWarmup(args []string) error {
	fs := flag.NewFlagSet(cmdSparksWarmup.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	clearCache := fs.Bool("clear-cache", false, "delete the local pipeline binary cache before compiling")
	if err := parseAndCheck(cmdSparksWarmup, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
	}

	ctx := context.Background()
	if _, err := sparksResolveAndWrite(ctx, sparkwingDir); err != nil {
		return fmt.Errorf("spark warmup: resolve: %w", err)
	}

	if *clearCache {
		result, err := bincache.PruneToLimits(ctx, 0, 0, true)
		if err != nil {
			return fmt.Errorf("spark warmup: clear cache: %w", err)
		}
		fmt.Fprintf(os.Stdout, "cache pruning removed %d bytes across %d entries\n",
			result.LogicalRemovedBytes, result.ReclaimedEntries)
	}

	_, cfg, err := projectconfig.DiscoverPipelines(sparkwingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: no pipelines discovered: %v\n", err)
	} else {
		fmt.Fprintf(os.Stdout, "warming up %d pipeline(s)\n", len(cfg.Pipelines))
	}

	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return fmt.Errorf("spark warmup: hash pipeline: %w", err)
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return fmt.Errorf("spark warmup: cache entry: %w", err)
	}
	lease, published, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		fmt.Fprintf(os.Stdout, "compiling %s\n", sparkwingDir)
		return bincache.CompilePipeline(sparkwingDir, tempPath)
	})
	if err != nil {
		return fmt.Errorf("spark warmup: %w", err)
	}
	defer func() { _ = lease.Release() }()
	if !published {
		fmt.Fprintf(os.Stdout, "binary already cached: %s\n", lease.Path())
	}

	if gcURL := bincache.CacheURL(); gcURL != "" {
		if err := bincache.UploadBinary(gcURL, bincache.CacheToken(), key, lease.Path()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: gitcache upload failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stdout, "uploaded to %s/bin/%s\n", gcURL, key)
		}
	} else {
		fmt.Fprintln(os.Stdout, "SPARKWING_GITCACHE_URL not set; skipping upload")
	}
	return nil
}

const sparksCoreModulePrefix = "github.com/sparkwing-dev/sparks-core/"

func resolveVendorModulePath(module string) string {
	module = strings.TrimSpace(module)
	if strings.Contains(module, "/") {
		return module
	}
	return sparksCoreModulePrefix + module
}

func runSparksInflate(args []string) error {
	fs := flag.NewFlagSet(cmdSparksInflate.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	moduleFlag := fs.String("module", "", "block module to vendor: a sparks-core name (e.g. templates) or a full module path")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json (default: pretty)")
	if err := parseAndCheck(cmdSparksInflate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("spark vendor: unexpected positional %q (use --module)", rest[0])
	}
	module := strings.TrimSpace(*moduleFlag)
	if module == "" {
		return errors.New("spark vendor: --module is required (e.g. --module templates)")
	}
	format, err := resolveOutputFormat(*outFmt, "spark vendor")
	if err != nil {
		return err
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
	}
	modulePath := resolveVendorModulePath(module)

	res, err := sparks.Vendor(context.Background(), sparkwingDir, modulePath)
	if err != nil {
		return err
	}

	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"module":  res.ModulePath,
			"version": res.Version,
			"dest":    res.Dest,
			"replace": res.RelReplace,
		})
	}

	fmt.Printf("vendored %s@%s\n", res.ModulePath, res.Version)
	fmt.Printf("  source copied to %s\n", res.Dest)
	fmt.Printf("  added `replace %s => %s` to %s and ran go mod tidy\n",
		res.ModulePath, res.RelReplace, filepath.Join(sparkwingDir, "go.mod"))
	fmt.Println()
	fmt.Println("your imports are unchanged -- the code now lives in your repo and is editable.")
	fmt.Printf("to undo: delete %s and drop the replace directive from %s.\n",
		res.Dest, filepath.Join(sparkwingDir, "go.mod"))
	return nil
}

func loadManifestForWrite(sparkwingDir string) (*sparks.Manifest, string, error) {
	if sparkwingDir == "" {
		return nil, "", errors.New("sparkwing-dir must not be empty")
	}
	if info, err := os.Stat(sparkwingDir); err != nil {
		return nil, "", fmt.Errorf("sparkwing-dir %s: %w", sparkwingDir, err)
	} else if !info.IsDir() {
		return nil, "", fmt.Errorf("sparkwing-dir %s is not a directory", sparkwingDir)
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
