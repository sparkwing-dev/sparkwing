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
	"runtime/debug"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// VerifyRelease authenticates bytes and inspects their build identity without executing them.
func VerifyRelease(version string, target releaseasset.Target, bundle ReleaseBundle, keys []ed25519.PublicKey) (VerifiedRelease, error) {
	asset, err := releaseasset.VerifyForVersion(keys, version, bundle.Manifest, bundle.ManifestSignature, target, bundle.Asset, bundle.AssetSignature)
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
	for _, item := range []struct {
		name string
		body *[]byte
	}{{"SHA256SUMS", &bundle.Manifest}, {"SHA256SUMS.sig", &bundle.ManifestSignature}, {name, &bundle.Asset}, {name + ".sig", &bundle.AssetSignature}} {
		body, err := r.fetch(ctx, base+item.name, true)
		var status downloadStatus
		if errors.As(err, &status) && (status == http.StatusNotFound || status == http.StatusForbidden) {
			return VerifiedRelease{}, unpublishedRelease(version, int(status))
		}
		if err != nil {
			return VerifiedRelease{}, err
		}
		*item.body = body
	}
	return VerifyRelease(version, target, bundle, r.config.TrustedKeys)
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
	if response.StatusCode != http.StatusOK {
		return nil, downloadStatus(response.StatusCode)
	}
	return body, nil
}

// downloadStatus is an official origin's non-200 answer.
type downloadStatus int

func (s downloadStatus) Error() string { return fmt.Sprintf("official download returned HTTP %d", int(s)) }

// safety: both origins answer a version they never published with 403 or 404, so
// the fix the hint names is the pin rather than the network.
func unpublishedRelease(version string, status int) error {
	hint := "Pin the pipeline to a published release: `sparkwing upgrade --version <release>` in the repository, or `go -C .sparkwing get " + GoModule + "@<release>`."
	if QATrust {
		hint = "This is a QA build, which installs engines only from " + indexHost + ", and that origin holds only -qa.N versions. " +
			"Pin the pipeline to one, for example `sparkwing upgrade --version vX.Y.Z-qa.N`, or run a production sparkwing build for released pins."
	}
	return &apierr.Error{Code: apierr.NotFound, Message: fmt.Sprintf("engine %s is not published at %s (HTTP %d)", version, ReleaseBase, status), Hint: hint}
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
