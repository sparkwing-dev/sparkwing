package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func checkBlobCollection(t testing.TB, ctx context.Context, root string, objects blob.ObjectBackend, transfers blob.HTTPClient) (err error) {
	s, err := teststore.OpenExisting(filepath.Join(root, "collection.db"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	operator := s.AsOperator()
	lease, err := operator.AcquireSingletonLease(ctx, "maintenance", "physical-gc", time.Hour)
	if err != nil {
		return err
	}
	random := rand.New(rand.NewPCG(24, 66))
	for seed := range 12 {
		if err := checkBlobCollectionGraph(ctx, teststore.SQL(t, s), operator, lease, objects, transfers, random, seed); err != nil {
			return fmt.Errorf("graph %d: %w", seed, err)
		}
	}
	return nil
}

type collectionGraph struct {
	team  string
	blobs []blob.Blob
	bytes [][]byte
	edges [][2]int
	roots []int
}

func (g collectionGraph) reachable() map[int]bool {
	found := make(map[int]bool)
	queue := append([]int(nil), g.roots...)
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		if found[i] {
			continue
		}
		found[i] = true
		for _, edge := range g.edges {
			if edge[0] == i {
				queue = append(queue, edge[1])
			}
		}
	}
	return found
}

func checkBlobCollectionGraph(ctx context.Context, db *sql.DB, op *store.Operator, lease store.SingletonLease, objects blob.ObjectBackend, transfers blob.HTTPClient, random *rand.Rand, seed int) error {
	graphs, err := seedCollectionGraphs(ctx, db, objects, transfers, random, seed)
	if err != nil {
		return err
	}
	var unreachable int64
	for _, g := range graphs {
		unreachable += int64(len(g.blobs) - len(g.reachable()))
	}
	if n, err := op.MarkBlobTombstones(ctx, lease, 100); err != nil || n != unreachable {
		return fmt.Errorf("marked=%d want=%d: %v", n, unreachable, err)
	}
	if n, err := op.CollectBlobTombstones(ctx, lease, 100, objects); err != nil || n != 0 {
		return fmt.Errorf("collected within grace=%d: %v", n, err)
	}
	if err := checkCollectionObjects(ctx, objects, transfers, graphs, false); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "UPDATE blob_tombstones SET marked_at=?", time.Now().Add(-25*time.Hour).UnixNano()); err != nil {
		return err
	}
	for i := range graphs {
		g := &graphs[i]
		g.edges = append(g.edges, [2]int{g.roots[0], 4})
		if _, err := db.ExecContext(ctx, "INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,?,?)", g.team, g.blobs[g.roots[0]].Digest, g.blobs[4].Digest); err != nil {
			return err
		}
	}
	unreachable = 0
	for _, g := range graphs {
		unreachable += int64(len(g.blobs) - len(g.reachable()))
	}
	if err := checkInterruptedCollection(ctx, db, op, lease, objects); err != nil {
		return err
	}
	if n, err := op.CollectBlobTombstones(ctx, lease, 100, objects); err != nil || n != unreachable {
		return fmt.Errorf("collected=%d want=%d: %v", n, unreachable, err)
	}
	if err := checkCollectionObjects(ctx, objects, transfers, graphs, true); err != nil {
		return err
	}
	if n, err := op.CollectBlobTombstones(ctx, lease, 100, objects); err != nil || n != 0 {
		return fmt.Errorf("collection replay=%d: %v", n, err)
	}
	for _, g := range graphs {
		if _, err := db.ExecContext(ctx, "DELETE FROM blob_records WHERE team=?", g.team); err != nil {
			return err
		}
	}
	remaining := int64(len(graphs)*16) - unreachable
	if n, err := op.MarkBlobTombstones(ctx, lease, 100); err != nil || n != remaining {
		return fmt.Errorf("remaining marks=%d want=%d: %v", n, remaining, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE blob_tombstones SET marked_at=?", time.Now().Add(-25*time.Hour).UnixNano()); err != nil {
		return err
	}
	if n, err := op.CollectBlobTombstones(ctx, lease, 100, objects); err != nil || n != remaining {
		return fmt.Errorf("last reference collection=%d want=%d: %v", n, remaining, err)
	}
	for i := range graphs {
		graphs[i].roots = nil
	}
	return checkCollectionObjects(ctx, objects, transfers, graphs, true)
}

func seedCollectionGraphs(ctx context.Context, db *sql.DB, objects blob.ObjectBackend, transfers blob.HTTPClient, random *rand.Rand, seed int) ([]collectionGraph, error) {
	graphs := make([]collectionGraph, 2)
	for team := range graphs {
		g := &graphs[team]
		g.team = fmt.Sprintf("collection-%d-%d", seed, team)
		if _, err := db.ExecContext(ctx, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,?,?)", g.team, time.Now().UnixNano(), time.Now().UnixNano()); err != nil {
			return nil, err
		}
		g.roots = []int{team}
		g.edges = [][2]int{{0, 2}, {2, 0}, {1, 3}, {3, 1}, {4, 5}, {5, 4}}
		for i := 6; i < 16; i++ {
			g.edges = append(g.edges, [2]int{int(random.Uint32N(uint32(i))), i})
		}
		for i := range 16 {
			body := make([]byte, seed*7+i*13)
			for j := range body {
				body[j] = byte(i + j + seed)
			}
			b := blob.Blob{Team: g.team, Digest: blob.Digest(fmt.Sprintf("%x", sha256.Sum256(body))), Size: int64(len(body)), CreatedAt: time.Now().Add(-200 * 24 * time.Hour)}
			g.blobs, g.bytes = append(g.blobs, b), append(g.bytes, body)
			if err := putCollectionObject(ctx, objects, transfers, b, body, fmt.Sprintf("ticket-%d", i)); err != nil {
				return nil, err
			}
			if _, err := db.ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?)", b.Team, b.Digest, b.Size, b.CreatedAt.UnixNano()); err != nil {
				return nil, err
			}
		}
		for _, edge := range g.edges {
			if _, err := db.ExecContext(ctx, "INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,?,?)", g.team, g.blobs[edge[0]].Digest, g.blobs[edge[1]].Digest); err != nil {
				return nil, err
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at) VALUES (?,'source_bundle','root','snapshot',?,'gh:1','manual:main','fixture','local',?,'publication','retired-run',0,0,?,?)`, g.team, g.blobs[g.roots[0]].Digest, []byte(`{}`), []byte(`{}`), g.blobs[0].CreatedAt.UnixNano()); err != nil {
			return nil, err
		}
	}
	return graphs, nil
}

func checkInterruptedCollection(ctx context.Context, db *sql.DB, op *store.Operator, lease store.SingletonLease, objects blob.ObjectBackend) error {
	failure := errors.New("collection checkpoint")
	for _, physicalDelete := range []bool{false, true} {
		calls := 0
		interrupted := blobBoundaryDeleter(func(ctx context.Context, b blob.Blob) error {
			calls++
			if physicalDelete {
				if err := objects.DeleteBlob(ctx, b); err != nil {
					return err
				}
			}
			return failure
		})
		if n, err := op.CollectBlobTombstones(ctx, lease, 100, interrupted); n != 0 || !errors.Is(err, failure) || calls != 1 {
			return fmt.Errorf("interrupted collection: removed=%d calls=%d: %v", n, calls, err)
		}
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM blobs").Scan(&rows); err != nil || rows != 32 {
			return fmt.Errorf("interrupted collection lost inventory: rows=%d: %v", rows, err)
		}
	}
	return nil
}

func putCollectionObject(ctx context.Context, objects blob.ObjectBackend, transfers blob.HTTPClient, b blob.Blob, body []byte, ticket string) error {
	pending, err := stageBoundaryObject(ctx, objects, transfers, b, body, ticket)
	if err != nil {
		return err
	}
	return objects.DeleteObject(ctx, pending)
}

func stageBoundaryObject(ctx context.Context, objects blob.ObjectBackend, transfers blob.HTTPClient, b blob.Blob, body []byte, ticket string) (string, error) {
	pending := "teams/" + b.Team + "/pending/" + ticket
	signed, err := objects.Sign(ctx, capability.ObjectToSign{Method: http.MethodPut, Key: pending, Size: b.Size, SHA256: string(b.Digest), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		return "", err
	}
	r, err := transfers.Transfer(ctx, collectionTransfer(signed), bytes.NewReader(body))
	if r != nil {
		err = errors.Join(err, r.Close())
	}
	if err != nil {
		return "", err
	}
	if err := objects.CommitObject(ctx, pending, b); err != nil {
		return "", err
	}
	return pending, nil
}

func collectionTransfer(signed capability.SignedObject) blob.SignedTransfer {
	return blob.SignedTransfer{URL: signed.URL, Headers: signed.Headers, Size: signed.Size, Digest: blob.Digest(signed.SHA256), ExpiresAt: signed.ExpiresAt}
}

func checkCollectionObjects(ctx context.Context, objects blob.ObjectBackend, transfers blob.HTTPClient, graphs []collectionGraph, collected bool) error {
	for _, g := range graphs {
		reachable := g.reachable()
		for i, b := range g.blobs {
			key, err := blob.ObjectKey(b.Team, b.Digest)
			if err != nil {
				return err
			}
			signed, err := objects.Sign(ctx, capability.ObjectToSign{Method: http.MethodGet, Key: key, Size: b.Size, SHA256: string(b.Digest), ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				return err
			}
			r, err := transfers.Transfer(ctx, collectionTransfer(signed), nil)
			if collected && !reachable[i] {
				if !errors.Is(err, blob.ErrNotFound) {
					if r != nil {
						err = errors.Join(err, r.Close())
					}
					return fmt.Errorf("unreachable object %s/%d survived: %v", g.team, i, err)
				}
				continue
			}
			if err != nil {
				return err
			}
			body, err := io.ReadAll(r)
			if err = errors.Join(err, r.Close()); err != nil || !bytes.Equal(body, g.bytes[i]) {
				return fmt.Errorf("retained object %s/%d differs: %v", g.team, i, err)
			}
		}
	}
	return nil
}

type blobBoundaryDeleter func(context.Context, blob.Blob) error

func (f blobBoundaryDeleter) DeleteBlob(ctx context.Context, value blob.Blob) error {
	return f(ctx, value)
}
