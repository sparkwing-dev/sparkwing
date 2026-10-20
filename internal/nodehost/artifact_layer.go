package nodehost

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Prepare implements the artifact portion of ExecutorDependencies.PrepareLayer.
// It stages consumes in declaration order and publishes once after successful
// execution. The finalizer uses the last committed attempt's storage authority.
// Other layer names are left to the caller's layer composition.
func (a ArtifactRecords) Prepare(ctx context.Context, layer LayerSpec, input NodeInput, job nodeproto.JobSpec) (ExecutorFinalizer, error) {
	if layer.Name != "artifacts" || len(job.Artifacts) == 0 && len(job.Consumes) == 0 {
		return nil, nil
	}
	if input.Entry == nil || input.Entry.Directory == "" {
		return nil, errors.New("artifact layer requires an acquired workspace")
	}
	if err := checkArtifactPatterns(job.Artifacts); err != nil {
		return nil, err
	}
	for _, edge := range job.Consumes {
		if edge.Into != "" && !filepath.IsLocal(edge.Into) {
			return nil, errors.New("artifact destination must stay inside the workspace")
		}
	}
	if len(job.Consumes) > 0 && a.Producer == nil {
		return nil, errors.New("artifact staging requires persisted producer references")
	}
	if input.Storage.Bytes != nil || input.Storage.Records != nil {
		if input.Storage.Bytes == nil || input.Storage.Records == nil {
			return nil, errors.New("artifact storage binding is incomplete")
		}
		a.Blobs, a.Records = input.Storage.Bytes, input.Storage.Records
	}
	for _, edge := range job.Consumes {
		key, err := a.Producer(ctx, input, edge.Producer)
		if err != nil {
			return nil, err
		}
		if key.Kind != blob.KindManifest || key.Scope.Job != edge.Producer {
			return nil, errors.New("artifact reference differs from its declared producer")
		}
		if _, err := a.Stage(ctx, blob.RecordLookup{Key: key, Authority: input.Storage.Authority}, sdkWorkingDirectory(input.Entry), edge.Into); err != nil {
			return nil, err
		}
	}
	if len(job.Artifacts) == 0 {
		return nil, nil
	}
	return func(ctx context.Context, current NodeInput, result *store.NodeResult) error {
		if result.Outcome != store.NodeSuccess {
			return nil
		}
		key, err := a.Publish(ctx, current, sdkWorkingDirectory(input.Entry), job.Artifacts, a.Warn)
		if err != nil {
			return err
		}
		result.ManifestRecord = key
		return nil
	}, nil
}
