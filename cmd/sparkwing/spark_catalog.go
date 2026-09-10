package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/modfile"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const sparksCoreModule = "github.com/sparkwing-dev/sparks-core"

type sparkCatalogSummary struct {
	Kind        string `json:"kind"`
	Library     string `json:"library"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	Manifest    string `json:"manifest"`
	Blocks      int    `json:"blocks"`
}

type sparkCatalogBlock struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Module      string `json:"module,omitempty"`
	Description string `json:"description,omitempty"`
	Stability   string `json:"stability,omitempty"`
}

func runSparksCatalog(args []string) error {
	fs := flag.NewFlagSet(cmdSparksCatalog.Path, flag.ContinueOnError)
	dir := fs.String("sparkwing-dir", "", "path to .sparkwing/ (default: <cwd>/.sparkwing)")
	library := fs.String("library", "", "spark library module path (default: "+sparksCoreModule+")")
	pathFlag := fs.String("path", "", "read a library checkout on disk instead of downloading it")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: table)")
	if err := parseAndCheck(cmdSparksCatalog, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("spark catalog: unexpected positional %q (use --library or --path)", rest[0])
	}
	if *pathFlag != "" && *library != "" {
		return errors.New("spark catalog: --path and --library both given (pass one)")
	}
	format, err := resolveOutputFormat(*outFmt, "spark catalog")
	if err != nil {
		return err
	}
	sparkwingDir := *dir
	if sparkwingDir == "" {
		sparkwingDir = defaultSparkwingDir()
	}

	source := ""
	target := *pathFlag
	if target == "" {
		source = *library
		if source == "" {
			source = sparksCoreModule
		}
		version := declaredSparkVersion(sparkwingDir, source)
		moduleDir, resolved, derr := sparks.DownloadModule(context.Background(), downloadDir(sparkwingDir), source, version)
		if derr != nil {
			return fmt.Errorf("spark catalog: %w", derr)
		}
		source += "@" + resolved
		target = moduleDir
	}

	manifest, libDir, manifestPath, err := readSparkManifest(target)
	if err != nil {
		return err
	}
	field, entries, problem := sparkManifestShape(*manifest)
	if problem != "" {
		return fmt.Errorf("spark catalog: %s: %s", manifestPath, problem)
	}

	libModule := libraryModulePath(source, libDir, entries)

	summary := sparkCatalogSummary{
		Kind:        "summary",
		Library:     manifest.Name,
		Version:     manifest.Version,
		Description: manifest.Description,
		Source:      source,
		Manifest:    manifestPath,
		Blocks:      len(entries),
	}
	blocks := make([]sparkCatalogBlock, 0, len(entries))
	for _, e := range entries {
		blocks = append(blocks, sparkCatalogBlock{
			Kind:        "block",
			Name:        e.Path,
			Module:      e.Module,
			Description: e.Description,
			Stability:   e.Stability,
		})
	}

	switch format {
	case "json":
		if err := ndjson.Write(os.Stdout, []sparkCatalogSummary{summary}); err != nil {
			return err
		}
		return ndjson.Write(os.Stdout, blocks)
	case "plain":
		for _, b := range blocks {
			fmt.Println(blockValue(b))
		}
		return nil
	default:
		printSparkCatalog(summary, field, libModule, blocks)
		return nil
	}
}

// safety: the catalog is a discovery verb, so it has to answer before a project exists;
// go mod download only needs a directory it can run in.
func downloadDir(sparkwingDir string) string {
	if info, err := os.Stat(sparkwingDir); err == nil && info.IsDir() {
		return sparkwingDir
	}
	return os.TempDir()
}

// safety: a modules[] row is its own module and `inflate --module` must get that path,
// because a bare name resolves against sparks-core; a packages[] row is not a module at all.
func blockValue(b sparkCatalogBlock) string {
	if b.Module != "" {
		return b.Module
	}
	return b.Name
}

func libraryModulePath(source, libDir string, entries []sparkManifestEntry) string {
	if source != "" {
		if trimmed, _, ok := strings.Cut(source, "@"); ok && trimmed != "" {
			return trimmed
		}
		return source
	}
	for _, e := range entries {
		if e.Module != "" {
			return path.Dir(e.Module)
		}
	}
	return modulePathFromGoMod(filepath.Join(libDir, "go.mod"))
}

func modulePathFromGoMod(goModPath string) string {
	raw, err := os.ReadFile(goModPath)
	if err != nil {
		return ""
	}
	return modfile.ModulePath(raw)
}

func readSparkManifest(target string) (*sparkManifest, string, string, error) {
	libDir, manifestPath, err := resolveSparkJSONPath("spark catalog", target)
	if err != nil {
		return nil, "", "", err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("spark catalog: read %s: %w", manifestPath, err)
	}
	var m sparkManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", "", fmt.Errorf("spark catalog: %s: invalid JSON: %w", manifestPath, err)
	}
	return &m, libDir, manifestPath, nil
}

func declaredSparkVersion(sparkwingDir, source string) string {
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil || m == nil {
		return "latest"
	}
	for _, lib := range m.Libraries {
		if lib.Source == source && strings.TrimSpace(lib.Version) != "" {
			return lib.Version
		}
	}
	return "latest"
}

func printSparkCatalog(summary sparkCatalogSummary, field, libModule string, blocks []sparkCatalogBlock) {
	title := summary.Library
	if title == "" {
		title = summary.Source
	}
	if summary.Version != "" {
		title += " " + summary.Version
	}
	if summary.Description != "" {
		title += " -- " + summary.Description
	}
	fmt.Println(title)
	if summary.Source != "" {
		fmt.Printf("%s\n", summary.Source)
	}
	fmt.Println()
	nameWidth, stabilityWidth := 0, 0
	for _, b := range blocks {
		nameWidth = max(nameWidth, len(b.Name))
		stabilityWidth = max(stabilityWidth, len(b.Stability))
	}
	for _, b := range blocks {
		fmt.Printf("  %-*s  %-*s  %s\n", nameWidth, b.Name, stabilityWidth, b.Stability, b.Description)
	}
	fmt.Println()

	if libModule != "" {
		fmt.Printf("import: %s/<name>\n", libModule)
	}
	if field == "modules" {
		fmt.Printf("edit one: sparkwing pipeline sparks inflate --module %s\n", blockValue(blocks[0]))
		return
	}
	if libModule != "" {
		fmt.Printf("these are packages inside one Go module; edit the library: "+
			"sparkwing pipeline sparks inflate --module %s\n", libModule)
		return
	}
	fmt.Println("these are packages inside one Go module; inflate the library by its module path.")
}
