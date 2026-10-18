package source

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// Publication binds source bytes and metadata to the prepared environment and withheld secret classification.
type Publication struct {
	Path    string
	Git     runspec.Git
	Source  runspec.Source
	Base    procenv.Base
	Secrets map[string]string
}

// OpenPublication returns a scanned bundle copy and its cleanup under the platform's private-file policy.
func OpenPublication(ctx context.Context, publication Publication) (_ *os.File, _ func() error, resultErr error) {
	if publication.Secrets == nil || publication.Base.Masker == nil {
		return nil, nil, errors.New("source publication requires prepared secret classification and masker")
	}
	environment, err := procenv.Build(procenv.Git, publication.Base, procenv.Runtime{})
	if err != nil {
		return nil, nil, err
	}
	root, err := fssecure.MkdirPrivateTemp("", "sparkwing-publication-")
	if err != nil {
		return nil, nil, err
	}
	var stream *os.File
	cleanup := func() error {
		var closeErr error
		// safety: an HTTP transfer closes the stream it sends as a request body.
		if stream != nil {
			if closeErr = stream.Close(); errors.Is(closeErr, os.ErrClosed) {
				closeErr = nil
			}
		}
		return errors.Join(closeErr, os.RemoveAll(root))
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, cleanup())
		}
	}()
	input, err := os.Open(publication.Path)
	if err != nil {
		return nil, nil, err
	}
	stream, err = fssecure.OpenFile(filepath.Join(root, "source.bundle"), os.O_CREATE|os.O_EXCL|os.O_RDWR)
	if err != nil {
		return nil, nil, errors.Join(err, input.Close())
	}
	_, copyErr := io.Copy(stream, input)
	if err := errors.Join(copyErr, input.Close(), ctx.Err()); err != nil {
		return nil, nil, err
	}
	if err := scanPublication(ctx, root, stream, environment, publication); err != nil {
		return nil, nil, err
	}
	if _, err := stream.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	return stream, cleanup, nil
}

func scanPublication(ctx context.Context, root string, bundle *os.File, environment []string, publication Publication) error {
	masker := secrets.NewMasker()
	for _, value := range publication.Secrets {
		lines := slices.DeleteFunc(strings.Split(value, "\n"), func(line string) bool {
			line = strings.TrimSpace(line)
			return strings.HasPrefix(line, "-----BEGIN ") || strings.HasPrefix(line, "-----END ")
		})
		value = strings.Join(lines, "\n")
		if len(value) >= 8 {
			masker.Register(value)
		}
	}
	patterns := slices.DeleteFunc(masker.Values(), func(pattern string) bool { return len(pattern) < 8 })
	longest := 0
	for _, pattern := range patterns {
		longest = max(longest, len(pattern))
	}
	if longest == 0 {
		return nil
	}
	policy := publicationPolicy{patterns: patterns, longest: longest, buffer: make([]byte, 64<<10)}
	metadata, err := json.Marshal(struct {
		Git    runspec.Git    `json:"git"`
		Source runspec.Source `json:"source"`
	}{publication.Git, publication.Source})
	if err != nil {
		return errors.New("read source publication metadata failed")
	}
	if err := policy.scan(strings.NewReader(string(metadata))); err != nil {
		return err
	}
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(bundle)
	var header strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return errors.New("read source bundle header failed")
		}
		header.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if err := policy.scan(strings.NewReader(header.String())); err != nil {
		return err
	}
	client := gitClient{env: gitEnvironment(environment)}
	checkout := filepath.Join(root, "objects")
	if err := initCheckout(ctx, &client, checkout, bundle.Name(), ""); err != nil {
		return err
	}
	objects, err := bundleObjects(ctx, &client, checkout, bundle.Name(), publication.Source.Location.LocalCheckout)
	if err != nil {
		return err
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(scanCtx, "git", "-C", checkout, "cat-file", "--batch")
	cmd.Env = client.env
	cmd.Stdin = strings.NewReader(objects)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("open source snapshot scan failed")
	}
	if err := cmd.Start(); err != nil {
		return errors.New("start source snapshot scan failed")
	}
	scanErr := policy.scanObjects(bufio.NewReader(stdout))
	if scanErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		return errors.New("source snapshot scan failed")
	}
	return ctx.Err()
}

type publicationPolicy struct {
	patterns []string
	longest  int
	buffer   []byte
}

func (p publicationPolicy) scanObjects(reader *bufio.Reader) error {
	for {
		header, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) && header == "" {
			return nil
		}
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 {
			return errors.New("read source snapshot object failed")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return errors.New("read source snapshot object size failed")
		}
		contents := &io.LimitedReader{R: reader, N: size}
		if err := p.scan(contents); err != nil {
			return err
		}
		if contents.N != 0 {
			return errors.New("read source snapshot contents failed")
		}
		if end, err := reader.ReadByte(); err != nil || end != '\n' {
			return errors.New("read source snapshot object end failed")
		}
	}
}

func (p publicationPolicy) scan(reader io.Reader) error {
	tail := ""
	for {
		n, err := io.ReadFull(reader, p.buffer)
		text := tail + string(p.buffer[:n])
		if slices.ContainsFunc(p.patterns, func(pattern string) bool { return strings.Contains(text, pattern) }) {
			return errors.New("source bundle contains a declared local secret")
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return errors.New("read source snapshot contents failed")
		}
		tail = text[max(0, len(text)-p.longest+1):]
	}
}

// safety: a bundle that stops at pushed commits resolves its deltas against the
// local checkout, but only objects the bundle itself carries are scanned.
func bundleObjects(ctx context.Context, client *gitClient, checkout, bundle, local string) (string, error) {
	prerequisites, err := bundlePrerequisites(bundle)
	if err != nil {
		return "", err
	}
	if len(prerequisites) > 0 {
		if local == "" {
			return "", errors.New("source bundle with prerequisites requires its local checkout")
		}
		common, err := client.run(ctx, local, "", "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return "", err
		}
		alternates := filepath.Join(checkout, ".git", "objects", "info", "alternates")
		if err := os.WriteFile(alternates, []byte(filepath.Join(strings.TrimSpace(common), "objects")+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	if _, err := client.run(ctx, checkout, "", "bundle", "unbundle", bundle); err != nil {
		return "", err
	}
	heads, err := client.run(ctx, checkout, "", "bundle", "list-heads", bundle)
	if err != nil {
		return "", err
	}
	args := []string{"rev-list", "--objects"}
	for _, line := range strings.Split(strings.TrimSpace(heads), "\n") {
		if sha, _, _ := strings.Cut(line, " "); gitSHA.MatchString(sha) {
			args = append(args, sha)
		}
	}
	if len(prerequisites) > 0 {
		args = append(append(args, "--not"), prerequisites...)
	}
	listed, err := client.run(ctx, checkout, "", args...)
	if err != nil {
		return "", err
	}
	var ids strings.Builder
	for _, line := range strings.Split(listed, "\n") {
		if id, _, _ := strings.Cut(line, " "); id != "" {
			ids.WriteString(id + "\n")
		}
	}
	return ids.String(), nil
}
