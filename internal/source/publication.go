package source

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// Publication binds source bytes to the prepared environment and withheld secret classification.
type Publication struct {
	Path    string
	Base    procenv.Base
	Secrets map[string]string
}

// OpenPublication copies and scans the final bundle before returning its private upload stream.
func OpenPublication(ctx context.Context, publication Publication) (_ *os.File, _ func() error, resultErr error) {
	if publication.Secrets == nil || publication.Base.Masker == nil {
		return nil, nil, errors.New("source publication requires prepared secret classification and masker")
	}
	environment, err := procenv.Build(procenv.Git, publication.Base, procenv.Runtime{})
	if err != nil {
		return nil, nil, err
	}
	root, err := os.MkdirTemp("", "sparkwing-publication-")
	if err != nil {
		return nil, nil, err
	}
	var stream *os.File
	cleanup := func() error {
		var closeErr error
		if stream != nil {
			closeErr = stream.Close()
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
	stream, err = os.OpenFile(filepath.Join(root, "source.bundle"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, errors.Join(err, input.Close())
	}
	_, copyErr := io.Copy(stream, input)
	if err := errors.Join(copyErr, input.Close(), ctx.Err()); err != nil {
		return nil, nil, err
	}
	if err := scanPublication(ctx, root, stream.Name(), environment, publication.Secrets); err != nil {
		return nil, nil, err
	}
	if _, err := stream.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	return stream, cleanup, nil
}

func scanPublication(ctx context.Context, root, bundle string, environment []string, values map[string]string) error {
	masker := secrets.NewMasker()
	for _, value := range values {
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
	client := gitClient{env: gitEnvironment(environment)}
	checkout := filepath.Join(root, "objects")
	if err := initCheckout(ctx, &client, checkout, bundle, ""); err != nil {
		return err
	}
	if _, err := client.run(ctx, checkout, "", "bundle", "unbundle", bundle); err != nil {
		return err
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(scanCtx, "git", "-C", checkout, "cat-file", "--batch-all-objects", "--batch")
	cmd.Env = client.env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("open source snapshot scan failed")
	}
	if err := cmd.Start(); err != nil {
		return errors.New("start source snapshot scan failed")
	}
	scanErr := scanPublicationObjects(bufio.NewReader(stdout), patterns, longest)
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

func scanPublicationObjects(reader *bufio.Reader, patterns []string, longest int) error {
	buffer := make([]byte, 64<<10)
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
		tail := ""
		for size > 0 {
			n, err := io.ReadFull(reader, buffer[:min(int64(len(buffer)), size)])
			if err != nil {
				return errors.New("read source snapshot contents failed")
			}
			if fields[1] == "blob" {
				text := tail + string(buffer[:n])
				if slices.ContainsFunc(patterns, func(pattern string) bool { return strings.Contains(text, pattern) }) {
					return errors.New("source bundle contains a declared local secret")
				}
				tail = text[max(0, len(text)-longest+1):]
			}
			size -= int64(n)
		}
		if end, err := reader.ReadByte(); err != nil || end != '\n' {
			return errors.New("read source snapshot object end failed")
		}
	}
}
