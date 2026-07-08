// `sparkwing docs cache` manages the on-disk web cache that backs
// `--web` fetches. The cache lives at
// $XDG_CACHE_HOME/sparkwing/web/ (or ~/.cache/sparkwing/web/) and
// mirrors the URL paths it serves, so users can inspect / grep the
// cached files directly without going through the CLI.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func runDocsCache(args []string) error {
	if len(args) == 0 {
		PrintHelp(cmdDocsCache, os.Stderr)
		return errors.New("docs cache: missing subcommand")
	}
	switch args[0] {
	case "info":
		return runDocsCacheInfo(args[1:])
	case "clear":
		return runDocsCacheClear(args[1:])
	case "help", "-h", "--help":
		PrintHelp(cmdDocsCache, os.Stdout)
		return nil
	default:
		PrintHelp(cmdDocsCache, os.Stderr)
		return fmt.Errorf("docs cache: unknown verb %q (valid: info, clear)", args[0])
	}
}

func runDocsCacheInfo(args []string) error {
	fs := flag.NewFlagSet(cmdDocsCacheInfo.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	if err := parseAndCheck(cmdDocsCacheInfo, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs cache info: unexpected positional %q", fs.Arg(0))
	}
	client := docs.NewWebClient()
	stats, err := client.CacheInfo()
	if err != nil {
		return fmt.Errorf("docs cache info: %w", err)
	}
	switch output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	case "pretty", "":
		fmt.Println(color.Bold("CACHE"))
		fmt.Printf("  dir:        %s\n", color.Cyan(stats.Dir))
		if !stats.Exists {
			fmt.Printf("  status:     %s\n", color.Dim("(not yet created -- no --web fetches have run)"))
			return nil
		}
		fmt.Printf("  total:      %d files, %s\n", stats.TotalFiles, humanBytes(stats.TotalBytes))
		fmt.Printf("  docs:       %d files\n", stats.DocFiles)
		fmt.Printf("  migrations: %d files\n", stats.MigrationFiles)
		fmt.Printf("  indexes:    %d files\n", stats.IndexFiles)
		fmt.Printf("  versions:   %s\n", stats.VersionsState)
		return nil
	}
	return fmt.Errorf("unknown output format %q (valid: pretty, json)", output)
}

func runDocsCacheClear(args []string) error {
	fs := flag.NewFlagSet(cmdDocsCacheClear.Path, flag.ContinueOnError)
	if err := parseAndCheck(cmdDocsCacheClear, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs cache clear: unexpected positional %q", fs.Arg(0))
	}
	client := docs.NewWebClient()
	removed, err := client.ClearCache()
	if err != nil {
		return fmt.Errorf("docs cache clear: %w", err)
	}
	if removed == 0 {
		fmt.Println(color.Dim("(cache was already empty)"))
		return nil
	}
	fmt.Printf("removed %d file(s) from %s\n", removed, color.Cyan(client.CacheDir))
	return nil
}

func humanBytes(n int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
