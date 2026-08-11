// `sparkwing cache` inspects and trims the compiled-pipeline binary
// cache at $SPARKWING_HOME/cache/pipelines. Every invocation of a
// pipeline compiles its .sparkwing/ module to a binary keyed on a
// fingerprint of the source; this is where those binaries accumulate.
//
// Pruning also happens automatically after a compile, so these verbs
// are for inspecting the cache and for reclaiming space on demand
// rather than a step anyone has to remember.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

var pruneCacheToLimits = bincache.PruneToLimits

func runCache(args []string) error {
	if len(args) == 0 {
		PrintHelp(cmdCache, os.Stderr)
		return errors.New("cache: missing subcommand")
	}
	switch args[0] {
	case "info":
		return runCacheInfo(args[1:])
	case "prune":
		return runCachePrune(args[1:])
	case "explain":
		return runCacheExplain(args[1:])
	case "help", "-h", "--help":
		PrintHelp(cmdCache, os.Stdout)
		return nil
	default:
		PrintHelp(cmdCache, os.Stderr)
		return fmt.Errorf("cache: unknown verb %q (valid: info, prune, explain)", args[0])
	}
}

// cacheInfoReport is the -o json shape.
type cacheInfoReport struct {
	Dir           string           `json:"dir"`
	Entries       int              `json:"entries"`
	ActiveEntries int              `json:"active_entries"`
	BusyEntries   int              `json:"busy_entries"`
	LegacyEntries int              `json:"legacy_entries"`
	LegacyBytes   int64            `json:"legacy_bytes"`
	Shared        int              `json:"shared_entries"`
	TotalBytes    int64            `json:"total_bytes"`
	MaxBytes      int64            `json:"max_bytes"`
	MaxEntries    int              `json:"max_entries"`
	OverLimit     bool             `json:"over_limit"`
	Items         []cacheInfoEntry `json:"items,omitempty"`
}

type cacheInfoEntry struct {
	Key      string           `json:"key"`
	Bytes    int64            `json:"bytes"`
	LastUsed string           `json:"last_used,omitempty"`
	Uses     int              `json:"uses"`
	Shared   bool             `json:"shared"`
	Owners   []cacheInfoOwner `json:"owners,omitempty"`
}

type cacheInfoOwner struct {
	Dir  string `json:"dir"`
	Uses int    `json:"uses"`
}

func runCacheInfo(args []string) error {
	fs := flag.NewFlagSet(cmdCacheInfo.Path, flag.ContinueOnError)
	var output string
	var all bool
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	fs.BoolVar(&all, "all", false, "List every entry rather than the ten most recent")
	requestedOutput := cacheOutputFromArgs(args, output)
	if err := parseAndCheck(cmdCacheInfo, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return writeCacheError(requestedOutput, err)
	}
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return writeCacheError(output, fmt.Errorf("cache info: unexpected positional %q", fs.Arg(0)))
	}

	entries, err := bincache.ScanCache()
	if err != nil {
		return writeCacheError(output, fmt.Errorf("cache info: %w", err))
	}
	status, err := bincache.Status(context.Background(), "")
	if err != nil {
		return writeCacheError(output, fmt.Errorf("cache info: %w", err))
	}

	report := cacheInfoReport{
		Dir:           bincache.CacheRoot(),
		Entries:       len(entries) + status.LegacyEntries,
		ActiveEntries: status.ActiveEntries,
		BusyEntries:   status.BusyEntries,
		LegacyEntries: status.LegacyEntries,
		LegacyBytes:   status.LegacyBytes,
		TotalBytes:    status.ObservedBytes + status.LegacyBytes,
		MaxBytes:      bincache.ConfiguredMaxBytes(),
		MaxEntries:    bincache.ConfiguredMaxEntries(),
	}
	for _, e := range entries {
		if len(e.Owners) > 1 {
			report.Shared++
		}
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
		item.Uses = bincache.TotalUses(e.Owners)
		item.Shared = len(e.Owners) > 1
		for _, o := range e.Owners {
			item.Owners = append(item.Owners, cacheInfoOwner{Dir: o.Dir, Uses: o.Uses})
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
		if report.Shared > 0 {
			fmt.Printf("  shared:   %s\n",
				color.Green(fmt.Sprintf("%d entries reused by more than one checkout", report.Shared)))
		}
		fmt.Printf("  total:    %s\n", humanBytes(report.TotalBytes))
		if report.ActiveEntries > 0 || report.BusyEntries > 0 {
			fmt.Printf("  in use:   %d active, %d busy\n", report.ActiveEntries, report.BusyEntries)
		}
		if report.LegacyEntries > 0 {
			fmt.Printf("  legacy:   %s across %d entries\n", humanBytes(report.LegacyBytes), report.LegacyEntries)
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
			fmt.Printf("  %-20s %10s  %-10s %4s  %s\n",
				e.Key, humanBytes(e.Bytes), last,
				color.Dim(fmt.Sprintf("x%d", bincache.TotalUses(e.Owners))),
				ownerLabel(e.Owners, all))
		}
		if !all && len(entries) > len(shown) {
			fmt.Printf("  %s\n", color.Dim(fmt.Sprintf("... and %d more (--all to list)", len(entries)-len(shown))))
		}
	})
}

func runCachePrune(args []string) error {
	fs := flag.NewFlagSet(cmdCachePrune.Path, flag.ContinueOnError)
	var output, maxBytesRaw string
	var maxEntries int
	var all bool
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	fs.StringVar(&maxBytesRaw, "max-bytes", "", "byte ceiling, e.g. 512MiB")
	fs.IntVar(&maxEntries, "max-entries", -1, "entry ceiling")
	fs.BoolVar(&all, "all", false, "remove every inactive entry")
	requestedOutput := cacheOutputFromArgs(args, output)
	if err := parseAndCheck(cmdCachePrune, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return writeCacheError(requestedOutput, err)
	}
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return writeCacheError(output, fmt.Errorf("cache prune: unexpected positional %q", fs.Arg(0)))
	}
	maxBytes := bincache.ConfiguredMaxBytes()
	if maxBytesRaw != "" {
		parsed, err := bincache.ParseBytes(maxBytesRaw)
		if err != nil {
			return writeCacheError(output, fmt.Errorf("cache prune: --max-bytes: %w", err))
		}
		maxBytes = parsed
	}
	entryLimit := bincache.ConfiguredMaxEntries()
	if maxEntries >= 0 {
		entryLimit = maxEntries
	}
	result, err := pruneCacheToLimits(context.Background(), maxBytes, entryLimit, all)
	if err != nil {
		return writeCacheError(output, fmt.Errorf("cache prune: %w", err))
	}
	return writeCacheOutput(output, result, func() {
		fmt.Printf("removed: %s across %d entries\n", humanBytes(result.RemovedBytes), result.Reclaimed)
		fmt.Printf("observed capacity gained: %s\n", humanBytes(result.ReclaimedBytes))
		fmt.Printf("examined: %d, active: %d, busy: %d\n", result.Examined, result.Active, result.Busy)
		fmt.Printf("goal satisfied: %t\n", result.GoalSatisfied)
	})
}

func writeCacheError(output string, err error) error {
	if output != "json" {
		return err
	}
	encoded := struct {
		Payload any `json:"payload"`
		Error   any `json:"error"`
	}{Error: struct {
		Message string `json:"message"`
	}{Message: err.Error()}}
	return errors.Join(err, json.NewEncoder(os.Stdout).Encode(encoded))
}

func writeCacheOutput(output string, payload any, pretty func()) error {
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	switch output {
	case "json":
		encoded := struct {
			Payload any `json:"payload"`
			Error   any `json:"error"`
		}{Payload: payload}
		return json.NewEncoder(os.Stdout).Encode(encoded)
	case "pretty", "":
		pretty()
		return nil
	}
	return nil
}

func validateCacheOutput(output string) error {
	switch output {
	case "json", "pretty", "":
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json)", output)
	}
}

func cacheOutputFromArgs(args []string, fallback string) string {
	output := fallback
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			return output
		case args[i] == "-o" || args[i] == "--output":
			if i+1 < len(args) {
				output = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], "-o="):
			output = strings.TrimPrefix(args[i], "-o=")
		case strings.HasPrefix(args[i], "-o") && len(args[i]) > len("-o"):
			output = strings.TrimPrefix(args[i], "-o")
		case strings.HasPrefix(args[i], "--output="):
			output = strings.TrimPrefix(args[i], "--output=")
		}
	}
	return output
}

// ownerLabel names the checkouts an entry serves. A key is a content
// fingerprint and -trimpath keeps build paths out of the binary, so
// without this an entry is an unidentifiable 90 MB blob. One entry can
// legitimately serve several checkouts, which is the point of a
// path-independent key, so the extras are counted rather than hidden.
func ownerLabel(owners []bincache.Owner, all bool) string {
	if len(owners) == 0 {
		return color.Dim("(unknown -- cached before owners were recorded)")
	}
	if all {
		parts := make([]string, 0, len(owners))
		for _, o := range owners {
			parts = append(parts, fmt.Sprintf("%s (x%d)", abbreviateHome(o.Dir), o.Uses))
		}
		return strings.Join(parts, ", ")
	}
	label := abbreviateHome(owners[0].Dir)
	if len(owners) > 1 {
		label += color.Green(fmt.Sprintf(" +%d more checkout(s)", len(owners)-1))
	}
	return label
}

// abbreviateHome shortens $HOME to ~ so a listing stays readable.
func abbreviateHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return "~" + strings.TrimPrefix(p, home)
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

// humanAge renders a duration the way a cache listing wants it: coarse,
// and never more precise than the reader can use.
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

// cacheExplainReport is the -o json shape for `cache explain`.
type cacheExplainReport struct {
	Dir     string             `json:"dir"`
	Key     string             `json:"key"`
	Cached  bool               `json:"cached"`
	Parts   []cacheExplainPart `json:"parts"`
	Related []cacheExplainPrev `json:"related_entries,omitempty"`
}

type cacheExplainPart struct {
	Label  string `json:"label"`
	Digest string `json:"digest"`
	Detail string `json:"detail,omitempty"`
}

type cacheExplainPrev struct {
	Key     string   `json:"key"`
	Changed []string `json:"changed_inputs,omitempty"`
}

func runCacheExplain(args []string) error {
	fs := flag.NewFlagSet(cmdCacheExplain.Path, flag.ContinueOnError)
	var output, dir string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	fs.StringVar(&dir, "dir", "", "Pipeline module directory (default: ./.sparkwing)")
	requestedOutput := cacheOutputFromArgs(args, output)
	if err := parseAndCheck(cmdCacheExplain, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return writeCacheError(requestedOutput, err)
	}
	if err := validateCacheOutput(output); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return writeCacheError(output, fmt.Errorf("cache explain: unexpected positional %q", fs.Arg(0)))
	}
	if dir == "" {
		dir = defaultSparkwingDir()
	}

	key, parts, err := bincache.ExplainCacheKey(dir)
	if err != nil {
		return writeCacheError(output, fmt.Errorf("cache explain: %w", err))
	}
	entry, entryErr := bincache.PipelineEntry(key)
	if entryErr != nil {
		return writeCacheError(output, fmt.Errorf("cache explain: %w", entryErr))
	}
	lease, cached, acquireErr := entry.Acquire(context.Background())
	if acquireErr != nil {
		return writeCacheError(output, fmt.Errorf("cache explain: %w", acquireErr))
	}
	if lease != nil {
		defer func() { _ = lease.Release() }()
	}
	report := cacheExplainReport{Dir: dir, Key: key, Cached: cached}
	for _, p := range parts {
		report.Parts = append(report.Parts, cacheExplainPart{Label: p.Label, Digest: p.Digest, Detail: p.Detail})
	}

	// Other entries this same checkout has produced. When the current
	// key is a miss, the inputs that differ from those are the reason.
	absDir, _ := filepath.Abs(dir)
	entries, err := bincache.ScanCache()
	if err != nil {
		return writeCacheError(output, fmt.Errorf("cache explain: %w", err))
	}
	for _, e := range entries {
		if e.Key == key {
			continue
		}
		owned := false
		for _, o := range e.Owners {
			if o.Dir == absDir {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		prev := cacheExplainPrev{Key: e.Key}
		if stored := bincache.StoredKeyParts(e.Key); stored != nil {
			prev.Changed = bincache.DiffKeyParts(stored, parts)
		}
		report.Related = append(report.Related, prev)
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
		if len(report.Related) > 0 {
			fmt.Println()
			fmt.Println(color.Bold("OTHER ENTRIES FROM THIS CHECKOUT"))
			for _, r := range report.Related {
				fmt.Printf("  %s\n", r.Key)
				if len(r.Changed) == 0 {
					fmt.Printf("    %s\n", color.Dim("(built before inputs were recorded)"))
					continue
				}
				for _, c := range r.Changed {
					fmt.Printf("    %s %s\n", color.Yellow("changed:"), c)
				}
			}
		}
	})
}
