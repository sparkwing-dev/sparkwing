package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	sdkDigestMarker = "SDK-Source-SHA256: "
)

type sdkReleaseDecision struct {
	Version string
	Digest  string
	Changed bool
}

// safety: hash committed bytes, including embeds and fixtures, so checkout state cannot change a tag's identity.
func sdkSourceDigest(ctx context.Context, repo, ref string) (string, error) {
	listed, err := runGitIn(ctx, repo, "ls-tree", "-rz", "--full-tree", ref, "--", "sparkwing/", "LICENSE")
	if err != nil {
		return "", err
	}
	hasSDKLicense := strings.Contains(listed, "\tsparkwing/LICENSE\x00")
	entries := strings.Split(strings.TrimSuffix(listed, "\x00"), "\x00")
	sort.Slice(entries, func(i, j int) bool {
		_, a, _ := strings.Cut(entries[i], "\t")
		_, b, _ := strings.Cut(entries[j], "\t")
		return strings.TrimPrefix(a, "sparkwing/") < strings.TrimPrefix(b, "sparkwing/")
	})
	h := sha256.New()
	hasModule := false
	for _, entry := range entries {
		metadata, name, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		if name == "LICENSE" && hasSDKLicense {
			continue
		}
		relative := strings.TrimPrefix(name, "sparkwing/")
		if !sdkPublishablePath(relative) {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 || fields[1] != "blob" || fields[0] == "120000" {
			return "", fmt.Errorf("SDK source contains unsupported entry %q", name)
		}
		body, err := runGitIn(ctx, repo, "cat-file", "blob", fields[2])
		if err != nil {
			return "", err
		}
		if relative == "go.mod" {
			hasModule = true
		}
		fmt.Fprintf(h, "%d:%s%d:", len(relative), relative, len(body))
		h.Write([]byte(body))
	}
	if !hasModule {
		return "", errors.New("SDK release requires sparkwing/go.mod")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sdkPublishablePath(name string) bool {
	for _, part := range strings.Split(name, "/") {
		switch part {
		case ".git", "node_modules", "vendor", ".cache":
			return false
		}
	}
	if strings.HasPrefix(name, "dist/") || strings.HasPrefix(name, "build/") || name == "go.work" || name == "go.work.sum" || path.Base(name) == ".DS_Store" {
		return false
	}
	return true
}

func decideSDKRelease(digest, latest, recorded string) (sdkReleaseDecision, error) {
	if latest != "" {
		if err := validateReleaseVersion(latest); err != nil {
			return sdkReleaseDecision{}, err
		}
		if len(recorded) != sha256.Size*2 {
			return sdkReleaseDecision{}, errors.New("latest SDK tag has no valid recorded source digest")
		}
		if digest == recorded {
			return sdkReleaseDecision{Version: latest, Digest: digest}, nil
		}
	}
	version := "v0.1.0"
	if latest != "" {
		var err error
		version, err = bumpVersion(latest, "minor")
		if err != nil {
			return sdkReleaseDecision{}, err
		}
	}
	return sdkReleaseDecision{Version: version, Digest: digest, Changed: true}, nil
}

func selectSDKRelease(ctx context.Context, repo, source string) (sdkReleaseDecision, error) {
	digest, err := sdkSourceDigest(ctx, repo, source)
	if err != nil {
		return sdkReleaseDecision{}, err
	}
	tags, err := remoteReleaseTags(ctx, repo)
	if err != nil {
		return sdkReleaseDecision{}, err
	}
	latest := ""
	for _, tag := range tags {
		version, ok := strings.CutPrefix(tag, "sparkwing/")
		if ok && onReleaseLine(version) && semver.Prerelease(version) == "" && (latest == "" || semver.Compare(version, latest) > 0) {
			latest = version
		}
	}
	recorded := ""
	if latest != "" {
		tag := "sparkwing/" + latest
		ref := "refs/tags/" + tag
		if _, err := runGitIn(ctx, repo, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "origin", ref+":"+ref); err != nil {
			return sdkReleaseDecision{}, fmt.Errorf("fetch immutable SDK tag %s: %w", tag, err)
		}
		message, err := runGitIn(ctx, repo, "for-each-ref", "--format=%(contents)", "refs/tags/"+tag)
		if err != nil {
			return sdkReleaseDecision{}, err
		}
		recorded = recordedSDKDigest(message)
		actual, err := sdkSourceDigest(ctx, repo, "refs/tags/"+tag)
		if err != nil {
			return sdkReleaseDecision{}, err
		}
		if recorded != actual {
			return sdkReleaseDecision{}, fmt.Errorf("immutable SDK tag %s digest does not match its source", tag)
		}
	}
	return decideSDKRelease(digest, latest, recorded)
}

type releaseSDKJob struct {
	sparkwing.Base
	sparkwing.Produces[string]
	RepoDir string
	Source  string
}

func (j *releaseSDKJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "publish-sdk", func(ctx context.Context) (string, error) { return j.run(ctx, false) }).DryRun(func(ctx context.Context) error { _, err := j.run(ctx, true); return err }).Risk("destructive", "prod"), nil
}

func (j *releaseSDKJob) run(ctx context.Context, dry bool) (string, error) {
	decision, err := selectSDKRelease(ctx, j.RepoDir, j.Source)
	if err != nil {
		return "", err
	}
	sparkwing.Info(ctx, "SDK %s source sha256=%s changed=%t", decision.Version, decision.Digest, decision.Changed)
	if dry {
		return decision.Version, nil
	}
	if decision.Changed {
		if err := checkSDKReleaseSource(ctx, j.RepoDir); err != nil {
			return "", err
		}
		digest, err := sdkSourceDigest(ctx, j.RepoDir, j.Source)
		if err != nil {
			return "", err
		}
		if digest != decision.Digest {
			return "", errors.New("SDK source changed during verification")
		}
		status, err := runGitIn(ctx, j.RepoDir, "status", "--porcelain", "--", "sparkwing")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(status) != "" {
			return "", errors.New("SDK verification modified source; commit the changes and retry")
		}
		tag := "sparkwing/" + decision.Version
		exists, err := tagExistsOnRemote(ctx, j.RepoDir, tag)
		if err != nil {
			return "", err
		}
		if exists {
			return "", fmt.Errorf("SDK tag %s appeared during verification; retry to compare its digest", tag)
		}
		local, err := runGitIn(ctx, j.RepoDir, "tag", "--list", tag)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(local) != "" {
			existing, err := sdkSourceDigest(ctx, j.RepoDir, "refs/tags/"+tag)
			if err != nil {
				return "", err
			}
			message, err := runGitIn(ctx, j.RepoDir, "for-each-ref", "--format=%(contents)", "refs/tags/"+tag)
			if err != nil {
				return "", err
			}
			if existing != decision.Digest || recordedSDKDigest(message) != decision.Digest {
				return "", fmt.Errorf("immutable SDK tag %s has different source", tag)
			}
		} else if _, err := runGitIn(ctx, j.RepoDir, "tag", "-a", tag, j.Source, "-m", "SDK "+decision.Version+"\n\n"+sdkDigestMarker+decision.Digest); err != nil {
			return "", err
		}
		if _, err := runGitIn(ctx, j.RepoDir, "push", "origin", "refs/tags/"+tag); err != nil {
			return "", err
		}
	}
	proxyCache, err := os.MkdirTemp("", "sparkwing-sdk-proxy-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err := os.RemoveAll(proxyCache); err != nil {
			sparkwing.Warn(ctx, "remove SDK proxy cache %s: %v", proxyCache, err)
		}
	}()
	// safety: the engine must never require a module the public Go proxy cannot serve.
	if _, err := sparkwing.Exec(ctx, "go", "mod", "download", sdkModulePath+"@"+decision.Version).Dir(j.RepoDir).
		Env("GOMODCACHE", proxyCache).Env("GOWORK", "off").Env("GOPROXY", "https://proxy.golang.org").Env("GONOPROXY", "none").Env("GOPRIVATE", "").Run(); err != nil {
		return "", fmt.Errorf("SDK %s published but proxy unavailable; retry the cut without moving its tag: %w", decision.Version, err)
	}
	return decision.Version, nil
}

func checkSDKReleaseSource(ctx context.Context, repo string) error {
	run := func(flags, command string, args ...string) error {
		_, err := sparkwing.Exec(ctx, command, args...).Dir(repo).Env("GOWORK", "off").Env("GOFLAGS", flags).Run()
		return err
	}
	if err := run("-trimpath", "go", "-C", filepath.Join(repo, "sparkwing"), "test", "-p", "1", "./..."); err != nil {
		return err
	}
	if err := run("-trimpath", "npm", "--prefix", filepath.Join(repo, "sdk/typescript"), "ci", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "sparkwing-sdk-conformance-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(scratch); err != nil {
			sparkwing.Warn(ctx, "remove SDK conformance modfile %s: %v", scratch, err)
		}
	}()
	for _, name := range []string{"go.mod", "go.sum"} {
		body, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(scratch, name), body, 0o600); err != nil {
			return err
		}
	}
	modfile := filepath.Join(scratch, "go.mod")
	// safety: conformance must compile the tested SDK source rather than its last published version.
	if err := run("-trimpath", "go", "mod", "edit", "-modfile", modfile, "-replace", sdkModulePath+"="+filepath.Join(repo, "sparkwing")); err != nil {
		return err
	}
	if err := run("-trimpath -modfile="+modfile, "go", "test", "-p", "1", "./internal/nodeproto/conformance"); err != nil {
		return err
	}
	return run("-trimpath -modfile="+modfile, "go", "test", "-p", "1", "./sdk/typescript/test/nativehost", "-run", "TestNativeConformance")
}

func recordedSDKDigest(message string) string {
	digest := ""
	for _, line := range strings.Split(message, "\n") {
		if value, ok := strings.CutPrefix(line, sdkDigestMarker); ok {
			if digest != "" {
				return ""
			}
			digest = strings.TrimSpace(value)
		}
	}
	return digest
}
