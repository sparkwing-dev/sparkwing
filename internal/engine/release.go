package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// VerifyRelease authenticates bytes and inspects their build identity without executing them.
func VerifyRelease(version string, target releaseasset.Target, bundle ReleaseBundle, keys []ed25519.PublicKey) (VerifiedRelease, error) {
	asset, err := releaseasset.VerifyForVersion(keys, version, bundle.Manifest, bundle.ManifestSignature, target, bundle.Asset, bundle.AssetSignature)
	if errors.Is(err, releaseasset.ErrUnexpectedLayout) && semver.Compare(version, firstEngineRelease+"-0") < 0 {
		return VerifiedRelease{}, predatesEngine(version, err)
	}
	if err != nil {
		return VerifiedRelease{}, integrity(err)
	}
	info, err := buildinfo.Read(bytes.NewReader(bundle.Asset))
	if err != nil {
		return VerifiedRelease{}, integrity(fmt.Errorf("read executable identity: %w", err))
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != target.GOOS || settings["GOARCH"] != target.GOARCH || info.Path != GoModule+"/cmd/"+string(target.Binary) {
		return VerifiedRelease{}, integrity(fmt.Errorf("executable binary or platform does not match release target"))
	}
	embedded, err := executableVersion(info)
	if err != nil {
		return VerifiedRelease{}, integrity(err)
	}
	if embedded != version {
		return VerifiedRelease{}, integrity(fmt.Errorf("embedded release version %q does not match %s", embedded, version))
	}
	copyBundle := ReleaseBundle{Manifest: bytes.Clone(bundle.Manifest), ManifestSignature: bytes.Clone(bundle.ManifestSignature), Asset: bytes.Clone(bundle.Asset), AssetSignature: bytes.Clone(bundle.AssetSignature)}
	return VerifiedRelease{version: version, target: target, asset: asset, bundle: copyBundle}, nil
}

// safety: earlier releases do not speak the node protocol this CLI hosts, and their
// authenticated manifests list executables this build no longer expects.
const firstEngineRelease = "v0.68.0"

func predatesEngine(version string, cause error) error {
	return &apierr.Error{
		Code:    apierr.VersionUnsupported,
		Message: fmt.Sprintf("SDK pin %s is older than %s, the oldest release this CLI can run as a pipeline engine; its release was not executed", version, firstEngineRelease),
		Hint:    "Run `" + UpgradeCommand + "` in this repository to move the pin to the latest release, then fix the pipeline code it reports; see https://sparkwing.dev/docs/migration-guide/",
		Details: map[string]any{"version": version, "minimum": firstEngineRelease, "cause": cause.Error()},
	}
}

func executableVersion(info *debug.BuildInfo) (string, error) {
	embedded := info.Main.Version
	for _, setting := range info.Settings {
		if setting.Key != "-ldflags" {
			continue
		}
		for _, field := range strings.Fields(setting.Value) {
			if candidate, ok := strings.CutPrefix(field, "main.Version="); ok {
				candidate = strings.Trim(candidate, "'\"")
				if embedded != "" && embedded != "(devel)" && embedded != candidate {
					return "", fmt.Errorf("conflicting embedded release versions")
				}
				embedded = candidate
			}
		}
	}
	return embedded, nil
}

// Version returns the verified release identity.
func (v VerifiedRelease) Version() string { return v.version }

// Target returns the verified executable target.
func (v VerifiedRelease) Target() releaseasset.Target { return v.target }

// Asset returns immutable verified release bytes for installation.
func (v VerifiedRelease) Asset() releaseasset.Verified { return v.asset }

func (r *Resolver) fetchRelease(ctx context.Context, version string, target releaseasset.Target) (VerifiedRelease, error) {
	name, err := target.NameForVersion(version)
	if err != nil {
		return VerifiedRelease{}, err
	}
	base := ReleaseBase + "/" + version + "/"
	var bundle ReleaseBundle
	get := func(name string, body *[]byte) error {
		fetched, err := r.fetch(ctx, base+name, true)
		if errors.Is(err, errNotPublished) {
			return unpublishedRelease(version, err)
		}
		*body = fetched
		return err
	}
	for _, item := range []struct {
		name string
		body *[]byte
	}{{"SHA256SUMS", &bundle.Manifest}, {"SHA256SUMS.sig", &bundle.ManifestSignature}, {name + ".sig", &bundle.AssetSignature}} {
		if err := get(item.name, item.body); err != nil {
			return VerifiedRelease{}, err
		}
	}
	// perf: the installed CLI is usually the pinned release itself, and its bytes pass the same signature
	// checks, so a first run skips downloading the same 70 MB executable again.
	if bundle.Asset = r.installedAsset(version, target); bundle.Asset != nil {
		if verified, err := VerifyRelease(version, target, bundle, r.config.TrustedKeys); err == nil {
			return verified, nil
		}
	}
	if err := get(name, &bundle.Asset); err != nil {
		return VerifiedRelease{}, err
	}
	return VerifyRelease(version, target, bundle, r.config.TrustedKeys)
}

func (r *Resolver) installedAsset(version string, target releaseasset.Target) []byte {
	if r.installed == nil || target.Binary != releaseasset.Sparkwing || target.GOOS != runtime.GOOS || target.GOARCH != runtime.GOARCH {
		return nil
	}
	path, err := r.installed()
	if err != nil {
		return nil
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return nil
	}
	if embedded, err := executableVersion(info); err != nil || embedded != version {
		return nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return body
}

func (r *Resolver) fetch(ctx context.Context, address string, release bool) ([]byte, error) {
	client := *r.config.HTTPClient
	priorCheck := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !allowedURL(req.URL, release) {
			return fmt.Errorf("refused redirect outside official release origins")
		}
		if priorCheck != nil {
			return priorCheck(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many download redirects")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	if !allowedURL(request.URL, release) {
		return nil, fmt.Errorf("refused unofficial download origin")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("official download returned HTTP %d: %w", response.StatusCode, errNotPublished)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official download returned HTTP %d", response.StatusCode)
	}
	return body, nil
}

// safety: both origins answer a version they never published with 403 or 404, so
// the fix the hint names is the pin rather than the network.
var errNotPublished = errors.New("the origin does not publish it")

func unpublishedRelease(version string, err error) error {
	hint := "Pin the pipeline to a published release: `sparkwing upgrade --version <release>` in the repository, or `go -C .sparkwing get " + GoModule + "@<release>`."
	if QATrust {
		hint = "This is a QA build, which installs engines only from " + indexHost + ", and that origin holds only -qa.N versions. " +
			"Pin the pipeline to one, for example `sparkwing upgrade --version vX.Y.Z-qa.N`, or run a production sparkwing build for released pins."
	}
	return &apierr.Error{Code: apierr.NotFound, Message: fmt.Sprintf("engine %s is not published at %s (%v)", version, ReleaseBase, err), Hint: hint}
}

func allowedURL(u *url.URL, release bool) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	if !release {
		return u.Host == indexHost && (u.Path == "/versions.json" || u.Path == "/versions.json.sig")
	}
	return releaseAssetURL(u)
}
