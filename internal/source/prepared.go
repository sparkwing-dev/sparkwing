package source

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

// Prepared binds a same-run installation to authenticated storage and the intake seal.
// Publish is reserved for the Cloud-awarded planning owner, before any pipeline code executes.
type Prepared struct {
	Bytes       blob.Store
	Records     blob.RecordStore
	Authority   blob.Authority
	Key         blob.RecordKey
	RequestHash string
	Publish     bool
}

type preparedInputs struct {
	Format      int
	RequestHash string
	Source      Spec
	Engine      engineid.Identity
	Target      releaseasset.Target
	Environment string
	Tools       string
	Snapshot    string
}

type preparedManifest struct {
	Inputs        preparedInputs
	Key           string
	Directory     string
	CodeRoot      string
	Main          string
	DefaultBranch string
}

func (p *Prepared) validate() error {
	if p.Bytes == nil || p.Records == nil || p.Key.Kind != blob.KindPrepared || p.RequestHash == "" || p.Key.Scope.Key == "" {
		return errors.New("prepared pipeline requires authenticated run storage and an intake seal")
	}
	return nil
}

func preparedSource(spec Spec) Spec {
	spec.Engine = nil
	// safety: Only the effective checkout policy affects acquisition and its fingerprint.
	spec.Checkout.Origins = nil
	return spec
}

func (p *Prepared) inputs(ctx context.Context, spec Spec, options Options, identity engineid.Identity, directory string) (preparedInputs, error) {
	environment, err := sourceEnvironment(options, procenv.Install)
	if err != nil {
		return preparedInputs{}, err
	}
	slices.Sort(environment)
	tools, err := preparedTools(ctx, environment, identity.Pin.SDK, directory)
	if err != nil {
		return preparedInputs{}, err
	}
	environmentHash, err := hashJSON(environment)
	if err != nil {
		return preparedInputs{}, err
	}
	return preparedInputs{Format: 1, RequestHash: p.RequestHash, Source: preparedSource(spec), Engine: identity, Target: options.Target, Environment: environmentHash, Tools: tools}, nil
}

func hashJSON(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func preparedTools(ctx context.Context, environment []string, sdk engineid.SDKKind, directory string) (string, error) {
	names := []string{"go"}
	if sdk == engineid.TypeScriptSDK {
		names = []string{"node", "npm"}
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	names = append(names, executable)
	hashes := make([]string, 0, len(names))
	for _, name := range names {
		resolved, err := exec.LookPath(name)
		if err != nil {
			return "", err
		}
		digest, err := fileDigest(ctx, resolved)
		if err != nil {
			return "", err
		}
		hashes = append(hashes, digest)
	}
	// safety: Go can select another toolchain; its compiler version and architecture settings
	// participate alongside the launcher image's tool executable bytes.
	if sdk == engineid.GoSDK {
		command := exec.CommandContext(ctx, "go", "env", "GOVERSION", "GOOS", "GOARCH", "GOEXPERIMENT", "GOAMD64", "GOARM64", "CGO_ENABLED", "CC", "CXX")
		command.Env, command.Dir = environment, directory
		body, err := command.Output()
		if err != nil {
			return "", fmt.Errorf("identify prepared Go toolchain: %w", err)
		}
		hashes = append(hashes, string(body))
	}
	return hashJSON(hashes)
}

func fileDigest(ctx context.Context, name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, preparedReader{ctx, file})
	if err := errors.Join(readErr, file.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type preparedReader struct {
	ctx context.Context
	io.Reader
}

func (r preparedReader) Read(dst []byte) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	return r.Reader.Read(dst)
}

func (p *Prepared) publish(ctx context.Context, spec Spec, options Options, entry *Entry) (err error) {
	if err := p.validate(); err != nil {
		return err
	}
	hit, err := p.Records.LookupRecord(ctx, blob.RecordLookup{Authority: p.Authority, Key: p.Key})
	if missingPrepared(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// safety: A replacement owner may build with different inputs. Keep the run index immutable;
	// consumers compare those inputs and build fresh when they differ.
	if hit.Found {
		return nil
	}
	spec.DefaultBranch = entry.DefaultBranch
	inputs, err := p.inputs(ctx, spec, options, entry.Engine.Identity, entry.Directory)
	if err != nil {
		return err
	}
	directory, err := filepath.Rel(entry.pipelineCheckout, entry.Directory)
	if err != nil || !filepath.IsLocal(directory) {
		return errors.New("prepared pipeline directory escapes its source")
	}
	manifest := preparedManifest{Inputs: inputs, Directory: directory, CodeRoot: "checkout", DefaultBranch: entry.DefaultBranch}
	if entry.pipelineCheckout != entry.checkoutDirectory {
		manifest.CodeRoot = "code"
	}
	if inputs.Engine.Pin.SDK == engineid.GoSDK {
		if len(entry.Command) != 1 {
			return errors.New("prepared Go pipeline requires one executable")
		}
	} else {
		if len(entry.Command) != 2 {
			return errors.New("prepared TypeScript pipeline requires Node and a main entry")
		}
		manifest.Main, err = filepath.Rel(entry.Directory, entry.Command[1])
		if err != nil || !filepath.IsLocal(manifest.Main) {
			return errors.New("prepared TypeScript main escapes its pipeline")
		}
	}
	if err := fssecure.EnsureDir(options.Workspace); err != nil {
		return err
	}
	archive, err := os.CreateTemp(options.Workspace, ".prepared-*.tar.gz")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, archive.Close(), os.Remove(archive.Name())) }()
	// safety: The payload digest covers every checkout/install byte, including local replaces,
	// submodules, LFS objects and the separately pinned pipeline checkout.
	digest := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(archive, digest))
	snapshot := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(gz, snapshot))
	// Snapshot is a publisher-declared fingerprint, authenticated with the archive bytes.
	roots := []struct{ name, path string }{{"checkout", entry.checkoutDirectory}}
	if manifest.CodeRoot == "code" {
		roots = append(roots, struct{ name, path string }{"code", entry.pipelineCheckout})
	}
	for _, tree := range roots {
		root, openErr := os.OpenRoot(tree.path)
		captureErr := openErr
		if captureErr == nil {
			captureErr = errors.Join(tarsafe.WriteTarRoot(ctx, tw, root, tree.name), root.Close())
		}
		if captureErr != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			slog.InfoContext(ctx, "skipping prepared pipeline publication: checkout cannot be captured safely", "error", captureErr)
			return nil
		}
	}
	if inputs.Engine.Pin.SDK == engineid.GoSDK {
		if err := preparedFile(ctx, tw, "pipeline", entry.Command[0]); err != nil {
			return err
		}
	}
	manifest.Inputs.Snapshot = hex.EncodeToString(snapshot.Sum(nil))
	manifest.Key, err = hashJSON(manifest.Inputs)
	if err != nil {
		return err
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "prepared.json", Mode: 0o600, Size: int64(len(body))}); err != nil {
		return err
	}
	if _, err := tw.Write(body); err != nil {
		return err
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		return err
	}
	size, err := archive.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	sum := blob.Digest(hex.EncodeToString(digest.Sum(nil)))
	if _, err := p.Bytes.Put(ctx, blob.PutRequest{Authority: p.Authority, Identity: p.Authority.Upload.Value, Digest: sum, Size: size, Body: archive}); err != nil {
		return err
	}
	_, err = p.Records.CommitRecord(ctx, blob.CommitRecordRequest{Authority: p.Authority, Source: p.Authority.Upload.Value, Key: p.Key, Digest: sum})
	return err
}

func preparedFile(ctx context.Context, dst *tar.Writer, name, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		return errors.Join(statErr, file.Close())
	}
	if !info.Mode().IsRegular() {
		return errors.Join(errors.New("prepared executable must be regular"), file.Close())
	}
	if err := dst.WriteHeader(&tar.Header{Name: name, Mode: 0o700, Size: info.Size()}); err != nil {
		return errors.Join(err, file.Close())
	}
	_, err = io.Copy(dst, preparedReader{ctx, file})
	return errors.Join(err, file.Close())
}

func missingPrepared(err error) bool {
	var typed *apierr.Error
	return errors.Is(err, blob.ErrNotFound) || errors.As(err, &typed) && typed.Code == apierr.NotFound
}

func (p *Prepared) restore(ctx context.Context, spec Spec, options Options) (entry *Entry, err error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	hit, err := p.Records.LookupRecord(ctx, blob.RecordLookup{Authority: p.Authority, Key: p.Key})
	if missingPrepared(err) {
		return nil, nil
	}
	if err != nil || !hit.Found {
		return nil, err
	}
	if hit.Record.Key != p.Key || !hit.Record.Digest.Valid() {
		return nil, blob.ErrIntegrity
	}
	reader, err := p.Bytes.Get(ctx, blob.GetRequest{Authority: p.Authority, Digest: hit.Record.Digest})
	if missingPrepared(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	if err := fssecure.EnsureDir(options.Workspace); err != nil {
		return nil, err
	}
	rootPath, err := fssecure.MkdirPrivateTemp(options.Workspace, "prepared-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if entry == nil {
			err = errors.Join(err, os.RemoveAll(rootPath))
		}
	}()
	archive, err := os.Create(filepath.Join(rootPath, "download"))
	if err != nil {
		return nil, err
	}
	_, copyErr := io.Copy(archive, preparedReader{ctx, reader})
	if err := errors.Join(copyErr, archive.Close()); err != nil {
		return nil, err
	}
	if !reader.Verified() {
		return nil, blob.ErrIntegrity
	}
	if err := extractPrepared(rootPath); err != nil {
		return nil, fmt.Errorf("%w: prepared archive: %w", blob.ErrIntegrity, err)
	}
	body, err := os.ReadFile(filepath.Join(rootPath, "prepared.json"))
	if err != nil {
		return nil, errors.Join(blob.ErrIntegrity, err)
	}
	var manifest preparedManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, errors.Join(blob.ErrIntegrity, err)
	}
	manifestKey, err := hashJSON(manifest.Inputs)
	if err != nil || manifest.Key != manifestKey || manifest.Inputs.Snapshot == "" {
		return nil, blob.ErrIntegrity
	}
	comparable := preparedSource(spec)
	if comparable.DefaultBranch == "" {
		comparable.DefaultBranch = manifest.DefaultBranch
	}
	if manifest.Inputs.Format != 1 || manifest.Inputs.RequestHash != p.RequestHash || !reflect.DeepEqual(manifest.Inputs.Source, comparable) || manifest.Inputs.Target != options.Target || spec.Engine != nil && manifest.Inputs.Engine != *spec.Engine {
		return nil, nil
	}
	spec.Engine = &manifest.Inputs.Engine
	spec.DefaultBranch = comparable.DefaultBranch

	if !filepath.IsLocal(manifest.Directory) || manifest.CodeRoot != "checkout" && manifest.CodeRoot != "code" {
		return nil, blob.ErrIntegrity
	}
	directory := filepath.Join(rootPath, manifest.CodeRoot, manifest.Directory)
	options.SDK = manifest.Inputs.Engine.Pin.SDK
	inputs, err := p.inputs(ctx, spec, options, *spec.Engine, directory)
	if err != nil {
		return nil, err
	}
	// safety: Snapshot is not recomputed on restore. Integrity comes from the verified
	// blob digest and server-enforced Cloud-only write authority; the publisher is trusted.
	inputs.Snapshot = manifest.Inputs.Snapshot
	currentKey, err := hashJSON(inputs)
	if err != nil {
		return nil, err
	}
	if currentKey != manifest.Key {
		return nil, nil
	}
	resolved, err := resolveEngine(ctx, spec, options, directory)
	if err != nil {
		return nil, err
	}
	command := []string{filepath.Join(rootPath, "pipeline")}
	if resolved.Identity.Pin.SDK == engineid.TypeScriptSDK {
		if !filepath.IsLocal(manifest.Main) {
			return nil, blob.ErrIntegrity
		}
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, err
		}
		command = []string{node, filepath.Join(directory, manifest.Main)}
	}
	return &Entry{Engine: resolved, Directory: directory, Command: command, RepoKey: spec.RepoKey, SHA: spec.SHA, DefaultBranch: manifest.DefaultBranch, ownedDirectory: rootPath, checkoutDirectory: filepath.Join(rootPath, "checkout"), pipelineCheckout: filepath.Join(rootPath, manifest.CodeRoot)}, nil
}

func extractPrepared(directory string) (err error) {
	archive, err := os.Open(filepath.Join(directory, "download"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, archive.Close()) }()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err := tarsafe.ExtractIntoRoot(tar.NewReader(gz), root, tarsafe.Policy{AllowSymlinks: true}); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, gz)
	return err
}
