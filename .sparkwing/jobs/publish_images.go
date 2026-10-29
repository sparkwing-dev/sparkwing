package jobs

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type PublishImagesArgs struct {
	Dist           string `flag:"dist" desc:"Directory holding the signed release binaries and their SHA256SUMS."`
	Version        string `flag:"version" desc:"Release version the images are tagged with, e.g. v0.68.0 or v0.68.0-qa.1."`
	Revision       string `flag:"revision" desc:"Source commit recorded in the org.opencontainers.image.revision label."`
	Registry       string `flag:"registry" desc:"Registry prefix the images are pushed under, e.g. ghcr.io/sparkwing-dev or an ECR host."`
	ControllerBase string `flag:"controller-base" desc:"Multi-arch runtime base for the controller, by digest: build/Dockerfile.binary's runtime stage built with BINARY=sparkwing-controller."`
	RunnerBase     string `flag:"runner-base" desc:"Multi-arch runtime base for the runner, by digest: build/Dockerfile.runner's runtime stage."`
}

// PublishImages assembles the release images from signed binaries and
// prebuilt runtime bases, so no architecture is ever emulated or compiled here.
type PublishImages struct {
	sparkwing.Base
	args PublishImagesArgs
}

func (PublishImages) ShortHelp() string {
	return "Publish linux/amd64 + linux/arm64 controller and runner images from signed binaries"
}

func (PublishImages) Help() string {
	return "Appends each signed linux binary in --dist, checked against its SHA256SUMS, as one layer onto the matching platform of a multi-arch runtime base, " +
		"the Dockerfile's runtime stage pushed by digest. Each architecture is tagged <version>-<arch>, so a registry that expires untagged images keeps it, " +
		"and both are joined under one index tagged <version>. It then reads every published platform back and refuses unless the index lists exactly " +
		"linux/amd64 and linux/arm64 and each image's binary matches SHA256SUMS. The work is registry operations only: no Docker daemon, builder or emulation. " +
		"Registry credentials come from the Docker config (docker login or crane auth login). Rebuild a base, with build-images --runtime-base on a builder " +
		"that runs both architectures, only when a Dockerfile's runtime stage or its pinned images change."
}

func (PublishImages) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Publish a QA release's images", Command: "sparkwing run publish-images -- --dist dist --version v0.68.0-qa.1 --revision $(git rev-parse HEAD) --registry 123456789012.dkr.ecr.us-west-2.amazonaws.com --controller-base REG/sparkwing-controller-base@sha256:... --runner-base REG/sparkwing-runner-base@sha256:..."},
	}
}

// safety: help and plan inspection run Plan with no arguments, so the inputs
// are checked when the step runs rather than here.
func (p *PublishImages) Plan(_ context.Context, plan *sparkwing.Plan, in PublishImagesArgs, _ sparkwing.RunContext) error {
	p.args = in
	sparkwing.Job(plan, "publish", p).Inline()
	return nil
}

func (p *PublishImages) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "publish", func(ctx context.Context) error {
		refs, err := publishReleaseImages(ctx, p.args, remote.WithAuthFromKeychain(authn.DefaultKeychain))
		for _, ref := range refs {
			sparkwing.Info(ctx, "published %s (linux/amd64, linux/arm64)", ref)
		}
		return err
	})
	return nil, nil
}

var releaseImageArches = []string{"amd64", "arm64"}

func publishReleaseImages(ctx context.Context, in PublishImagesArgs, opts ...remote.Option) ([]string, error) {
	for _, input := range []struct{ flag, value string }{
		{"--dist", in.Dist}, {"--version", in.Version}, {"--revision", in.Revision},
		{"--registry", in.Registry}, {"--controller-base", in.ControllerBase}, {"--runner-base", in.RunnerBase},
	} {
		if strings.TrimSpace(input.value) == "" {
			return nil, fmt.Errorf("publish-images: %s is required", input.flag)
		}
	}
	opts = append(opts, remote.WithContext(ctx))
	sums, err := readSHA256Sums(filepath.Join(in.Dist, "SHA256SUMS"))
	if err != nil {
		return nil, err
	}
	components := []struct{ binary, base string }{
		{"sparkwing-controller", in.ControllerBase},
		{"sparkwing-runner", in.RunnerBase},
	}
	// safety: every binary is checked before the first push, so a missing or
	// altered one publishes nothing.
	for _, c := range components {
		for _, arch := range releaseImageArches {
			if err := checkBinarySum(in.Dist, sums, c.binary, arch); err != nil {
				return nil, err
			}
		}
	}
	var published []string
	for _, c := range components {
		ref, err := publishComponent(in, c.binary, c.base, sums, opts)
		if err != nil {
			return published, fmt.Errorf("%s: %w", c.binary, err)
		}
		published = append(published, ref)
	}
	return published, nil
}

func publishComponent(in PublishImagesArgs, binary, base string, sums map[string]string, opts []remote.Option) (string, error) {
	baseRef, err := name.NewDigest(base)
	if err != nil {
		return "", fmt.Errorf("base %q must be a reference by digest: %w", base, err)
	}
	baseIndex, err := remote.Index(baseRef, opts...)
	if err != nil {
		return "", fmt.Errorf("read base %s: %w", base, err)
	}
	baseManifest, err := baseIndex.IndexManifest()
	if err != nil {
		return "", err
	}
	repo := strings.TrimSuffix(in.Registry, "/") + "/" + binary
	index := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	for _, arch := range releaseImageArches {
		desc, err := linuxPlatform(baseManifest, arch)
		if err != nil {
			return "", fmt.Errorf("base %s: %w", base, err)
		}
		baseImage, err := baseIndex.Image(desc.Digest)
		if err != nil {
			return "", err
		}
		img, err := withBinary(baseImage, filepath.Join(in.Dist, binary+"-linux-"+arch), binary, map[string]string{
			"org.opencontainers.image.version":  in.Version,
			"org.opencontainers.image.revision": in.Revision,
			"org.opencontainers.image.source":   "https://github.com/sparkwing-dev/sparkwing",
		})
		if err != nil {
			return "", err
		}
		tag, err := name.NewTag(repo + ":" + in.Version + "-" + arch)
		if err != nil {
			return "", err
		}
		if err := remote.Write(tag, img, opts...); err != nil {
			return "", fmt.Errorf("push %s: %w", tag, err)
		}
		index = mutate.AppendManifests(index, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: desc.Platform},
		})
	}
	tag, err := name.NewTag(repo + ":" + in.Version)
	if err != nil {
		return "", err
	}
	if err := remote.WriteIndex(tag, index, opts...); err != nil {
		return "", fmt.Errorf("push %s: %w", tag, err)
	}
	if err := verifyPublishedIndex(tag, binary, sums, opts); err != nil {
		return "", err
	}
	return tag.String(), nil
}

func linuxPlatform(manifest *v1.IndexManifest, arch string) (v1.Descriptor, error) {
	for _, d := range manifest.Manifests {
		if d.Platform != nil && d.Platform.OS == "linux" && d.Platform.Architecture == arch {
			return d, nil
		}
	}
	return v1.Descriptor{}, fmt.Errorf("no linux/%s image", arch)
}

// safety: the layer is built byte-for-byte the same way every time (root
// owner, mode 0755, zero time), so a rerun over the same binary and base
// produces the same digest.
func withBinary(base v1.Image, path, binary string, labels map[string]string) (v1.Image, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "usr/local/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "usr/local/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
	} {
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "usr/local/bin/" + binary, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(body); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	layerBytes := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(layerBytes)), nil
	})
	if err != nil {
		return nil, err
	}
	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		return nil, err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cfg = cfg.DeepCopy()
	if cfg.Config.Labels == nil {
		cfg.Config.Labels = map[string]string{}
	}
	for k, v := range labels {
		cfg.Config.Labels[k] = v
	}
	return mutate.ConfigFile(img, cfg)
}

func verifyPublishedIndex(tag name.Tag, binary string, sums map[string]string, opts []remote.Option) error {
	index, err := remote.Index(tag, opts...)
	if err != nil {
		return fmt.Errorf("read back %s: %w", tag, err)
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return err
	}
	var arches []string
	for _, d := range manifest.Manifests {
		if d.Platform == nil || d.Platform.OS != "linux" {
			continue
		}
		arches = append(arches, d.Platform.Architecture)
		img, err := index.Image(d.Digest)
		if err != nil {
			return err
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			return err
		}
		if cfg.Architecture != d.Platform.Architecture {
			return fmt.Errorf("%s lists linux/%s for an image whose config says %s", tag, d.Platform.Architecture, cfg.Architecture)
		}
		got, err := imageFileSHA256(img, "usr/local/bin/"+binary)
		if err != nil {
			return fmt.Errorf("%s linux/%s: %w", tag, d.Platform.Architecture, err)
		}
		want := sums[binary+"-linux-"+d.Platform.Architecture]
		if got != want {
			return fmt.Errorf("%s linux/%s carries %s sha256 %s, SHA256SUMS says %s", tag, d.Platform.Architecture, binary, got, want)
		}
	}
	slices.Sort(arches)
	if !slices.Equal(arches, releaseImageArches) {
		return fmt.Errorf("%s lists linux platforms %v, want %v", tag, arches, releaseImageArches)
	}
	return nil
}

// imageFileSHA256 hashes the file as the container sees it: the topmost layer
// that carries the path wins.
func imageFileSHA256(img v1.Image, path string) (string, error) {
	layers, err := img.Layers()
	if err != nil {
		return "", err
	}
	for i := len(layers) - 1; i >= 0; i-- {
		rc, err := layers[i].Uncompressed()
		if err != nil {
			return "", err
		}
		sum, found, err := tarFileSHA256(rc, path)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		if found {
			return sum, nil
		}
	}
	return "", fmt.Errorf("no %s in the image", path)
}

func tarFileSHA256(r io.Reader, path string) (string, bool, error) {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if strings.TrimPrefix(h.Name, "./") != path {
			continue
		}
		sum := sha256.New()
		if _, err := io.Copy(sum, tr); err != nil {
			return "", false, err
		}
		return hex.EncodeToString(sum.Sum(nil)), true, nil
	}
}

func readSHA256Sums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read SHA256SUMS: %w", err)
	}
	defer func() { _ = f.Close() }()
	sums := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 {
			sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
		}
	}
	return sums, scanner.Err()
}

func checkBinarySum(dist string, sums map[string]string, binary, arch string) error {
	file := binary + "-linux-" + arch
	want, ok := sums[file]
	if !ok {
		return fmt.Errorf("SHA256SUMS has no %s", file)
	}
	body, err := os.ReadFile(filepath.Join(dist, file))
	if err != nil {
		return err
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s does not match SHA256SUMS", file)
	}
	return nil
}

func init() {
	sparkwing.Register[PublishImagesArgs]("PublishImages", func() sparkwing.Pipeline[PublishImagesArgs] { return &PublishImages{} })
}
