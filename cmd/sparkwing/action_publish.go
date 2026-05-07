// `sparkwing pipeline publish` -- compile the .sparkwing/ pipeline
// binary and upload it to the configured ArtifactStore at bin/<hash>.
// . Pairs with the storage backends + the
// existing PipelineCacheKey so a single S3 bucket hosts both
// pipeline binaries (via this command) and per-run logs / state
// (via ci-embedded mode).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/bincache"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage/storeurl"
)

// publishedBinary is one row of the publish output -- one entry per
// (GOOS, GOARCH) combination requested by --platform.
type publishedBinary struct {
	Key        string `json:"key"`
	Platform   string `json:"platform"`
	SizeBytes  int64  `json:"size_bytes"`
	UploadedTo string `json:"uploaded_to"`
}

func runPipelinePublish(args []string) error {
	fs := flag.NewFlagSet("pipeline publish", flag.ContinueOnError)
	on := fs.String("on", "",
		"profile name; uses its artifact_store field as the upload target")
	artifactStore := fs.String("artifact-store", "",
		"artifact-store URL (fs:///path or s3://bucket/prefix). Overrides --on.")
	platforms := fs.String("platform", "",
		"comma-separated GOOS/GOARCH pairs to cross-compile + publish "+
			"(e.g. linux/amd64,linux/arm64,darwin/arm64). Default: current platform.")
	sparkwingDirFlag := fs.String("dir", "",
		"path to .sparkwing/ (default: walk up from cwd)")
	output := fs.StringP("output", "o", "table", "output format: table | json | plain")
	asJSON := fs.Bool("json", false, "alias for --output json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve the artifact-store target. URL flag wins over profile.
	storeURL, err := resolveArtifactStoreURL(*on, *artifactStore)
	if err != nil {
		return err
	}
	if storeURL == "" {
		return errors.New("pipeline publish: no artifact-store configured. Pass --on PROFILE (with artifact_store set) or --artifact-store URL")
	}
	store, err := storeurl.OpenArtifactStore(context.Background(), storeURL)
	if err != nil {
		return fmt.Errorf("open artifact-store: %w", err)
	}

	// Resolve .sparkwing/ -- explicit --dir wins, fallback to walk-up.
	dir := *sparkwingDirFlag
	if dir == "" {
		d, err := findSparkwingDir()
		if err != nil {
			return err
		}
		dir = d
	}

	// Parse --platform list. Empty = current platform only, matches
	// what `wing run` would compile for.
	platforms_list, err := parsePlatforms(*platforms)
	if err != nil {
		return err
	}

	results := make([]publishedBinary, 0, len(platforms_list))
	for _, p := range platforms_list {
		row, err := compileAndPublishOne(context.Background(), dir, p, store, storeURL)
		if err != nil {
			return fmt.Errorf("publish %s: %w", p.label(), err)
		}
		results = append(results, row)
	}

	format := "table"
	switch {
	case *asJSON:
		format = "json"
	case *output != "":
		format = *output
	}
	return renderPublishResults(results, format)
}

// platform is a parsed GOOS/GOARCH target.
type platform struct {
	OS, Arch string
}

func (p platform) label() string { return p.OS + "/" + p.Arch }

// parsePlatforms splits comma-separated "os/arch" entries; empty
// returns a single-element slice for the current runtime.
func parsePlatforms(s string) ([]platform, error) {
	if s == "" {
		return []platform{{OS: runtime.GOOS, Arch: runtime.GOARCH}}, nil
	}
	out := []platform{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		slash := strings.Index(part, "/")
		if slash <= 0 || slash == len(part)-1 {
			return nil, fmt.Errorf("--platform %q: expected GOOS/GOARCH (e.g. linux/amd64)", part)
		}
		out = append(out, platform{OS: part[:slash], Arch: part[slash+1:]})
	}
	if len(out) == 0 {
		return nil, errors.New("--platform: no valid entries")
	}
	return out, nil
}

// compileAndPublishOne builds the pipeline binary for one target
// platform and uploads it to the artifact-store under bin/<hash>.
// Returns the metadata row for the publish output.
func compileAndPublishOne(ctx context.Context, sparkwingDir string, p platform, store storage.ArtifactStore, storeURL string) (publishedBinary, error) {
	key, err := bincache.PipelineCacheKeyForPlatform(sparkwingDir, p.OS, p.Arch)
	if err != nil {
		return publishedBinary{}, fmt.Errorf("hash: %w", err)
	}
	// Per-platform local cache path so a cross-compile doesn't stomp
	// the operator's host-platform binary (which lives at the same
	// hash if not for the platform mix-in).
	binPath := bincache.CachedBinaryPath(key)

	// Compile if not already cached locally for this hash. The local
	// cache is keyed on hash, which mixes platform, so cross-compiles
	// don't collide with the operator's native build.
	if _, err := os.Stat(binPath); err != nil {
		if err := compileForPlatform(sparkwingDir, binPath, p); err != nil {
			return publishedBinary{}, fmt.Errorf("compile: %w", err)
		}
	}

	if err := bincache.UploadToArtifactStore(ctx, store, key, binPath); err != nil {
		return publishedBinary{}, err
	}

	st, _ := os.Stat(binPath)
	var size int64
	if st != nil {
		size = st.Size()
	}
	return publishedBinary{
		Key:        key,
		Platform:   p.label(),
		SizeBytes:  size,
		UploadedTo: strings.TrimRight(storeURL, "/") + "/bin/" + key,
	}, nil
}

// compileForPlatform shells out to `go build` with GOOS+GOARCH set.
// bincache.CompilePipeline reads from os.Environ at exec time, so a
// pair of os.Setenv calls before invoking it is enough -- but we
// inline a thin go build invocation here for clarity since publish
// is the only caller that cross-compiles.
func compileForPlatform(sparkwingDir, dest string, p platform) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	args := []string{"build"}
	if overlay := overlayModfilePath(sparkwingDir); overlay != "" {
		args = append(args, "-modfile="+overlay)
	}
	args = append(args, "-o", dest, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = sparkwingDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GOOS="+p.OS, "GOARCH="+p.Arch)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s/%s: %w", p.OS, p.Arch, err)
	}
	return nil
}

// overlayModfilePath mirrors the helper in bincache (kept private
// there). Duplicated here so this package doesn't have to widen
// its public surface for one caller.
func overlayModfilePath(sparkwingDir string) string {
	p := filepath.Join(sparkwingDir, ".resolved.mod")
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}

// resolveArtifactStoreURL picks the storage URL to publish to.
// Explicit --artifact-store URL beats --on profile's field;
// returning "" means neither was provided.
func resolveArtifactStoreURL(on, urlFlag string) (string, error) {
	if urlFlag != "" {
		return urlFlag, nil
	}
	if on == "" {
		return "", nil
	}
	prof, err := resolveProfile(on)
	if err != nil {
		return "", err
	}
	return prof.ArtifactStore, nil
}

func renderPublishResults(rows []publishedBinary, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case "plain":
		for _, r := range rows {
			fmt.Println(r.UploadedTo)
		}
		return nil
	default:
		// table: stable column order so agents that grep for fields
		// don't break across releases.
		sort.Slice(rows, func(i, j int) bool { return rows[i].Platform < rows[j].Platform })
		fmt.Printf("%-20s  %-8s  %s\n", "PLATFORM", "SIZE", "URL")
		for _, r := range rows {
			fmt.Printf("%-20s  %-8s  %s\n",
				r.Platform, humanSize(r.SizeBytes), r.UploadedTo)
		}
		return nil
	}
}

// humanSize is a small bytes->KiB/MiB formatter so the table output
// shows useful magnitudes without a third-party dep.
func humanSize(b int64) string {
	const (
		kib = 1024
		mib = 1024 * 1024
	)
	switch {
	case b >= mib:
		return fmt.Sprintf("%.1fM", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.1fK", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
