package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// LocalPublication exports terminal state and its selected result records to the local peer.
type LocalPublication struct {
	RunSnapshot
	Records []blob.RecordLookupResult `json:"publication_records,omitempty"`
	Results map[string]NodeResult     `json:"publication_results,omitempty"`
}

// ReadLocalPublication reads the local controller's completed result graph.
func (t *Tenant) ReadLocalPublication(ctx context.Context, run string) (publication LocalPublication, err error) {
	err = withBlobIO(ctx, func(reads *blobIO) error {
		publication, err = t.readLocalPublication(ctx, run, reads)
		return err
	})
	return publication, err
}

func (t *Tenant) readLocalPublication(ctx context.Context, run string, reads *blobIO) (LocalPublication, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return LocalPublication{}, err
	}
	defer rollbackOrLog(tx)
	tx.blobIO = reads
	snapshot, err := readSnapshotTx(ctx, tx, t.team, run)
	if err != nil {
		return LocalPublication{}, err
	}
	if snapshot.Run.FinishedAt == nil {
		return LocalPublication{}, notFound("completed local run", run)
	}
	result := LocalPublication{RunSnapshot: snapshot, Records: []blob.RecordLookupResult{}, Results: map[string]NodeResult{}}
	records := t.BlobRecords(nil).(*blobRecords)
	pending := false
	for _, node := range snapshot.Nodes {
		terminal, err := readStoredNodeResultTx(ctx, tx, t.team, node)
		if err != nil {
			return LocalPublication{}, err
		}
		result.Results[string(node.Ref.NodeID)] = terminal
		for _, key := range []*blob.RecordKey{node.OutputRecord, node.ManifestRecord} {
			if key == nil {
				continue
			}
			if key.Team != string(t.team) {
				return LocalPublication{}, blob.ErrDigestNotReadable
			}
			namespace, err := blob.RecordNamespace(*key)
			if err != nil {
				return LocalPublication{}, err
			}
			record, found, err := readBlobRecordTx(ctx, tx, *key, namespace)
			if err != nil {
				return LocalPublication{}, err
			}
			if !found {
				return LocalPublication{}, blob.ErrNotFound
			}
			members, err := records.readMembers(ctx, tx, record)
			if errors.Is(err, errBlobIOPending) {
				pending = true
				continue
			}
			if err != nil {
				return LocalPublication{}, err
			}
			result.Records = append(result.Records, blob.RecordLookupResult{Found: true, Record: record, Members: members})
		}
	}
	if pending {
		return LocalPublication{}, errBlobIOPending
	}
	return result, tx.Commit()
}
