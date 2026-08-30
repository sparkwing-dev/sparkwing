package livechainacceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

const maxEffectRecordBytes = 1 << 20

// IdempotentEffectAdapter owns the external system's create-if-absent and
// read-after-response-loss contract for one stable EffectRequest.ID.
type IdempotentEffectAdapter interface {
	Apply(context.Context, EffectRequest) (EffectResult, error)
	Reconcile(context.Context, EffectRequest) (EffectResult, bool, error)
}

// DurableEffectExecutor records the canonical intent before delegating to an
// idempotent external adapter, then records its original result create-only.
type DurableEffectExecutor struct {
	prefix  string
	writer  DistributedSessionObjectStore
	adapter IdempotentEffectAdapter
}

func NewDurableEffectExecutor(prefix string, writer DistributedSessionObjectStore, adapter IdempotentEffectAdapter) (*DurableEffectExecutor, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" || writer == nil || adapter == nil {
		return nil, fmt.Errorf("durable effect executor requires a prefix, distributed conditional writer, and idempotent adapter")
	}
	return &DurableEffectExecutor{prefix: prefix, writer: writer, adapter: adapter}, nil
}

var _ EffectExecutor = (*DurableEffectExecutor)(nil)

func (executor *DurableEffectExecutor) Apply(ctx context.Context, request EffectRequest) (EffectResult, error) {
	if err := executor.requireConditionalWrites(ctx); err != nil {
		return EffectResult{}, err
	}
	if err := executor.persistIntent(ctx, request); err != nil {
		return EffectResult{}, err
	}
	if result, found, err := executor.loadResult(ctx, request.ID); err != nil {
		return EffectResult{}, err
	} else if found {
		return result, nil
	}
	if result, found, err := executor.adapter.Reconcile(ctx, request); err != nil {
		return EffectResult{}, fmt.Errorf("reconcile external effect %s: %w", request.ID, err)
	} else if found {
		return executor.persistResult(ctx, request.ID, result)
	}
	result, err := executor.adapter.Apply(ctx, request)
	if err != nil {
		return EffectResult{}, fmt.Errorf("apply external effect %s: %w", request.ID, err)
	}
	return executor.persistResult(ctx, request.ID, result)
}

func (executor *DurableEffectExecutor) Reconcile(ctx context.Context, request EffectRequest) (EffectResult, bool, error) {
	if err := executor.requireConditionalWrites(ctx); err != nil {
		return EffectResult{}, false, err
	}
	intent, found, err := executor.loadIntent(ctx, request.ID)
	if err != nil {
		return EffectResult{}, false, err
	}
	if !found {
		return EffectResult{}, false, nil
	}
	encodedRequest, err := encodeEffectRecord(request)
	if err != nil {
		return EffectResult{}, false, err
	}
	if !bytes.Equal(intent, encodedRequest) {
		return EffectResult{}, false, fmt.Errorf("%w: durable effect intent %s has a different request", ErrSessionConflict, request.ID)
	}
	if result, found, err := executor.loadResult(ctx, request.ID); err != nil || found {
		return result, found, err
	}
	result, found, err := executor.adapter.Reconcile(ctx, request)
	if err != nil || !found {
		return EffectResult{}, false, err
	}
	persisted, err := executor.persistResult(ctx, request.ID, result)
	return persisted, err == nil, err
}

func (executor *DurableEffectExecutor) persistIntent(ctx context.Context, request EffectRequest) error {
	if request.ID == "" || request.Kind == "" {
		return fmt.Errorf("durable effect request identity is incomplete")
	}
	encoded, err := encodeEffectRecord(request)
	if err != nil {
		return err
	}
	key := executor.intentKey(request.ID)
	if _, err := executor.writer.PutIfAbsent(ctx, key, bytes.NewReader(encoded)); err == nil {
		return nil
	} else if !errors.Is(err, storage.ErrPreconditionFailed) {
		return fmt.Errorf("persist durable effect intent: %w", err)
	}
	existing, err := executor.readRecord(ctx, key)
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, encoded) {
		return fmt.Errorf("%w: durable effect intent %s has a different request", ErrSessionConflict, request.ID)
	}
	return nil
}

func (executor *DurableEffectExecutor) loadIntent(ctx context.Context, id string) ([]byte, bool, error) {
	encoded, err := executor.readRecord(ctx, executor.intentKey(id))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	return encoded, err == nil, err
}

func (executor *DurableEffectExecutor) loadResult(ctx context.Context, id string) (EffectResult, bool, error) {
	encoded, err := executor.readRecord(ctx, executor.resultKey(id))
	if errors.Is(err, storage.ErrNotFound) {
		return EffectResult{}, false, nil
	}
	if err != nil {
		return EffectResult{}, false, err
	}
	var result EffectResult
	if err := decodeEffectRecord(encoded, &result); err != nil {
		return EffectResult{}, false, fmt.Errorf("decode durable effect result: %w", err)
	}
	return result, true, nil
}

func (executor *DurableEffectExecutor) persistResult(ctx context.Context, id string, result EffectResult) (EffectResult, error) {
	encoded, err := encodeEffectRecord(result)
	if err != nil {
		return EffectResult{}, err
	}
	key := executor.resultKey(id)
	if _, err := executor.writer.PutIfAbsent(ctx, key, bytes.NewReader(encoded)); err == nil {
		return result, nil
	} else if !errors.Is(err, storage.ErrPreconditionFailed) {
		return EffectResult{}, fmt.Errorf("persist durable effect result: %w", err)
	}
	existing, err := executor.readRecord(ctx, key)
	if err != nil {
		return EffectResult{}, err
	}
	if !bytes.Equal(existing, encoded) {
		return EffectResult{}, fmt.Errorf("%w: durable effect result %s differs from the original", ErrSessionConflict, id)
	}
	return result, nil
}

func (executor *DurableEffectExecutor) requireConditionalWrites(ctx context.Context) error {
	ok, err := executor.writer.ConditionalWritesSupported(ctx)
	if err != nil {
		return fmt.Errorf("probe durable effect conditional writes: %w", err)
	}
	if !ok {
		return fmt.Errorf("durable effects refuse an endpoint without enforced conditional writes")
	}
	return nil
}

func (executor *DurableEffectExecutor) readRecord(ctx context.Context, key string) ([]byte, error) {
	reader, _, err := executor.writer.GetWithETag(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	encoded, err := io.ReadAll(io.LimitReader(reader, maxEffectRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxEffectRecordBytes {
		return nil, fmt.Errorf("durable effect record exceeds %d bytes", maxEffectRecordBytes)
	}
	return encoded, nil
}

func (executor *DurableEffectExecutor) intentKey(id string) string {
	return executor.effectPrefix(id) + "/intent.json"
}

func (executor *DurableEffectExecutor) resultKey(id string) string {
	return executor.effectPrefix(id) + "/result.json"
}

func (executor *DurableEffectExecutor) effectPrefix(id string) string {
	sum := sha256.Sum256([]byte(id))
	return executor.prefix + "/effects/" + hex.EncodeToString(sum[:])
}

func encodeEffectRecord(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxEffectRecordBytes {
		return nil, fmt.Errorf("durable effect record exceeds %d bytes", maxEffectRecordBytes)
	}
	return encoded, nil
}

func decodeEffectRecord(encoded []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("durable effect record contains trailing data")
	}
	return nil
}
