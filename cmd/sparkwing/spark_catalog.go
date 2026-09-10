package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	flag "github.com/spf13/pflag"

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
		moduleDir, resolved, derr := sparks.DownloadModule(context.Background(), sparkwingDir, source, version)
		if derr != nil {
			return fmt.Errorf("spark catalog: %w", derr)
		}
		source += "@" + resolved
		target = moduleDir
	}

	manifest, manifestPath, err := readSparkManifest(target)
	if err != nil {
		return err
	}
	field, entries, problem := sparkManifestShape(*manifest)
	if problem != "" {
		return fmt.Errorf("spark catalog: %s: %s", manifestPath, problem)
	}

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
			fmt.Println(b.Name)
		}
		return nil
	default:
		printSparkCatalog(summary, field, blocks)
		return nil
	}
}

func readSparkManifest(target string) (*sparkManifest, string, error) {
	_, manifestPath, err := resolveSparkJSONPath(target)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, "", fmt.Errorf("spark catalog: read %s: %w", manifestPath, err)
	}
	var m sparkManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", fmt.Errorf("spark catalog: %s: invalid JSON: %w", manifestPath, err)
	}
	return &m, manifestPath, nil
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

func printSparkCatalog(summary sparkCatalogSummary, field string, blocks []sparkCatalogBlock) {
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
	if len(blocks) == 0 {
		fmt.Printf("this library declares no %s.\n", field)
		return
	}
	nameWidth, stabilityWidth := 0, 0
	for _, b := range blocks {
		nameWidth = max(nameWidth, len(b.Name))
		stabilityWidth = max(stabilityWidth, len(b.Stability))
	}
	for _, b := range blocks {
		fmt.Printf("  %-*s  %-*s  %s\n", nameWidth, b.Name, stabilityWidth, b.Stability, b.Description)
	}
	fmt.Println()
	fmt.Printf("import path: %s\n", importPathHint(blocks))
	fmt.Printf("edit one here: sparkwing pipeline sparks inflate --module %s\n", blocks[0].Name)
}

func importPathHint(blocks []sparkCatalogBlock) string {
	for _, b := range blocks {
		if b.Module != "" {
			return path.Dir(b.Module) + "/<name>"
		}
	}
	return "the library's own module path"
}
