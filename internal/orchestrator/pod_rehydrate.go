package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// rehydratePipelineConfig decodes the resolved Config blob the
// orchestrator persisted into the pipeline's typed Config struct.
// The struct's shape comes from reg.instance().Config() (the
// pipeline's ConfigProvider implementation); the snapshot ships
// only the json-encoded values, never the type information. Returns
// nil, nil when the pipeline does not implement ConfigProvider, the
// snapshot carries no pipeline_config field, or the snapshot can't
// be parsed (the caller logs and proceeds with a nil accessor in
// that case).
func rehydratePipelineConfig(snapshot []byte, reg *sparkwing.Registration) (any, error) {
	if len(snapshot) == 0 || reg == nil {
		return nil, nil
	}
	var meta struct {
		PipelineConfig json.RawMessage `json:"pipeline_config"`
	}
	if err := json.Unmarshal(snapshot, &meta); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	return sparkwingruntime.DecodePipelineConfig(reg, meta.PipelineConfig)
}

// rehydrateTarget reads the run's active target out of the persisted
// plan snapshot so the pod sees the same sparkwing.Target(ctx) value
// the orchestrator-side dispatch saw. Empty (with nil error) when
// the run had no --for selection.
func rehydrateTarget(snapshot []byte) (string, error) {
	if len(snapshot) == 0 {
		return "", nil
	}
	var meta struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(snapshot, &meta); err != nil {
		return "", fmt.Errorf("decode snapshot: %w", err)
	}
	return meta.Target, nil
}

// rehydratePipelineSecrets re-resolves the pipeline's declared
// secrets against the SecretResolver already installed on ctx (the
// pod's controller-backed source). The orchestrator persisted only
// the SecretsField list (names + required/optional flags); values
// never traveled with the snapshot.
//
// Returns nil, nil when the pipeline does not implement
// SecretsProvider and the snapshot carries no SecretsField entries.
// A required-secret miss propagates as an error -- by the time the
// pod runs this, the orchestrator-side fail-fast already passed, so
// a pod-side miss signals an environment drift between controller
// and runner that's worth failing loudly.
func rehydratePipelineSecrets(ctx context.Context, snapshot []byte, reg *sparkwing.Registration) (any, error) {
	if reg == nil {
		return nil, nil
	}
	var meta struct {
		Secrets pipelines.SecretsField `json:"secrets"`
	}
	if len(snapshot) > 0 {
		if err := json.Unmarshal(snapshot, &meta); err != nil {
			return nil, fmt.Errorf("decode snapshot: %w", err)
		}
	}
	stub := &pipelines.Pipeline{Secrets: meta.Secrets}
	return sparkwingruntime.ResolvePipelineSecrets(ctx, reg, stub)
}
