package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

type publishedBinary struct {
	Key        string `json:"key"`
	Platform   string `json:"platform"`
	SizeBytes  int64  `json:"size_bytes"`
	UploadedTo string `json:"uploaded_to"`
}

func runPipelinePublish(args []string) error {
	fs := flag.NewFlagSet("pipeline publish", flag.ContinueOnError)
	on := fs.String("profile", "",
		"profile name; uses its artifact_store field as the upload target")
	artifactStore := fs.String("artifact-store", "",
		"artifact-store URL (fs:///path or s3://bucket/prefix). Overrides --profile.")
	platforms := fs.String("platform", "",
		"comma-separated GOOS/GOARCH pairs to cross-compile + publish "+
			"(e.g. linux/amd64,linux/arm64,darwin/arm64). Default: current platform.")
	sparkwingDirFlag := fs.String("dir", "",
		"path to .sparkwing/ (default: walk up from cwd)")
	output := fs.StringP("output", "o", "pretty", "output format: pretty | json | plain")
	if err := fs.Parse(args); err != nil {
		return err
	}

	storeURL, err := resolveArtifactStoreURL(*on, *artifactStore)
	if err != nil {
		return err
	}
	if storeURL == "" {
		return errors.New("pipeline publish: no artifact-store configured. Pass --profile PROFILE (with artifact_store set) or --artifact-store URL")
	}
	store, err := storeurl.OpenArtifactStore(context.Background(), storeURL)
	if err != nil {
		return fmt.Errorf("open artifact-store: %w", err)
	}

	dir := *sparkwingDirFlag
	if dir == "" {
		d, err := findSparkwingDir()
		if err != nil {
			return err
		}
		dir = d
	}

	platformsList, err := parsePlatforms(*platforms)
	if err != nil {
		return err
	}

	results := make([]publishedBinary, 0, len(platformsList))
	for _, p := range platformsList {
		row, err := compileAndPublishOne(context.Background(), dir, p, store, storeURL)
		if err != nil {
			return fmt.Errorf("publish %s: %w", p.label(), err)
		}
		results = append(results, row)
	}

	format := *output
	if format == "" || format == "table" {
		format = "pretty"
	}
	return renderPublishResults(results, format)
}

type platform struct {
	OS, Arch string
}

func (p platform) label() string { return p.OS + "/" + p.Arch }

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

func compileAndPublishOne(ctx context.Context, sparkwingDir string, p platform, store storage.ArtifactStore, storeURL string) (publishedBinary, error) {
	key, err := bincache.PipelineCacheKeyForPlatform(sparkwingDir, p.OS, p.Arch)
	if err != nil {
		return publishedBinary{}, fmt.Errorf("hash: %w", err)
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return publishedBinary{}, fmt.Errorf("cache entry: %w", err)
	}
	lease, _, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		return compileForPlatform(sparkwingDir, tempPath, p)
	})
	if err != nil {
		return publishedBinary{}, fmt.Errorf("compile: %w", err)
	}
	defer func() { _ = lease.Release() }()

	if err := bincache.UploadToArtifactStore(ctx, store, key, lease.Path()); err != nil {
		return publishedBinary{}, err
	}

	st, _ := os.Stat(lease.Path())
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

func compileForPlatform(sparkwingDir, dest string, p platform) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	args := []string{"build"}
	if overlay := overlayModfilePath(sparkwingDir); overlay != "" {
		if work, present := goWorkInScope(sparkwingDir); present {
			fmt.Fprintf(os.Stderr,
				"warning: %s in effect; skipping sparks resolution for %s/%s.\n",
				work, p.OS, p.Arch,
			)
		} else {
			args = append(args, "-modfile="+overlay)
		}
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

func overlayModfilePath(sparkwingDir string) string {
	p := filepath.Join(sparkwingDir, ".resolved.mod")
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}

func goWorkInScope(sparkwingDir string) (string, bool) {
	switch env := os.Getenv("GOWORK"); env {
	case "off":
		return "", false
	case "":
	default:
		// #nosec G703 -- the path comes from this user's own environment
		if fi, err := os.Stat(env); err == nil && fi.Mode().IsRegular() {
			return env, true
		}
		return "", false
	}
	dir := sparkwingDir
	for {
		candidate := filepath.Join(dir, "go.work")
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func resolveArtifactStoreURL(_, urlFlag string) (string, error) {
	return urlFlag, nil
}

func renderPublishResults(rows []publishedBinary, format string) error {
	switch format {
	case "json":

		return ndjson.Write(os.Stdout, rows)
	case "plain":
		for _, r := range rows {
			fmt.Println(r.UploadedTo)
		}
		return nil
	default:
		sort.Slice(rows, func(i, j int) bool { return rows[i].Platform < rows[j].Platform })
		fmt.Printf("%-20s  %-8s  %s\n", "PLATFORM", "SIZE", "URL")
		for _, r := range rows {
			fmt.Printf("%-20s  %-8s  %s\n",
				r.Platform, humanSize(r.SizeBytes), r.UploadedTo)
		}
		return nil
	}
}

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
