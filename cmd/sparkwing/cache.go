package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

var pruneCacheToLimits = bincache.PruneToLimits

type cacheInfoReport struct {
	Dir           string           `json:"dir"`
	Entries       int              `json:"entries"`
	ActiveEntries int              `json:"active_entries"`
	BusyEntries   int              `json:"busy_entries"`
	TotalBytes    int64            `json:"total_bytes"`
	MaxBytes      int64            `json:"max_bytes"`
	MaxEntries    int              `json:"max_entries"`
	OverLimit     bool             `json:"over_limit"`
	Items         []cacheInfoEntry `json:"items,omitempty"`
}

type cacheInfoEntry struct {
	Key      string `json:"key"`
	Bytes    int64  `json:"bytes"`
	LastUsed string `json:"last_used,omitempty"`
}

func runCacheInfo(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	output := inv.Output(color.IsInteractiveStdout())
	all := inv.Bool("all-entries")
	docsCache := inv.Bool("docs")
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	if docsCache {
		return docsCacheInfo(output)
	}

	entries, err := bincache.ScanCache()
	if err != nil {
		return fmt.Errorf("cache info: %w", err)
	}
	status, err := bincache.Status(context.Background(), "")
	if err != nil {
		return fmt.Errorf("cache info: %w", err)
	}
	limits, err := bincache.ConfiguredLimits()
	if err != nil {
		return fmt.Errorf("cache info: %w", err)
	}

	report := cacheInfoReport{
		Dir:           bincache.CacheRoot(),
		Entries:       len(entries),
		ActiveEntries: status.ActiveEntries,
		BusyEntries:   status.BusyEntries,
		TotalBytes:    status.ObservedBytes,
		MaxBytes:      limits.MaxBytes,
		MaxEntries:    limits.MaxEntries,
	}
	report.OverLimit = (report.MaxBytes > 0 && report.TotalBytes > report.MaxBytes) ||
		(report.MaxEntries > 0 && report.Entries > report.MaxEntries)

	shown := entries
	if !all && len(shown) > 10 {
		shown = shown[:10]
	}
	for _, e := range shown {
		item := cacheInfoEntry{Key: e.Key, Bytes: e.Bytes}
		if !e.LastUsed.IsZero() {
			item.LastUsed = e.LastUsed.UTC().Format(time.RFC3339)
		}
		report.Items = append(report.Items, item)
	}

	return writeCacheOutput(output, report, func() {
		fmt.Println(color.Bold("PIPELINE BINARY CACHE"))
		fmt.Printf("  dir:      %s\n", color.Cyan(report.Dir))
		if report.Entries == 0 {
			fmt.Printf("  status:   %s\n", color.Dim("(empty -- no pipeline has been compiled yet)"))
			return
		}
		fmt.Printf("  entries:  %d\n", report.Entries)
		fmt.Printf("  total:    %s\n", humanBytes(report.TotalBytes))
		if report.ActiveEntries > 0 || report.BusyEntries > 0 {
			fmt.Printf("  in use:   %d active, %d busy\n", report.ActiveEntries, report.BusyEntries)
		}
		fmt.Printf("  ceiling:  %s / %s\n", limitLabel(report.MaxBytes), entryLimitLabel(report.MaxEntries))
		if report.OverLimit {
			fmt.Printf("  %s\n", color.Yellow("over ceiling -- the next compile will prune, or run `sparkwing cache prune`"))
		}
		fmt.Println()
		heading := fmt.Sprintf("MOST RECENTLY USED (%d managed of %d total)", len(shown), report.Entries)
		if all {
			heading = fmt.Sprintf("ALL MANAGED ENTRIES (%d)", len(entries))
		}
		fmt.Println(color.Bold(heading))
		for _, e := range shown {
			last := color.Dim("never used since build")
			if !e.LastUsed.IsZero() {
				last = humanAge(time.Since(e.LastUsed))
			}
			fmt.Printf("  %-20s %10s  %s\n", e.Key, humanBytes(e.Bytes), last)
		}
		if !all && len(entries) > len(shown) {
			fmt.Printf("  %s\n", color.Dim(fmt.Sprintf("... and %d more (--all-entries to list)", len(entries)-len(shown))))
		}
	})
}

func runCachePrune(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	output := inv.Output(color.IsInteractiveStdout())
	maxBytesRaw := inv.Value("max-bytes")
	maxEntries := -1
	if inv.Has("max-entries") {
		maxEntries, _ = strconv.Atoi(inv.Value("max-entries"))
	}
	all, docsCache := inv.Bool("all-entries"), inv.Bool("docs")
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	if docsCache {
		return docsCacheClear(output)
	}
	limits, err := bincache.ConfiguredLimits()
	if err != nil {
		return fmt.Errorf("cache prune: %w", err)
	}
	maxBytes := limits.MaxBytes
	if maxBytesRaw != "" {
		parsed, err := userconfig.ParseByteSize(maxBytesRaw)
		if err != nil {
			return fmt.Errorf("cache prune: --max-bytes: %w", err)
		}
		maxBytes = parsed
	}
	entryLimit := limits.MaxEntries
	if maxEntries >= 0 {
		entryLimit = maxEntries
	}
	result, err := pruneCacheToLimits(context.Background(), maxBytes, entryLimit, all)
	if err != nil {
		return fmt.Errorf("cache prune: %w", err)
	}
	return writeCacheOutput(output, result, func() {
		if result.PruneBusy {
			fmt.Println("prune already in progress")
			return
		}
		fmt.Printf("removed: %s across %d entries\n", humanBytes(result.LogicalRemovedBytes), result.ReclaimedEntries)
		fmt.Printf("examined: %d, active skipped: %d, busy skipped: %d\n",
			result.ExaminedEntries, result.ActiveSkippedEntries, result.BusySkippedEntries)
		fmt.Printf("goal satisfied: %t\n", result.GoalSatisfied)
	})
}

func writeCacheOutput(output string, payload any, pretty func()) error {
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	switch output {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(payload)
	case "plain":
		switch report := payload.(type) {
		case cacheInfoReport:
			fmt.Println(report.Dir)
		case cacheExplainReport:
			fmt.Println(report.Key)
		case bincache.PruneResult:
			fmt.Println(report.ReclaimedEntries)
		default:
			return fmt.Errorf("cache: no plain value for %T", payload)
		}
	default:
		pretty()
	}
	return nil
}

func validateCacheOutput(output string) error {
	_, err := resolveOutputFormat(output, "cache")
	return err
}

func limitLabel(n int64) string {
	if n <= 0 {
		return "unlimited"
	}
	return humanBytes(n)
}

func entryLimitLabel(n int) string {
	if n <= 0 {
		return "unlimited entries"
	}
	return fmt.Sprintf("%d entries", n)
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

type cacheExplainReport struct {
	Dir    string             `json:"dir"`
	Key    string             `json:"key"`
	Cached bool               `json:"cached"`
	Reason string             `json:"reason,omitempty"`
	Parts  []cacheExplainPart `json:"parts"`
}

type cacheExplainPart struct {
	Label  string `json:"label"`
	Digest string `json:"digest"`
	Detail string `json:"detail,omitempty"`
}

func runCacheExplain(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	output := inv.Output(color.IsInteractiveStdout())
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	dir, err := findSparkwingDir()
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
	}
	ts, err := pipelineentry.IsTypeScript(dir)
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
	}
	if ts {
		report := cacheExplainReport{Dir: dir, Reason: "a TypeScript pipeline runs from source with node, so nothing is compiled or cached"}
		return writeCacheOutput(output, report, func() {
			fmt.Println(color.Bold("CACHE KEY"))
			fmt.Printf("  pipeline: %s\n", color.Cyan(dir))
			fmt.Printf("  status:   %s\n", color.Dim("not cached -- "+report.Reason))
		})
	}

	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}, procenv.Runtime{})
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
	}
	pin, err := engine.ReadPin(dir, engineid.GoSDK)
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
	}
	environment, err = sdkinstall.GoEnvironment(context.Background(), dir, pin, environment)
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
	}
	key, parts, err := bincache.ExplainCacheKey(dir, slices.Collect(environment.Values()))
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
	}
	entry, entryErr := bincache.PipelineEntry(key)
	if entryErr != nil {
		return fmt.Errorf("cache explain: %w", entryErr)
	}
	lease, cached, acquireErr := entry.Acquire(context.Background())
	if acquireErr != nil {
		return fmt.Errorf("cache explain: %w", acquireErr)
	}
	if lease != nil {
		defer func() { _ = lease.Release() }()
	}
	report := cacheExplainReport{Dir: dir, Key: key, Cached: cached}
	for _, p := range parts {
		report.Parts = append(report.Parts, cacheExplainPart{Label: p.Label, Digest: p.Digest, Detail: p.Detail})
	}

	return writeCacheOutput(output, report, func() {
		fmt.Println(color.Bold("CACHE KEY"))
		fmt.Printf("  pipeline: %s\n", color.Cyan(dir))
		fmt.Printf("  key:      %s\n", color.Cyan(report.Key))
		if report.Cached {
			fmt.Printf("  status:   %s\n", color.Green("cached -- the next run reuses this binary"))
		} else {
			fmt.Printf("  status:   %s\n", color.Yellow("not cached -- the next run compiles"))
		}
		fmt.Println()
		fmt.Println(color.Bold("INPUTS"))
		for _, p := range report.Parts {
			fmt.Printf("  %-28s %s  %s\n", p.Label, color.Dim(p.Digest), color.Dim(p.Detail))
		}
	})
}
