package s3state

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// casMaxRetries bounds a contended read-modify-CAS loop before giving up.
const casMaxRetries = 16

// maxAncestorDepth bounds the parent-run walk EnqueueTrigger uses for
// cycle detection, guarding against a corrupted parent chain.
const maxAncestorDepth = 64

// notSupported wraps ErrNotSupported for a control-plane operation the
// backend cannot perform, either because the endpoint lacks conditional
// writes or the feature is unavailable in this mode.
func notSupported(op string) error {
	return fmt.Errorf("%w: %s requires conditional-write support (object-store CAS), Mode 3 (Postgres), or Mode 4 (hosted controller)", ErrNotSupported, op)
}

// cas returns the backend's ConditionalWriter when the artifact store
// implements it and the live endpoint enforces write preconditions. A
// false ok means the caller must surface ErrNotSupported. A non-nil err
// is a transport fault probing the endpoint and propagates unchanged.
func (b *Backend) cas(ctx context.Context) (cw storage.ConditionalWriter, ok bool, err error) {
	cw, implemented := storage.Conditional(b.art)
	if !implemented {
		return nil, false, nil
	}
	supported, err := cw.ConditionalWritesSupported(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("s3state: probe conditional-write support: %w", err)
	}
	return cw, supported, nil
}

// seg hex-encodes an identifier into a single path-safe key segment.
// Node IDs and pause reasons are caller-supplied and may carry slashes
// or other characters an object key cannot; hex keeps the segment
// reversible-free and collision-free without constraining the input.
func seg(s string) string { return hex.EncodeToString([]byte(s)) }

func dispatchPrefix(runID, nodeID string) string {
	return "runs/" + runID + "/dispatch/" + seg(nodeID) + "/"
}

func dispatchKey(runID, nodeID string, seq int) string {
	return fmt.Sprintf("%s%020d.json", dispatchPrefix(runID, nodeID), seq)
}

// seqFromDispatchKey extracts the seq from a dispatch record key,
// returning false for any key that does not match the layout.
func seqFromDispatchKey(key string) (int, bool) {
	const suffix = ".json"
	if !strings.HasSuffix(key, suffix) {
		return 0, false
	}
	slash := strings.LastIndex(key, "/")
	if slash < 0 {
		return 0, false
	}
	mid := key[slash+1 : len(key)-len(suffix)]
	seq, err := strconv.Atoi(mid)
	if err != nil || seq < 0 {
		return 0, false
	}
	return seq, true
}

func pauseNodePrefix(runID, nodeID string) string {
	return "runs/" + runID + "/pause/" + seg(nodeID) + "/"
}

func pauseRunPrefix(runID string) string { return "runs/" + runID + "/pause/" }

func pauseKey(runID, nodeID, reason string) string {
	return pauseNodePrefix(runID, nodeID) + seg(reason) + ".json"
}

func approvalKey(runID, nodeID string) string {
	return "runs/" + runID + "/approval/" + seg(nodeID) + ".json"
}

// isApprovalKey reports whether key is a runs/<id>/approval/<seg>.json
// record, used to filter a full-bucket scan for ListPendingApprovals.
func isApprovalKey(key string) bool {
	parts := strings.Split(key, "/")
	return len(parts) == 4 && parts[0] == "runs" && parts[2] == "approval" && strings.HasSuffix(parts[3], ".json")
}

func triggerKey(id string) string { return "triggers/by-id/" + seg(id) + ".json" }

func childTriggerKey(parentRunID, parentNodeID, pipeline string) string {
	return "triggers/child/" + seg(parentRunID) + "/" + seg(parentNodeID) + "/" + seg(pipeline) + ".json"
}

// childTriggerIndex is the canonical idempotency record a spawning node
// writes once per (parentRunID, parentNodeID, pipeline); a repeated
// enqueue reads it back instead of minting a duplicate child run.
type childTriggerIndex struct {
	TriggerID string `json:"trigger_id"`
}

// getRecord reads and JSON-decodes the object at key, returning its
// current ETag for a follow-on PutIfMatch. storage.ErrNotFound when the
// object is absent.
func getRecord(ctx context.Context, cw storage.ConditionalWriter, key string, v any) (storage.ETag, error) {
	rc, etag, err := cw.GetWithETag(ctx, key)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return "", fmt.Errorf("s3state: decode %s: %w", key, err)
	}
	return etag, nil
}

// listKeys returns every key under prefix, translating the
// list-unsupported sentinel into a feature-scoped ErrNotSupported.
func (b *Backend) listKeys(ctx context.Context, prefix, feature string) ([]string, error) {
	keys, err := b.art.List(ctx, prefix)
	if err != nil {
		if errors.Is(err, storage.ErrListNotSupported) {
			return nil, fmt.Errorf("%w: %s needs ArtifactStore.List", ErrNotSupported, feature)
		}
		return nil, err
	}
	return keys, nil
}

func (b *Backend) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return notSupported("dispatch tracking")
	}
	if d.RunID == "" || d.NodeID == "" {
		return fmt.Errorf("WriteNodeDispatch: run_id and node_id required")
	}
	origSize := int64(len(d.InputEnvelope))
	if origSize > store.MaxNodeDispatchEnvelope {
		d.InputEnvelope = fmt.Appendf(nil,
			`{"version":1,"truncated":true,"reason":"size","original_size":%d}`, origSize)
	}
	d.InputSizeBytes = origSize
	if d.DispatchedAt.IsZero() {
		d.DispatchedAt = time.Now()
	}

	if d.Seq >= 0 {
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		_, err = cw.PutIfAbsent(ctx, dispatchKey(d.RunID, d.NodeID, d.Seq), bytes.NewReader(body))
		if errors.Is(err, storage.ErrPreconditionFailed) {
			return fmt.Errorf("WriteNodeDispatch: seq %d already written for %s/%s", d.Seq, d.RunID, d.NodeID)
		}
		return err
	}

	for attempt := 0; attempt < casMaxRetries; attempt++ {
		next, err := b.nextDispatchSeq(ctx, d.RunID, d.NodeID)
		if err != nil {
			return err
		}
		d.Seq = next
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		_, err = cw.PutIfAbsent(ctx, dispatchKey(d.RunID, d.NodeID, next), bytes.NewReader(body))
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			return err
		}
	}
	return fmt.Errorf("WriteNodeDispatch: seq contention for %s/%s after %d attempts", d.RunID, d.NodeID, casMaxRetries)
}

// nextDispatchSeq returns one past the highest existing seq for the
// node, mirroring the MAX(seq)+1 assignment the SQL store performs.
func (b *Backend) nextDispatchSeq(ctx context.Context, runID, nodeID string) (int, error) {
	keys, err := b.listKeys(ctx, dispatchPrefix(runID, nodeID), "dispatch tracking")
	if err != nil {
		return 0, err
	}
	maxSeq := -1
	for _, k := range keys {
		if seq, ok := seqFromDispatchKey(k); ok && seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq + 1, nil
}

func (b *Backend) GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error) {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("dispatch tracking")
	}
	if seq < 0 {
		keys, err := b.listKeys(ctx, dispatchPrefix(runID, nodeID), "dispatch tracking")
		if err != nil {
			return nil, err
		}
		latest, found := "", -1
		for _, k := range keys {
			if s, ok := seqFromDispatchKey(k); ok && s > found {
				found, latest = s, k
			}
		}
		if latest == "" {
			return nil, store.ErrNotFound
		}
		var d store.NodeDispatch
		if _, err := getRecord(ctx, cw, latest, &d); err != nil {
			return nil, err
		}
		return &d, nil
	}
	var d store.NodeDispatch
	if _, err := getRecord(ctx, cw, dispatchKey(runID, nodeID, seq), &d); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

func (b *Backend) ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error) {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("dispatch tracking")
	}
	keys, err := b.listKeys(ctx, dispatchPrefix(runID, nodeID), "dispatch tracking")
	if err != nil {
		return nil, err
	}
	out := make([]*store.NodeDispatch, 0, len(keys))
	for _, k := range keys {
		if _, ok := seqFromDispatchKey(k); !ok {
			continue
		}
		var d store.NodeDispatch
		if _, err := getRecord(ctx, cw, k, &d); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, &d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (b *Backend) CreateDebugPause(ctx context.Context, p store.DebugPause) error {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return notSupported("interactive debug pauses")
	}
	p.ReleasedAt = nil
	p.ReleasedBy = ""
	p.ReleaseKind = ""
	key := pauseKey(p.RunID, p.NodeID, p.Reason)
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < casMaxRetries; attempt++ {
		var cur store.DebugPause
		etag, err := getRecord(ctx, cw, key, &cur)
		if errors.Is(err, storage.ErrNotFound) {
			_, err = cw.PutIfAbsent(ctx, key, bytes.NewReader(body))
		} else if err != nil {
			return err
		} else {
			_, err = cw.PutIfMatch(ctx, key, bytes.NewReader(body), etag)
		}
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			return err
		}
	}
	return fmt.Errorf("CreateDebugPause: contention for %s/%s after %d attempts", p.RunID, p.NodeID, casMaxRetries)
}

func (b *Backend) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error) {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("interactive debug pauses")
	}
	keys, err := b.listKeys(ctx, pauseNodePrefix(runID, nodeID), "interactive debug pauses")
	if err != nil {
		return nil, err
	}
	var best *store.DebugPause
	for _, k := range keys {
		var p store.DebugPause
		if _, err := getRecord(ctx, cw, k, &p); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if p.ReleasedAt != nil {
			continue
		}
		if best == nil || p.PausedAt.After(best.PausedAt) {
			clone := p
			best = &clone
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return best, nil
}

func (b *Backend) ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error) {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("interactive debug pauses")
	}
	keys, err := b.listKeys(ctx, pauseRunPrefix(runID), "interactive debug pauses")
	if err != nil {
		return nil, err
	}
	out := make([]*store.DebugPause, 0, len(keys))
	for _, k := range keys {
		var p store.DebugPause
		if _, err := getRecord(ctx, cw, k, &p); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return nil, err
		}
		clone := p
		out = append(out, &clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PausedAt.After(out[j].PausedAt) })
	return out, nil
}

func (b *Backend) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return notSupported("interactive debug pauses")
	}
	keys, err := b.listKeys(ctx, pauseNodePrefix(runID, nodeID), "interactive debug pauses")
	if err != nil {
		return err
	}
	released := 0
	for _, k := range keys {
		done, err := b.releasePauseRecord(ctx, cw, k, releasedBy, kind)
		if err != nil {
			return err
		}
		if done {
			released++
		}
	}
	if released == 0 {
		return store.ErrNotFound
	}
	return nil
}

// releasePauseRecord stamps released_at on one open pause via a CAS
// loop. Reports false (without error) when the pause is already
// released or vanished, so a concurrent release is not double-counted.
func (b *Backend) releasePauseRecord(ctx context.Context, cw storage.ConditionalWriter, key, releasedBy, kind string) (bool, error) {
	for attempt := 0; attempt < casMaxRetries; attempt++ {
		var p store.DebugPause
		etag, err := getRecord(ctx, cw, key, &p)
		if errors.Is(err, storage.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if p.ReleasedAt != nil {
			return false, nil
		}
		now := time.Now().UTC()
		p.ReleasedAt = &now
		p.ReleasedBy = releasedBy
		p.ReleaseKind = kind
		body, err := json.Marshal(p)
		if err != nil {
			return false, err
		}
		_, err = cw.PutIfMatch(ctx, key, bytes.NewReader(body), etag)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			return false, err
		}
	}
	return false, fmt.Errorf("ReleaseDebugPause: contention on %s after %d attempts", key, casMaxRetries)
}

func (b *Backend) CreateApproval(ctx context.Context, a store.Approval) error {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return notSupported("approval gates")
	}
	if a.OnTimeout == "" {
		a.OnTimeout = store.ApprovalOnTimeoutFail
	}
	a.Approver = ""
	a.ResolvedAt = nil
	a.Resolution = ""
	a.Comment = ""
	key := approvalKey(a.RunID, a.NodeID)
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < casMaxRetries; attempt++ {
		var cur store.Approval
		etag, err := getRecord(ctx, cw, key, &cur)
		if errors.Is(err, storage.ErrNotFound) {
			_, err = cw.PutIfAbsent(ctx, key, bytes.NewReader(body))
		} else if err != nil {
			return err
		} else {
			_, err = cw.PutIfMatch(ctx, key, bytes.NewReader(body), etag)
		}
		if err == nil {
			return b.SetNodeStatus(ctx, a.RunID, a.NodeID, store.NodeStatusApprovalPending)
		}
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			return err
		}
	}
	return fmt.Errorf("CreateApproval: contention for %s/%s after %d attempts", a.RunID, a.NodeID, casMaxRetries)
}

func (b *Backend) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("approval gates")
	}
	var a store.Approval
	if _, err := getRecord(ctx, cw, approvalKey(runID, nodeID), &a); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (b *Backend) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error) {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("approval gates")
	}
	key := approvalKey(runID, nodeID)
	for attempt := 0; attempt < casMaxRetries; attempt++ {
		var a store.Approval
		etag, err := getRecord(ctx, cw, key, &a)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if a.ResolvedAt != nil {
			return nil, store.ErrLockHeld
		}
		now := time.Now().UTC()
		a.Resolution = resolution
		a.Approver = approver
		a.Comment = comment
		a.ResolvedAt = &now
		body, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		if _, err := cw.PutIfMatch(ctx, key, bytes.NewReader(body), etag); err != nil {
			if errors.Is(err, storage.ErrPreconditionFailed) {
				continue
			}
			return nil, err
		}
		resolved := a
		return &resolved, nil
	}
	return nil, fmt.Errorf("ResolveApproval: contention for %s/%s after %d attempts", runID, nodeID, casMaxRetries)
}

func (b *Backend) ListPendingApprovals(ctx context.Context) ([]*store.Approval, error) {
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("approval gates")
	}
	keys, err := b.listKeys(ctx, "runs/", "approval gates")
	if err != nil {
		return nil, err
	}
	var out []*store.Approval
	for _, k := range keys {
		if !isApprovalKey(k) {
			continue
		}
		var a store.Approval
		if _, err := getRecord(ctx, cw, k, &a); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if a.ResolvedAt != nil {
			continue
		}
		clone := a
		out = append(out, &clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out, nil
}

func (b *Backend) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	if parentRunID == "" || parentNodeID == "" || pipeline == "" {
		return "", nil
	}
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", notSupported("pipeline triggers")
	}
	var idx childTriggerIndex
	if _, err := getRecord(ctx, cw, childTriggerKey(parentRunID, parentNodeID, pipeline), &idx); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return idx.TriggerID, nil
}

func (b *Backend) GetTrigger(ctx context.Context, triggerID string) (*store.Trigger, error) {
	if triggerID == "" {
		return nil, store.ErrNotFound
	}
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notSupported("pipeline triggers")
	}
	var trigger store.Trigger
	if _, err := getRecord(ctx, cw, triggerKey(triggerID), &trigger); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &trigger, nil
}

// EnqueueTrigger records a pipeline trigger as a discrete object-store
// record and returns its run ID. When parentRunID and parentNodeID are
// set, the child trigger is idempotent on (parentRunID, parentNodeID,
// pipeline): a repeated enqueue from the same spawning node returns the
// original run ID instead of minting a duplicate, the Mode 2 analog of
// the SQL unique constraint. Cycles are rejected by walking the parent
// run chain; the error wraps the word "cycle" for the await path.
func (b *Backend) EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (string, error) {
	return b.EnqueueTriggerWithEnv(ctx, pipeline, args, parentRunID, parentNodeID, retryOf, source, user, repo, branch, nil)
}

func (b *Backend) EnqueueTriggerWithEnv(
	ctx context.Context,
	pipeline string,
	args map[string]string,
	parentRunID string,
	parentNodeID string,
	retryOf string,
	source string,
	user string,
	repo string,
	branch string,
	triggerEnv map[string]string,
) (string, error) {
	if pipeline == "" {
		return "", errors.New("EnqueueTrigger: pipeline required")
	}
	cw, ok, err := b.cas(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", notSupported("pipeline triggers")
	}

	if parentRunID != "" {
		chain, err := b.ancestorPipelines(ctx, parentRunID)
		if err != nil {
			return "", err
		}
		for _, p := range chain {
			if p == pipeline {
				return "", fmt.Errorf("cycle: %s would re-enter itself", pipeline)
			}
		}
	}

	runID := triggerRunID()
	tg := store.Trigger{
		ID:            runID,
		Pipeline:      pipeline,
		Args:          args,
		TriggerSource: firstNonEmpty(source, "await-pipeline"),
		TriggerUser:   user,
		Status:        "pending",
		CreatedAt:     time.Now().UTC(),
		ParentRunID:   parentRunID,
		ParentNodeID:  parentNodeID,
		RetryOf:       retryOf,
		TriggerEnv:    triggerEnv,
	}
	if repo != "" {
		tg.Repo = repo
		tg.GitBranch = firstNonEmpty(branch, "main")
		owner, name := githubSplit(repo)
		tg.GithubOwner = owner
		tg.GithubRepo = name
	} else if parentRunID != "" {
		if parent, perr := b.GetRun(ctx, parentRunID); perr == nil && parent != nil {
			tg.Repo = parent.Repo
			tg.RepoURL = parent.RepoURL
			tg.GitBranch = firstNonEmpty(branch, parent.GitBranch)
			tg.GitSHA = parent.GitSHA
			tg.GithubOwner = parent.GithubOwner
			tg.GithubRepo = parent.GithubRepo
		}
	}

	if parentRunID != "" && parentNodeID != "" {
		idxKey := childTriggerKey(parentRunID, parentNodeID, pipeline)
		idxBody, err := json.Marshal(childTriggerIndex{TriggerID: runID})
		if err != nil {
			return "", err
		}
		_, err = cw.PutIfAbsent(ctx, idxKey, bytes.NewReader(idxBody))
		if errors.Is(err, storage.ErrPreconditionFailed) {
			var existing childTriggerIndex
			if _, gerr := getRecord(ctx, cw, idxKey, &existing); gerr != nil {
				return "", gerr
			}
			return existing.TriggerID, nil
		}
		if err != nil {
			return "", err
		}
	}

	body, err := json.Marshal(tg)
	if err != nil {
		return "", err
	}
	if _, err := cw.PutIfAbsent(ctx, triggerKey(runID), bytes.NewReader(body)); err != nil {
		return "", err
	}
	return runID, nil
}

// ancestorPipelines returns the pipeline names of runID and its parent
// chain, oldest-walk bounded by maxAncestorDepth. A missing run record
// stops the walk: cycle detection is best-effort over the records the
// bucket actually holds.
func (b *Backend) ancestorPipelines(ctx context.Context, runID string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for cur := runID; cur != "" && len(out) < maxAncestorDepth; {
		if seen[cur] {
			break
		}
		seen[cur] = true
		r, err := b.GetRun(ctx, cur)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				break
			}
			return nil, err
		}
		out = append(out, r.Pipeline)
		cur = r.ParentRunID
	}
	return out, nil
}

// triggerRunID mints a run ID for an enqueued trigger, matching the
// shape the local store's EnqueueTrigger produces.
func triggerRunID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("run-%s-%08x", now.Format("20060102-150405"), now.UnixNano()&0xFFFFFFFF)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// githubSplit returns owner+repo from an "owner/repo" slug.
func githubSplit(slug string) (owner, repo string) {
	i := strings.IndexByte(slug, '/')
	if i <= 0 || i == len(slug)-1 {
		return "", ""
	}
	return slug[:i], slug[i+1:]
}
