// `sparkwing pipeline sparks` subcommand. Manages the consumer
// manifest .sparkwing/sparks.yaml and drives the resolver in
// internal/sparks.
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
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// defaultSparkwingDir resolves the --sparkwing-dir flag's default:
// the `.sparkwing/` child of the current working directory. We keep
// the resolution lazy so tests can chdir first.
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
		return errors.New("spark: subcommand required (list|lint|resolve|update|add|remove|warmup)")
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
	default:
		PrintHelp(cmdSparks, os.Stderr)
		return fmt.Errorf("spark: unknown subcommand %q", args[0])
	}
}

// ---- list ------------------------------------------------------

// sparkListEntry is the per-library shape we render for `spark list`.
// Kept separate from sparks.Library so we can add the resolved
// version and keep JSON output stable even if the manifest shape
// changes.
type sparkListEntry struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Declared string `json:"declared"`
	Resolved string `json:"resolved,omitempty"`
	Error    string `json:"error,omitempty"`
}

func runSparksList(args []string) error {
	fs := flag.NewFlagSet(cmdSparksList.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain (default: table)")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	noResolve := fs.Bool("no-resolve", false, "skip module-proxy lookups; only print declared versions")
	if err := parseAndCheck(cmdSparksList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*outFmt, *asJSON, "spark list")
	if err != nil {
		return err
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
	}
	m, err := sparks.LoadManifest(sparkwingDir)
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
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"sparkwing_dir": sparkwingDir,
			"libraries":     entries,
		})
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
			fmt.Fprintf(os.Stdout, "no %s in %s\n", sparks.ManifestFilename, sparkwingDir)
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

// shortErr trims a long resolver error message to one line suitable
// for the RESOLVED column. Full error is still in JSON output.
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

// ---- lint ------------------------------------------------------

// sparkManifest is the shape of spark.json. Kept inline rather than
// imported from internal/sparks because that package is concerned
// with the consumer-side sparks.yaml, not the library-side
// spark.json. Fields follow docs/sparks.md.
type sparkManifest struct {
	Name          string                `json:"name"`
	Description   string                `json:"description"`
	Author        string                `json:"author"`
	Version       string                `json:"version"`
	SDKMinVersion string                `json:"sdk_min_version"`
	Stability     string                `json:"stability"`
	Packages      []sparkManifestPkg    `json:"packages"`
	Dependencies  []sparkManifestDepRaw `json:"dependencies"`
}

type sparkManifestPkg struct {
	Path        string `json:"path"`
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
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("spark lint: unexpected positional %q (use --path)", rest[0])
	}
	libDir, manifestPath, err := resolveSparkJSONPath(*pathFlag)
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
		// Re-try without DisallowUnknownFields so we can present a
		// clearer "parse error" vs "unknown field" distinction. Unknown
		// fields are a soft warning, not an error: spark.json may grow
		// fields ahead of our lint rules.
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
	if len(m.Packages) == 0 {
		problems = append(problems, "'packages' must be a non-empty array")
	}
	for i, p := range m.Packages {
		if strings.TrimSpace(p.Path) == "" {
			problems = append(problems, fmt.Sprintf("packages[%d]: 'path' is required", i))
		} else {
			// Path is relative to the module root; verify the dir
			// actually exists so the manifest doesn't advertise
			// phantom packages.
			abs := filepath.Join(libDir, p.Path)
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				problems = append(problems, fmt.Sprintf(
					"packages[%d] (%s): directory %s does not exist", i, p.Path, abs))
			}
		}
		if strings.TrimSpace(p.Description) == "" {
			problems = append(problems, fmt.Sprintf("packages[%d] (%s): 'description' is required", i, p.Path))
		}
		if p.Stability != "" && !validStability(p.Stability) {
			problems = append(problems, fmt.Sprintf(
				"packages[%d] (%s): stability must be experimental|beta|stable, got %q",
				i, p.Path, p.Stability))
		}
	}
	if m.Stability != "" && !validStability(m.Stability) {
		problems = append(problems, fmt.Sprintf(
			"stability must be experimental|beta|stable, got %q", m.Stability))
	}
	// Check duplicate package paths -- surfaces authorship mistakes
	// before a confused consumer does.
	seen := map[string]int{}
	for i, p := range m.Packages {
		if p.Path == "" {
			continue
		}
		if prev, ok := seen[p.Path]; ok {
			problems = append(problems, fmt.Sprintf(
				"packages[%d] (%s): duplicate path; first seen at packages[%d]",
				i, p.Path, prev))
		}
		seen[p.Path] = i
	}
	// Dependencies: informational but we can still sanity-check.
	for i, d := range m.Dependencies {
		if d.Source == "" {
			problems = append(problems, fmt.Sprintf("dependencies[%d]: 'source' is required", i))
		}
		if d.Version == "" {
			problems = append(problems, fmt.Sprintf(
				"dependencies[%d] (%s): 'version' is required", i, d.Source))
		}
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "spark lint: %s: %d problem(s)\n", manifestPath, len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		return fmt.Errorf("spark lint: %d problem(s) in %s", len(problems), manifestPath)
	}
	fmt.Fprintf(os.Stdout, "ok: %s (%d package%s)\n",
		manifestPath, len(m.Packages), pluralS(len(m.Packages)))
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

// resolveSparkJSONPath handles the ergonomics of `spark lint PATH`:
// PATH can be a library directory (we append spark.json) or a direct
// path to a spark.json file.
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

// ---- resolve ---------------------------------------------------

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
	changed, err := sparks.ResolveAndWrite(ctx, sparkwingDir)
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

// ---- update ----------------------------------------------------

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
		// Sanity-check that the named entry exists. Don't mutate
		// yaml; update re-materializes the overlay against the
		// declared versions, which already reflect any new 'latest'
		// tags or range upper bounds.
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
	changed, err := sparks.ResolveAndWrite(ctx, sparkwingDir)
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

// ---- add -------------------------------------------------------

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

// ---- remove ----------------------------------------------------

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

// ---- warmup ----------------------------------------------------

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

	// Step 1: resolve + materialize overlay. No-op when sparks.yaml is
	// absent; the rest of warmup is still worth running so a consumer
	// with just a go.mod-pinned build can still pre-compile.
	ctx := context.Background()
	if _, err := sparks.ResolveAndWrite(ctx, sparkwingDir); err != nil {
		return fmt.Errorf("spark warmup: resolve: %w", err)
	}

	// Step 2: optionally clear the local pipeline binary cache so the
	// warmup actually rebuilds. Without this flag, a prior matching
	// build short-circuits the compile loop.
	if *clearCache {
		cacheRoot := filepath.Join(bincache.SparkwingHome(), "cache", "pipelines")
		if err := os.RemoveAll(cacheRoot); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("spark warmup: clear cache %s: %w", cacheRoot, err)
		}
		fmt.Fprintf(os.Stdout, "cleared %s\n", cacheRoot)
	}

	// Step 3: discover pipelines. We compile the .sparkwing/ module
	// once (one binary registers every pipeline) rather than per
	// pipeline entry -- the binary dispatches internally on the
	// pipeline name, and bincache keys the binary on the whole
	// sparkwing dir, not per pipeline.
	_, cfg, err := pipelines.Discover(sparkwingDir)
	if err != nil {
		// Discover walks up from the start dir; a consumer repo with
		// a sparks.yaml but no pipelines.yaml is still a valid warmup
		// target if the user only cares about resolving.
		fmt.Fprintf(os.Stderr, "warn: no pipelines discovered: %v\n", err)
	} else {
		fmt.Fprintf(os.Stdout, "warming up %d pipeline(s)\n", len(cfg.Pipelines))
	}

	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return fmt.Errorf("spark warmup: hash pipeline: %w", err)
	}
	binPath := bincache.CachedBinaryPath(key)

	// If the binary already exists at this cache key, short-circuit.
	// This is the idempotent fast path: a second warmup with no
	// manifest/source changes does nothing.
	if _, err := os.Stat(binPath); err == nil {
		fmt.Fprintf(os.Stdout, "binary already cached: %s\n", binPath)
	} else {
		fmt.Fprintf(os.Stdout, "compiling %s -> %s\n", sparkwingDir, binPath)
		if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
			return fmt.Errorf("spark warmup: %w", err)
		}
	}

	// Step 4: upload to gitcache when configured. No-op without
	// SPARKWING_GITCACHE_URL; logged without failing so a warmup run
	// on a laptop without cache config still succeeds.
	if gcURL := bincache.CacheURL(); gcURL != "" {
		if err := bincache.UploadBinary(gcURL, bincache.CacheToken(), key, binPath); err != nil {
			fmt.Fprintf(os.Stderr, "warn: gitcache upload failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stdout, "uploaded to %s/bin/%s\n", gcURL, key)
		}
	} else {
		fmt.Fprintln(os.Stdout, "SPARKWING_GITCACHE_URL not set; skipping upload")
	}
	return nil
}

// ---- helpers ---------------------------------------------------

// loadManifestForWrite reads sparks.yaml for a mutation subcommand.
// Absent file -> an empty Manifest (so `spark add` on a fresh repo
// creates the file). Returns the resolved path so callers write back
// to the same location.
func loadManifestForWrite(sparkwingDir string) (*sparks.Manifest, string, error) {
	if sparkwingDir == "" {
		return nil, "", errors.New("sparkwing-dir must not be empty")
	}
	if info, err := os.Stat(sparkwingDir); err != nil {
		return nil, "", fmt.Errorf("sparkwing-dir %s: %w", sparkwingDir, err)
	} else if !info.IsDir() {
		return nil, "", fmt.Errorf("sparkwing-dir %s is not a directory", sparkwingDir)
	}
	path := filepath.Join(sparkwingDir, sparks.ManifestFilename)
	m, err := sparks.LoadManifest(sparkwingDir)
	if err != nil {
		return nil, path, err
	}
	if m == nil {
		m = &sparks.Manifest{}
	}
	return m, path, nil
}

// writeSparksYAML serializes m to path with stable indent. Uses
// go.yaml.in/yaml/v3 directly so the on-disk format stays close to
// what the resolver reads. Library names/sources/versions are short
// strings; no quoting concerns.
func writeSparksYAML(path string, m *sparks.Manifest) error {
	var buf strings.Builder
	buf.WriteString("# Managed by `sparkwing pipeline sparks add|remove|update`. See docs/sparks.md.\n")
	enc := yaml.NewEncoder(&writerAdapter{s: &buf})
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

// writerAdapter lets us feed a strings.Builder to yaml.Encoder, which
// requires an io.Writer. Avoids pulling in a bytes.Buffer solely for
// the interface satisfaction.
type writerAdapter struct{ s *strings.Builder }

func (w *writerAdapter) Write(p []byte) (int, error) { return w.s.Write(p) }
