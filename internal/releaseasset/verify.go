package releaseasset

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

const (
	probeTimeout   = 5 * time.Second
	probeWaitDelay = time.Second
	maxProbeOutput = 64 << 10
)

// Verified is an asset whose bytes match both trusted signatures and the
// signed checksum manifest. Callers cannot construct this capability.
type Verified struct {
	name              string
	bytes             []byte
	digest            string
	manifest          []byte
	manifestSignature []byte
}

// Name returns the authenticated release asset name.
func (asset Verified) Name() string { return asset.name }

// Bytes returns a copy of the authenticated executable bytes.
func (asset Verified) Bytes() []byte { return bytes.Clone(asset.bytes) }

// Digest returns the authenticated lowercase SHA-256 digest.
func (asset Verified) Digest() string { return asset.digest }

// Manifest returns a copy of the signed release manifest.
func (asset Verified) Manifest() []byte { return bytes.Clone(asset.manifest) }

// ManifestSignature returns a copy of the trusted manifest signature.
func (asset Verified) ManifestSignature() []byte { return bytes.Clone(asset.manifestSignature) }

// Verify requires one trusted key to sign both the manifest and asset bytes.
func Verify(publicKeys []ed25519.PublicKey, manifest, manifestSignature []byte, target Target, asset, assetSignature []byte) (Verified, error) {
	name, err := target.Name()
	if err != nil {
		return Verified{}, err
	}
	for _, publicKey := range publicKeys {
		verified, verifyErr := verifyWithKey(publicKey, manifest, manifestSignature, name, asset, assetSignature)
		if verifyErr == nil {
			return verified, nil
		}
	}
	return Verified{}, errors.New("release signatures do not match the updater trust set")
}

func verifyWithKey(publicKey ed25519.PublicKey, manifest, manifestSignature []byte, name string, asset, assetSignature []byte) (Verified, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return Verified{}, errors.New("release public key is invalid")
	}
	if !ed25519.Verify(publicKey, manifest, manifestSignature) {
		return Verified{}, errors.New("SHA256SUMS signature is invalid")
	}
	if !ed25519.Verify(publicKey, asset, assetSignature) {
		return Verified{}, fmt.Errorf("signature is invalid for %s", name)
	}
	digest, err := ManifestDigest(manifest, name)
	if err != nil {
		return Verified{}, err
	}
	actual := sha256.Sum256(asset)
	actualHex := hex.EncodeToString(actual[:])
	if digest != actualHex {
		return Verified{}, fmt.Errorf("checksum mismatch for %s", name)
	}
	return Verified{
		name:              name,
		bytes:             bytes.Clone(asset),
		digest:            digest,
		manifest:          bytes.Clone(manifest),
		manifestSignature: bytes.Clone(manifestSignature),
	}, nil
}

// ManifestSignedBy reports whether a manifest signature matches the trust set.
func ManifestSignedBy(publicKeys []ed25519.PublicKey, manifest, signature []byte) bool {
	for _, key := range publicKeys {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, manifest, signature) {
			return true
		}
	}
	return false
}

// ManifestDigest returns one unambiguous lowercase SHA-256 digest.
func ManifestDigest(manifest []byte, assetName string) (string, error) {
	var digest string
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != assetName {
			continue
		}
		if digest != "" {
			return "", fmt.Errorf("duplicate %s entry in SHA256SUMS", assetName)
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", fmt.Errorf("malformed SHA-256 digest for %s", assetName)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", fmt.Errorf("malformed SHA-256 digest for %s: %w", assetName, err)
		}
		digest = strings.ToLower(fields[0])
	}
	if digest == "" {
		return "", errors.New(assetName + " not listed in SHA256SUMS")
	}
	return digest, nil
}

// VerifyExecutableIdentity stages authenticated bytes in a private directory,
// then probes their offline identity with bounded runtime, output, and environment.
func (asset Verified) VerifyExecutableIdentity(target Target, version string) (buildinfo.Identity, error) {
	name, err := target.Name()
	if err != nil {
		return buildinfo.Identity{}, err
	}
	if asset.name != name {
		return buildinfo.Identity{}, fmt.Errorf("verified asset is %q, want %q", asset.name, name)
	}
	if target.GOOS != runtime.GOOS || target.GOARCH != runtime.GOARCH {
		return buildinfo.Identity{}, fmt.Errorf("cannot probe %s executable on this %s/%s host", name,
			runtime.GOOS, runtime.GOARCH)
	}
	path, cleanup, err := asset.stageProbe(name)
	if err != nil {
		return buildinfo.Identity{}, err
	}
	defer cleanup()
	return asset.probeStagedIdentity(path, target, version, nil)
}

func (asset Verified) stageProbe(name string) (string, func(), error) {
	directory, err := fssecure.MkdirPrivateTemp("", ".sparkwing-release-probe-")
	if err != nil {
		return "", nil, fmt.Errorf("create private %s probe directory: %w", name, err)
	}
	path := filepath.Join(directory, name)
	directoryIdentity, err := os.Lstat(directory)
	if err != nil {
		_ = os.Remove(directory)
		return "", nil, fmt.Errorf("inspect private %s probe directory: %w", name, err)
	}
	// Cleanup touches the child only while its secured parent retains the same
	// identity, then uses non-recursive removes so replacements are never walked.
	cleanup := func() {
		current, currentErr := os.Lstat(directory)
		if currentErr == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 &&
			os.SameFile(directoryIdentity, current) {
			_ = os.Remove(path)
		}
		_ = os.Remove(directory)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("stage %s identity probe: %w", name, err)
	}
	if _, err := file.Write(asset.bytes); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("stage %s identity probe: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("sync %s identity probe: %w", name, err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close %s identity probe: %w", name, err)
	}
	return path, cleanup, nil
}

func (asset Verified) probeStagedIdentity(path string, target Target, version string, beforeExec func(string)) (buildinfo.Identity, error) {
	name, err := target.Name()
	if err != nil {
		return buildinfo.Identity{}, err
	}
	initial, err := regularFileIdentity(path)
	if err != nil {
		return buildinfo.Identity{}, fmt.Errorf("inspect staged %s: %w", name, err)
	}
	digest, err := digestFile(path)
	if err != nil {
		return buildinfo.Identity{}, fmt.Errorf("hash staged %s: %w", name, err)
	}
	if digest != asset.digest {
		return buildinfo.Identity{}, fmt.Errorf("staged %s digest mismatch: got %s want %s", name, digest, asset.digest)
	}
	if beforeExec != nil {
		beforeExec(path)
	}
	final, err := regularFileIdentity(path)
	if err != nil {
		return buildinfo.Identity{}, fmt.Errorf("reinspect staged %s: %w", name, err)
	}
	if !os.SameFile(initial, final) {
		return buildinfo.Identity{}, fmt.Errorf("staged %s was replaced after verification", name)
	}
	digest, err = digestFile(path)
	if err != nil {
		return buildinfo.Identity{}, fmt.Errorf("rehash staged %s: %w", name, err)
	}
	if digest != asset.digest {
		return buildinfo.Identity{}, fmt.Errorf("staged %s changed after verification: got digest %s want %s", name, digest, asset.digest)
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var stdout, stderr boundedBuffer
	stdout.limit = maxProbeOutput
	stderr.limit = maxProbeOutput
	// #nosec G702 -- signatures and digest authenticate these bytes before the constrained offline identity probe.
	cmd := exec.CommandContext(ctx, path, "version", "-o", "json", "--offline")
	cmd.Dir = filepath.Dir(path)
	cmd.Env = []string{}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = probeWaitDelay
	if err := runProbeProcess(ctx, cmd, probeWaitDelay); err != nil {
		if ctx.Err() != nil {
			return buildinfo.Identity{}, fmt.Errorf("probe %s identity: %w", name, ctx.Err())
		}
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return buildinfo.Identity{}, fmt.Errorf("probe %s identity: %w: %s", name, err, detail)
		}
		return buildinfo.Identity{}, fmt.Errorf("probe %s identity: %w", name, err)
	}
	var identity buildinfo.Identity
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(&identity); err != nil {
		return buildinfo.Identity{}, fmt.Errorf("decode %s identity: %w", name, err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return buildinfo.Identity{}, fmt.Errorf("decode %s identity: %w", name, err)
	}
	expected := buildinfo.Expectation{
		Binary: string(target.Binary), Version: version,
		GOOS: target.GOOS, GOARCH: target.GOARCH,
	}
	if err := buildinfo.Verify(identity, expected); err != nil {
		return buildinfo.Identity{}, fmt.Errorf("verify %s identity: %w", name, err)
	}
	return identity, nil
}

func regularFileIdentity(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symbolic link is not an executable asset")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("mode %s is not a regular executable asset", info.Mode())
	}
	return info, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *boundedBuffer) Write(body []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		return 0, errors.New("identity output exceeds limit")
	}
	if len(body) > remaining {
		_, _ = buffer.buffer.Write(body[:remaining])
		return remaining, errors.New("identity output exceeds limit")
	}
	return buffer.buffer.Write(body)
}

func (buffer *boundedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }
func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }
