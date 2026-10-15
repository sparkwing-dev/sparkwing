package nodehost

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// Publish captures declared artifacts through the shared byte and record store.
func (a ArtifactRecords) Publish(ctx context.Context, input NodeInput, workspace string, patterns []string, warn func(string)) (*blob.RecordKey, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if input.Storage.Bytes != nil || input.Storage.Records != nil {
		if input.Storage.Bytes == nil || input.Storage.Records == nil {
			return nil, errors.New("artifact storage binding is incomplete")
		}
		a.Blobs, a.Records = input.Storage.Bytes, input.Storage.Records
	}
	if a.Blobs == nil || a.Records == nil || a.Bind == nil || workspace == "" {
		return nil, errors.New("artifact publication requires bytes, records, workspace and authenticated binding")
	}
	if err := checkArtifactPatterns(patterns); err != nil {
		return nil, err
	}
	upload, err := a.Bind(ctx, input)
	if err != nil {
		return nil, err
	}
	if input.Storage.Authority.CapabilityRef != "" && input.Storage.Authority != upload.Authority {
		return nil, errors.New("artifact authority differs from the current storage binding")
	}
	if err := upload.check(input, blob.KindManifest); err != nil {
		return nil, err
	}
	if warn == nil {
		warn = func(message string) { slog.Warn(message) }
	}
	record, err := artifacts.Capture(ctx, artifacts.Data{Bytes: a.Blobs, Records: a.Records, Authority: upload.Authority, Identity: upload.Identity, Pipeline: upload.Key.Scope.Pipeline}, workspace, patterns, warn)
	if err != nil {
		return nil, err
	}
	return &record.Key, nil
}

func checkArtifactPatterns(patterns []string) error {
	for _, pattern := range patterns {
		if !filepath.IsLocal(pattern) || !fs.ValidPath(pattern) || strings.Contains(pattern, "\\") {
			return errors.New("artifact patterns must stay inside the workspace")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return errors.New("artifact glob is malformed")
		}
	}
	return nil
}
