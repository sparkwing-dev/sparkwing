package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func docsCacheInfo(output string) error {
	client := docs.NewWebClient()
	stats, err := client.CacheInfo()
	if err != nil {
		return fmt.Errorf("cache info --docs: %w", err)
	}
	switch output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(stats)
	case "plain":
		_, err := fmt.Println(stats.Dir)
		return err
	}
	fmt.Println(color.Bold("DOCS WEB CACHE"))
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

type docsCacheClearReport struct {
	Dir     string `json:"dir"`
	Removed int    `json:"removed"`
}

func docsCacheClear(output string) error {
	client := docs.NewWebClient()
	removed, err := client.ClearCache()
	if err != nil {
		return fmt.Errorf("cache prune --docs: %w", err)
	}
	switch output {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(docsCacheClearReport{Dir: client.CacheDir, Removed: removed})
	case "plain":
		_, err := fmt.Println(removed)
		return err
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
