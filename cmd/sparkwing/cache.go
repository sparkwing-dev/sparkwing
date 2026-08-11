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
	Dir        string           `json:"dir"`
	Entries    int              `json:"entries"`
	Shared     int              `json:"shared_entries"`
	TotalBytes int64            `json:"total_bytes"`
	MaxBytes   int64            `json:"max_bytes"`
	MaxEntries int              `json:"max_entries"`
	OverLimit  bool             `json:"over_limit"`
	Items      []cacheInfoEntry `json:"items,omitempty"`
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
	if err := parseAndCheck(cmdCacheInfo, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("cache info: unexpected positional %q", fs.Arg(0))
	}

	entries, err := bincache.ScanCache()
	if err != nil {
		return fmt.Errorf("cache info: %w", err)
	}

	report := cacheInfoReport{
		Dir:        bincache.CacheRoot(),
		Entries:    len(entries),
		MaxBytes:   bincache.ConfiguredMaxBytes(),
		MaxEntries: bincache.ConfiguredMaxEntries(),
	}
	for _, e := range entries {
		report.TotalBytes += e.Bytes
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

	switch output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "pretty", "":
		fmt.Println(color.Bold("PIPELINE BINARY CACHE"))
		fmt.Printf("  dir:      %s\n", color.Cyan(report.Dir))
		if report.Entries == 0 {
			fmt.Printf("  status:   %s\n", color.Dim("(empty -- no pipeline has been compiled yet)"))
			return nil
		}
		fmt.Printf("  entries:  %d\n", report.Entries)
		if report.Shared > 0 {
			fmt.Printf("  shared:   %s\n",
				color.Green(fmt.Sprintf("%d entries reused by more than one checkout", report.Shared)))
		}
		fmt.Printf("  total:    %s\n", humanBytes(report.TotalBytes))
		fmt.Printf("  ceiling:  %s / %s\n", limitLabel(report.MaxBytes), entryLimitLabel(report.MaxEntries))
		if report.OverLimit {
			fmt.Printf("  %s\n", color.Yellow("over ceiling -- the next compile will prune, or run `sparkwing cache prune`"))
		}
		fmt.Println()
		heading := fmt.Sprintf("MOST RECENTLY USED (%d of %d)", len(shown), report.Entries)
		if all {
			heading = fmt.Sprintf("ALL ENTRIES (%d)", report.Entries)
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
		return nil
	default:
		return fmt.Errorf("cache info: unknown output %q (valid: pretty, json)", output)
	}
}

func runCachePrune(args []string) error {
	fs := flag.NewFlagSet(cmdCachePrune.Path, flag.ContinueOnError)
	var output, maxBytesRaw string
	var maxEntries int
	var all bool
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	fs.StringVar(&maxBytesRaw, "max-bytes", "", "Byte ceiling, e.g. 512MiB (default: $SPARKWING_CACHE_MAX_BYTES)")
	fs.IntVar(&maxEntries, "max-entries", -1, "Entry ceiling (default: $SPARKWING_CACHE_MAX_ENTRIES)")
	fs.BoolVar(&all, "all", false, "Remove every entry, ignoring both ceilings")
	if err := parseAndCheck(cmdCachePrune, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("cache prune: unexpected positional %q", fs.Arg(0))
	}

	maxBytes := bincache.ConfiguredMaxBytes()
	if maxBytesRaw != "" {
		n, err := bincache.ParseBytes(maxBytesRaw)
		if err != nil {
			return fmt.Errorf("cache prune: --max-bytes: %w", err)
		}
		maxBytes = n
	}
	limitEntries := bincache.ConfiguredMaxEntries()
	if maxEntries >= 0 {
		limitEntries = maxEntries
	}
	if all {
		// A ceiling of one byte evicts everything the grace window
		// allows, which is the honest meaning of --all: entries a run
		// may be about to exec are still spared.
		maxBytes, limitEntries = 1, 0
	}

	result, err := bincache.Prune(maxBytes, limitEntries)
	if err != nil {
		return fmt.Errorf("cache prune: %w", err)
	}

	switch output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	case "pretty", "":
		fmt.Printf("removed %d entries, freed %s\n", result.Removed, humanBytes(result.Freed))
		fmt.Printf("kept    %d entries, %s\n", result.Kept, humanBytes(result.KeptBytes))
		if result.Skipped > 0 {
			fmt.Printf("%s\n", color.Dim(fmt.Sprintf(
				"skipped %d entry(s) still in use; a later prune will take them", result.Skipped)))
		}
		return nil
	default:
		return fmt.Errorf("cache prune: unknown output %q (valid: pretty, json)", output)
	}
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
	if err := parseAndCheck(cmdCacheExplain, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("cache explain: unexpected positional %q", fs.Arg(0))
	}
	if dir == "" {
		dir = defaultSparkwingDir()
	}

	key, parts, err := bincache.ExplainCacheKey(dir)
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
	}
	_, statErr := os.Stat(bincache.CachedBinaryPath(key))
	report := cacheExplainReport{Dir: dir, Key: key, Cached: statErr == nil}
	for _, p := range parts {
		report.Parts = append(report.Parts, cacheExplainPart{Label: p.Label, Digest: p.Digest, Detail: p.Detail})
	}

	// Other entries this same checkout has produced. When the current
	// key is a miss, the inputs that differ from those are the reason.
	absDir, _ := filepath.Abs(dir)
	entries, err := bincache.ScanCache()
	if err != nil {
		return fmt.Errorf("cache explain: %w", err)
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

	switch output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "pretty", "":
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
		return nil
	default:
		return fmt.Errorf("cache explain: unknown output %q (valid: pretty, json)", output)
	}
}
