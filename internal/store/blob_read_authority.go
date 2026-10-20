package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type blobReadCandidate struct {
	kind                                        blob.Kind
	name, pipeline, repo, ref, run, node, scope string
	attempt, generation                         int64
	source                                      blob.UploadIdentity
	digest                                      blob.Digest
	facts                                       blob.RefFacts
}

const blobReadCandidateColumns = `b.kind,b.name,b.pipeline,b.repo_key,b.ref_scope,COALESCE(b.run_id,''),COALESCE(b.node_id,''),COALESCE(b.attempt,0),COALESCE(b.generation,0),b.source_identity_json,b.ref_facts_json,b.scope,b.digest`

func scanBlobReadCandidate(row rowScanner) (blobReadCandidate, error) {
	var c blobReadCandidate
	var source, facts []byte
	if err := row.Scan(&c.kind, &c.name, &c.pipeline, &c.repo, &c.ref, &c.run, &c.node, &c.attempt, &c.generation, &source, &facts, &c.scope, &c.digest); err != nil {
		return c, err
	}
	if err := json.Unmarshal(source, &c.source); err != nil {
		return c, err
	}
	if err := json.Unmarshal(facts, &c.facts); err != nil {
		return c, err
	}
	return c, nil
}

func blobRecordReadableTx(ctx context.Context, tx *storeTx, a blobActor, key blob.RecordKey, namespace string) error {
	c, err := scanBlobReadCandidate(tx.QueryRowContext(ctx, "SELECT "+blobReadCandidateColumns+" FROM blob_records b WHERE b.team=? AND b.kind=? AND b.scope=? AND b.name=?", key.Team, key.Kind, namespace, key.Name))
	if errors.Is(err, sql.ErrNoRows) {
		return blob.ErrNotFound
	}
	if err != nil {
		return err
	}
	if c.repo != string(key.Scope.Repo) || c.ref != key.Scope.Ref.CapabilityScope() || c.pipeline != key.Scope.Pipeline {
		return blob.ErrDigestNotReadable
	}
	if key.Kind == blob.KindMemo && (!c.source.Attempt.Set || c.source.Attempt.Value.NodeID != key.Scope.Job) {
		return blob.ErrDigestNotReadable
	}
	_, allowed, err := blobCandidateEdgeTx(ctx, tx, a, c)
	if err != nil {
		return err
	}
	if !allowed {
		return blob.ErrDigestNotReadable
	}
	return nil
}

func requireBlobMemoTx(ctx context.Context, tx *storeTx, a blobActor) error {
	if !a.authority.Attempt.Set {
		return blob.ErrDigestNotReadable
	}
	id := a.authority.Attempt.Value
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT spec_json FROM nodes WHERE team=? AND run_id=? AND node_id=?", id.Team, id.RunID, id.NodeID).Scan(&raw); err != nil {
		return err
	}
	var spec nodeproto.JobSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return err
	}
	if spec.Memo == nil {
		return blob.ErrDigestNotReadable
	}
	return nil
}

func blobReadSetTx(ctx context.Context, tx *storeTx, a blobActor, digest blob.Digest) (blob.ReadSet, error) {
	set := blob.ReadSet{Reader: a.authority.Attempt.Value}
	rows, err := tx.QueryContext(ctx, `SELECT `+blobReadCandidateColumns+` FROM blob_records b WHERE b.team=? AND b.repo_key=? AND (b.expires_at IS NULL OR b.expires_at>? OR EXISTS(SELECT 1 FROM pipeline_success_pins p WHERE p.team=b.team AND p.run_id=b.run_id)) AND (b.digest=? OR b.manifest_digest=? OR EXISTS(SELECT 1 FROM blob_members m WHERE m.team=b.team AND m.member_digest=? AND (m.manifest_digest=b.manifest_digest OR (b.kind='manifest' AND m.manifest_digest=b.digest))))`, a.capability.Team, a.authority.Repo, tx.now().UnixNano(), digest, digest, digest)
	if err != nil {
		return set, err
	}
	var candidates []blobReadCandidate
	for rows.Next() {
		c, err := scanBlobReadCandidate(rows)
		if err != nil {
			return set, errors.Join(err, rows.Close())
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return set, err
	}
	for _, c := range candidates {
		edge, allowed, err := blobCandidateEdgeTx(ctx, tx, a, c)
		if err != nil {
			return set, err
		}
		if !allowed {
			continue
		}
		edge.Reader = set.Reader
		edge.Source = c.source.Attempt.Value
		edge.Digest = digest
		edge.Record = blob.RecordKey{Team: a.capability.Team, Kind: c.kind, Name: c.name, Scope: blob.Scope{Repo: runspec.RepoKey(c.repo), Ref: c.facts.Scope, Pipeline: c.pipeline, Job: c.source.Attempt.Value.NodeID}}
		set.Edges = append(set.Edges, edge)
	}
	return set, nil
}

func blobCandidateEdgeTx(ctx context.Context, tx *storeTx, a blobActor, c blobReadCandidate) (blob.ReadEdge, bool, error) {
	edge := blob.ReadEdge{}
	if ((c.kind != blob.KindOutput && c.kind != blob.KindManifest) && c.ref != c.facts.Scope.CapabilityScope()) || (c.facts.Scope.Verified && (c.facts.AppDeliveryKey == "" || c.facts.SHA == "")) || (c.facts.DefaultBranchVerified && (!c.facts.Scope.Verified || c.facts.Scope.Ref != c.facts.DefaultBranch)) {
		return edge, false, nil
	}
	scope := a.capability.Data
	if scope.Use == capability.DataRemoteCache {
		allowed := c.facts.DefaultBranchVerified && slices.Contains(scope.ReadRefs, c.ref) && c.kind != blob.KindBinary && c.kind != blob.KindSourceBundle
		edge.Kind = blob.EdgeMemoScope
		return edge, allowed, nil
	}
	if c.kind == blob.KindSourceBundle && c.source.Kind == blob.UploadSubmission {
		if c.run != a.source.RunID {
			return edge, false, nil
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT request_json FROM runs WHERE team=? AND id=?", a.capability.Team, c.run).Scan(&raw); err != nil {
			return edge, false, err
		}
		var seal runspec.SealedRequest
		if err := json.Unmarshal(raw, &seal); err != nil {
			return edge, false, err
		}
		edge.Kind = blob.EdgeSealedSource
		return edge, seal.Request.Source.BundleDigest.Set && seal.Request.Source.BundleDigest.Value == string(c.digest), nil
	}
	if a.capability.Delivery == capability.DeliveryBootstrap || a.capability.Unit == capability.UnitNode && !a.authority.Attempt.Set {
		return edge, false, nil
	}
	if c.kind == blob.KindBinary || c.kind == blob.KindSourceBundle {
		edge.Kind = blob.EdgeOwnUpload
		return edge, c.source == a.source, nil
	}
	if c.kind == blob.KindMemo || c.kind == blob.KindDepcache {
		allowed := slices.Contains(scope.ReadRefs, c.ref)
		if c.ref != a.authority.Ref.Scope.CapabilityScope() {
			verifiedDefault := c.facts.DefaultBranchVerified && c.facts.Scope.Ref == a.authority.Ref.DefaultBranch
			verifiedBase := c.kind == blob.KindDepcache && a.authority.Ref.PullRequestBase.Set && c.facts.Scope.Ref == a.authority.Ref.PullRequestBase.Value
			allowed = allowed && c.facts.Scope.Verified && (verifiedDefault || verifiedBase)
		}
		if c.kind == blob.KindMemo {
			allowed = allowed && c.pipeline == a.pipeline && a.authority.Attempt.Set && c.source.Attempt.Value.NodeID == a.authority.Attempt.Value.NodeID
			if allowed {
				if err := requireBlobMemoTx(ctx, tx, a); errors.Is(err, blob.ErrDigestNotReadable) {
					return edge, false, nil
				} else if err != nil {
					return edge, false, err
				}
			}
		}
		edge.Kind = blob.EdgeMemoScope
		return edge, allowed, nil
	}
	if a.source.Kind == blob.UploadOwnerUnit && (a.capability.Unit == capability.UnitTrigger || a.capability.Unit == capability.UnitPlan) {
		if c.run != a.source.RunID {
			return edge, false, nil
		}
		selected, err := blobCandidateSelectedTx(ctx, tx, a.capability.Team, c)
		edge.Kind = blob.EdgeDependency
		return edge, selected, err
	}
	if source := a.source.Unit.Value.Source; a.source.Kind == blob.UploadOwnerUnit && a.capability.Unit == capability.UnitEval && source.Set {
		edge.Kind = blob.EdgeDependency
		return edge, c.kind == blob.KindOutput && c.run == source.Value.RunID && c.node == source.Value.NodeID && c.attempt == source.Value.Attempt && c.generation == source.Value.Generation, nil
	}
	if !a.authority.Attempt.Set {
		return edge, false, nil
	}
	reader := a.authority.Attempt.Value
	if c.run == reader.RunID && c.node == reader.NodeID && c.attempt == reader.Attempt && c.generation == reader.Generation {
		edge.Kind = blob.EdgeMemoHit
		return edge, true, nil
	}
	if c.run == "" || c.node == "" {
		return edge, false, nil
	}
	selected, err := blobCandidateSelectedTx(ctx, tx, a.capability.Team, c)
	if err != nil || !selected {
		return edge, false, err
	}
	if c.run == reader.RunID {
		deps, _, err := blobDependenciesTx(ctx, tx, Team(a.capability.Team), reader.RunID, reader.NodeID)
		edge.Kind = blob.EdgeDependency
		return edge, deps[c.node], err
	}
	_, refs, err := blobDependenciesTx(ctx, tx, Team(a.capability.Team), reader.RunID, reader.NodeID)
	if err != nil {
		return edge, false, err
	}
	if !slices.Contains(scope.ReadRefs, c.ref) {
		return edge, false, nil
	}
	for _, ref := range refs {
		if ref.Pipeline != c.pipeline || ref.Job != c.node {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM pipeline_success_pins WHERE team=? AND pipeline=? AND run_id=?", a.capability.Team, c.pipeline, c.run).Scan(&count); err != nil {
			return edge, false, err
		}
		edge.Kind = blob.EdgeDeclaredReference
		return edge, count == 1, nil
	}
	return edge, false, nil
}

func blobDependenciesTx(ctx context.Context, tx *storeTx, team Team, run, node string) (map[string]bool, []nodeproto.PipelineReference, error) {
	rows, err := tx.QueryContext(ctx, "SELECT node_id,spec_json FROM nodes WHERE team=? AND run_id=?", team, run)
	if err != nil {
		return nil, nil, err
	}
	specs := map[string]nodeproto.JobSpec{}
	groups := map[string][]string{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, nil, errors.Join(err, rows.Close())
		}
		var spec nodeproto.JobSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, nil, errors.Join(err, rows.Close())
		}
		specs[id] = spec
		for _, group := range spec.Groups {
			groups[group] = append(groups[group], id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	visited := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		s := specs[id]
		for _, dep := range append(slices.Clone(s.Needs), s.NeedsIfPresent...) {
			if _, ok := specs[dep]; ok {
				visit(dep)
			}
		}
		for _, input := range s.Consumes {
			if _, ok := specs[input.Producer]; ok {
				visit(input.Producer)
			}
		}
		for _, group := range s.NeedsGenerated {
			for _, member := range groups[group] {
				visit(member)
			}
		}
		if s.OnFailureOf != "" {
			visit(s.OnFailureOf)
		}
	}
	visit(node)
	delete(visited, node)
	return visited, specs[node].References, nil
}

// ReadableBlobs answers existence only within the transaction's authorized read set.
func (t *Tenant) ReadableBlobs(ctx context.Context, request blob.HasManyRequest) (blob.Presence, error) {
	result := blob.Presence{}
	if !request.Budget.Deadline.IsZero() && !t.s.now().Before(request.Budget.Deadline) {
		return result, blob.ErrBudget
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return result, err
	}
	defer rollbackOrLog(tx)
	result, err = readableBlobsTx(ctx, tx, t.team, request)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func readableBlobsTx(ctx context.Context, tx *storeTx, team Team, request blob.HasManyRequest) (blob.Presence, error) {
	result := blob.Presence{}
	p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: request.Authority.CapabilityRef})
	if err != nil {
		return result, err
	}
	grant := capability.DataRead
	if p.Capability.Delivery == capability.DeliveryBootstrap {
		grant = capability.SourceRead
	}
	a, err := resolveBlobAuthorityTx(ctx, tx, team, request.Authority, grant)
	if err != nil {
		return result, err
	}
	seen := map[blob.Digest]bool{}
	for _, digest := range request.Digests {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !request.Budget.Deadline.IsZero() && !tx.now().Before(request.Budget.Deadline) {
			return blob.Presence{}, blob.ErrBudget
		}
		if seen[digest] {
			continue
		}
		seen[digest] = true
		if _, err := requireBlobTx(ctx, tx, team, digest); errors.Is(err, blob.ErrNotFound) {
			continue
		} else if err != nil {
			return result, err
		}
		own, err := readOwnBlobReceiptTx(ctx, tx, a.source, digest)
		if err != nil {
			return result, err
		}
		set, err := blobReadSetTx(ctx, tx, a, digest)
		if err != nil {
			return result, err
		}
		if own.Set || len(set.Edges) > 0 {
			result.Present = append(result.Present, digest)
		}
	}
	if !request.Budget.Deadline.IsZero() && !tx.now().Before(request.Budget.Deadline) {
		return blob.Presence{}, blob.ErrBudget
	}
	return result, nil
}

func blobCandidateSelectedTx(ctx context.Context, tx *storeTx, team string, c blobReadCandidate) (bool, error) {
	row, err := readNodeTx(ctx, tx, Team(team), controllerwire.NodeRef{RunID: c.run, NodeID: c.node})
	if err != nil {
		return false, err
	}
	if row.Status != StatusDone {
		return false, nil
	}
	for _, key := range []*blob.RecordKey{row.OutputRecord, row.ManifestRecord} {
		if key == nil || key.Team != team || key.Kind != c.kind || key.Name != c.name {
			continue
		}
		namespace, err := blob.RecordNamespace(*key)
		if err != nil {
			return false, err
		}
		if namespace == c.scope {
			return true, nil
		}
	}
	return false, nil
}
