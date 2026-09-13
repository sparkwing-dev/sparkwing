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
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
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
		"profile name; uploads to the backend its cache surface serves binaries from")
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

	store, storeLocation, err := resolveArtifactStore(context.Background(), *on, *artifactStore)
	if err != nil {
		return err
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
		row, err := compileAndPublishOne(context.Background(), dir, p, store, storeLocation)
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

func compileAndPublishOne(ctx context.Context, sparkwingDir string, p platform, store storage.ArtifactStore, storeLocation string) (publishedBinary, error) {
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
		UploadedTo: strings.TrimRight(storeLocation, "/") + "/bin/" + key,
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

func resolveArtifactStore(ctx context.Context, profileName, urlFlag string) (storage.ArtifactStore, string, error) {
	if urlFlag != "" {
		store, err := storeurl.OpenArtifactStore(ctx, urlFlag)
		if err != nil {
			return nil, "", fmt.Errorf("open artifact-store: %w", err)
		}
		return store, urlFlag, nil
	}
	if profileName == "" {
		return nil, "", errors.New("pipeline publish: no artifact-store configured. Pass --profile PROFILE (with a cache surface) or --artifact-store URL")
	}
	p, err := resolveProfile(profileName)
	if err != nil {
		return nil, "", err
	}
	spec := p.Surfaces().BinaryCache()
	if spec == nil && p.ControllerURL() != "" {
		spec = &backends.Spec{Type: backends.TypeController, Controller: p.Name}
	}
	if spec == nil {
		return nil, "", fmt.Errorf("pipeline publish: profile %q serves binaries from nowhere -- it declares neither a cache surface nor a controller. Pass --artifact-store URL", profileName)
	}
	location, err := artifactStoreURL(p, *spec)
	if err != nil {
		return nil, "", err
	}
	store, err := storeurl.OpenArtifactStoreFromSpec(ctx, *spec, controllerLookup(p))
	if err != nil {
		return nil, "", fmt.Errorf("open artifact-store: %w", err)
	}
	return store, location, nil
}

// safety: the reported location is the same URL vocabulary --artifact-store
// accepts, so what publish prints can be handed back to the flag.
func artifactStoreURL(p *profile.Profile, spec backends.Spec) (string, error) {
	switch spec.Type {
	case backends.TypeFilesystem:
		// safety: fs:// takes an absolute or ~-rooted path, and a spec path is
		// opened relative to the working directory, so anything else is resolved
		// against the same directory the upload used.
		path := spec.Path
		if !strings.HasPrefix(path, "~") {
			abs, err := filepath.Abs(path)
			if err != nil {
				return "", fmt.Errorf("pipeline publish: profile %q cache path %q: %w", p.Name, path, err)
			}
			path = abs
		}
		return "fs://" + path, nil
	case backends.TypeS3:
		location := "s3://" + spec.Bucket
		if prefix := strings.Trim(spec.Prefix, "/"); prefix != "" {
			location += "/" + prefix
		}
		return location, nil
	case backends.TypeController:
		if url := p.ControllerURL(); url != "" {
			return url, nil
		}
	}
	return "", fmt.Errorf("pipeline publish: profile %q serves binaries from %s, which has no artifact-store URL. Pass --artifact-store URL",
		p.Name, profile.SpecString(&spec))
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
