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
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

const maxSessionStateBytes = 4 << 20

// CASSessionStore persists an authenticated session head through an object
// store that enforces conditional writes. Each accepted state is also written
// once under its version and digest before the head advances.
type CASSessionStore struct {
	prefix   string
	writer   DistributedSessionObjectStore
	createMu sync.Mutex
}

type DistributedSessionObjectStore interface {
	storage.ConditionalWriter
	List(context.Context, string) ([]string, error)
	DistributedConditionalWrites()
}

func NewCASSessionStore(prefix string, writer DistributedSessionObjectStore) (*CASSessionStore, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" || writer == nil {
		return nil, fmt.Errorf("acceptance session store requires a prefix and conditional writer")
	}
	return &CASSessionStore{prefix: prefix, writer: writer}, nil
}

var _ SessionStore = (*CASSessionStore)(nil)

func (store *CASSessionStore) LoadOrCreate(ctx context.Context, seed SessionSeed, factory InitialSessionFactory, verifier SessionVerifier) (Session, error) {
	if factory == nil || verifier == nil {
		return Session{}, fmt.Errorf("acceptance session create requires a factory and verifier")
	}
	if err := store.requireConditionalWrites(ctx); err != nil {
		return Session{}, err
	}
	if current, _, err := store.loadHead(ctx, seed.ID, verifier); err == nil {
		return current, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return Session{}, err
	}
	store.createMu.Lock()
	defer store.createMu.Unlock()
	if current, _, err := store.loadHead(ctx, seed.ID, verifier); err == nil {
		return current, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return Session{}, err
	}

	initial, err := factory(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("seal initial acceptance session: %w", err)
	}
	if err := verifySessionState(ctx, verifier, initial); err != nil {
		return Session{}, fmt.Errorf("verify initial acceptance session: %w", err)
	}
	encoded, err := encodeStoredSession(initial)
	if err != nil {
		return Session{}, err
	}
	if _, err := store.writer.PutIfAbsent(ctx, store.headKey(initial.ID), bytes.NewReader(encoded)); err != nil {
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			return Session{}, fmt.Errorf("create acceptance session head: %w", err)
		}
		current, _, loadErr := store.loadHead(ctx, seed.ID, verifier)
		if loadErr != nil {
			return Session{}, fmt.Errorf("load concurrently created acceptance session: %w", loadErr)
		}
		currentEncoded, encodeErr := encodeStoredSession(current)
		if encodeErr != nil {
			return Session{}, encodeErr
		}
		if !bytes.Equal(currentEncoded, encoded) {
			return Session{}, fmt.Errorf("%w: concurrent genesis differs from deterministic signer result", ErrSessionConflict)
		}
		return current, nil
	}
	if err := store.commitWatermark(ctx, initial, encoded); err != nil {
		return Session{}, err
	}
	return initial, nil
}

func (store *CASSessionStore) CompareAndSwap(ctx context.Context, id string, expectedVersion uint64, expectedDigest, seedDigest string, next Session, verifier SessionVerifier) error {
	if verifier == nil {
		return fmt.Errorf("acceptance session compare-and-swap requires a verifier")
	}
	if err := store.requireConditionalWrites(ctx); err != nil {
		return err
	}
	current, etag, err := store.loadHead(ctx, id, verifier)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ErrSessionConflict
		}
		return err
	}
	if current.Version != expectedVersion || current.StateSeal.Digest != expectedDigest || current.SeedDigest != seedDigest {
		return ErrSessionConflict
	}
	if next.ID != id || next.SeedDigest != seedDigest || next.Version != current.Version+1 || next.PreviousStateDigest != current.StateSeal.Digest {
		return fmt.Errorf("acceptance session successor does not extend the durable head")
	}
	if err := verifySessionState(ctx, verifier, next); err != nil {
		return fmt.Errorf("verify acceptance session successor: %w", err)
	}
	encoded, err := encodeStoredSession(next)
	if err != nil {
		return err
	}
	if err := store.putCandidate(ctx, next, encoded); err != nil {
		return err
	}
	if _, err := store.writer.PutIfMatch(ctx, store.headKey(id), bytes.NewReader(encoded), etag); err != nil {
		if errors.Is(err, storage.ErrPreconditionFailed) {
			return ErrSessionConflict
		}
		return fmt.Errorf("advance acceptance session head: %w", err)
	}
	return store.commitWatermark(ctx, next, encoded)
}

func (store *CASSessionStore) requireConditionalWrites(ctx context.Context) error {
	ok, err := store.writer.ConditionalWritesSupported(ctx)
	if err != nil {
		return fmt.Errorf("probe acceptance session conditional writes: %w", err)
	}
	if !ok {
		return fmt.Errorf("acceptance session store refuses an endpoint without enforced conditional writes")
	}
	return nil
}

func (store *CASSessionStore) loadHead(ctx context.Context, id string, verifier SessionVerifier) (Session, storage.ETag, error) {
	reader, etag, err := store.writer.GetWithETag(ctx, store.headKey(id))
	if err != nil {
		return Session{}, "", err
	}
	defer reader.Close()
	current, err := decodeStoredSession(reader)
	if err != nil {
		return Session{}, "", fmt.Errorf("decode acceptance session head: %w", err)
	}
	if current.ID != id {
		return Session{}, "", fmt.Errorf("acceptance session head identity mismatch")
	}
	if err := verifySessionState(ctx, verifier, current); err != nil {
		return Session{}, "", fmt.Errorf("verify acceptance session head: %w", err)
	}
	if err := store.verifyOrRepairWatermark(ctx, current, verifier); err != nil {
		return Session{}, "", err
	}
	return current, etag, nil
}

func (store *CASSessionStore) putCandidate(ctx context.Context, session Session, encoded []byte) error {
	key := store.candidateKey(session)
	if _, err := store.writer.PutIfAbsent(ctx, key, bytes.NewReader(encoded)); err == nil {
		return nil
	} else if !errors.Is(err, storage.ErrPreconditionFailed) {
		return fmt.Errorf("stage acceptance session candidate: %w", err)
	}
	reader, _, err := store.writer.GetWithETag(ctx, key)
	if err != nil {
		return fmt.Errorf("read existing acceptance session candidate: %w", err)
	}
	defer reader.Close()
	existing, err := io.ReadAll(io.LimitReader(reader, maxSessionStateBytes+1))
	if err != nil {
		return fmt.Errorf("read existing acceptance session candidate: %w", err)
	}
	if len(existing) > maxSessionStateBytes || !bytes.Equal(existing, encoded) {
		return fmt.Errorf("%w: acceptance session candidate conflicts at version %d", ErrSessionConflict, session.Version)
	}
	return nil
}

func (store *CASSessionStore) commitWatermark(ctx context.Context, session Session, encoded []byte) error {
	key := store.historyKey(session)
	if _, err := store.writer.PutIfAbsent(ctx, key, bytes.NewReader(encoded)); err == nil {
		return nil
	} else if !errors.Is(err, storage.ErrPreconditionFailed) {
		return fmt.Errorf("commit acceptance session watermark: %w", err)
	}
	existing, err := store.readObject(ctx, key)
	if err != nil {
		return fmt.Errorf("read acceptance session watermark: %w", err)
	}
	if !bytes.Equal(existing, encoded) {
		return fmt.Errorf("%w: acceptance session watermark conflicts at version %d", ErrSessionConflict, session.Version)
	}
	return nil
}

func (store *CASSessionStore) verifyOrRepairWatermark(ctx context.Context, head Session, verifier SessionVerifier) error {
	prefix := store.sessionPrefix(head.ID) + "/history/"
	keys, err := store.writer.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("list acceptance session watermarks: %w", err)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		if head.Version != 1 {
			return fmt.Errorf("acceptance session head version %d has no durable watermark", head.Version)
		}
		encoded, encodeErr := encodeStoredSession(head)
		if encodeErr != nil {
			return encodeErr
		}
		return store.commitWatermark(ctx, head, encoded)
	}
	latestKey := keys[len(keys)-1]
	version, digest, err := parseHistoryKey(latestKey)
	if err != nil {
		return err
	}
	encoded, err := store.readObject(ctx, latestKey)
	if err != nil {
		return err
	}
	watermark, err := decodeStoredSession(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("decode acceptance session watermark: %w", err)
	}
	if err := verifySessionState(ctx, verifier, watermark); err != nil {
		return fmt.Errorf("verify acceptance session watermark: %w", err)
	}
	if watermark.Version != version || strings.TrimPrefix(watermark.StateSeal.Digest, "sha256:") != digest {
		return fmt.Errorf("acceptance session watermark key does not bind its state")
	}
	if version > head.Version {
		return fmt.Errorf("acceptance session head rollback: version %d is below watermark %d", head.Version, version)
	}
	if version == head.Version {
		if watermark.StateSeal.Digest != head.StateSeal.Digest {
			return fmt.Errorf("acceptance session head conflicts with its durable watermark")
		}
		return nil
	}
	if head.Version != version+1 || head.PreviousStateDigest != watermark.StateSeal.Digest {
		return fmt.Errorf("acceptance session head skips durable watermark %d", version)
	}
	headEncoded, err := encodeStoredSession(head)
	if err != nil {
		return err
	}
	return store.commitWatermark(ctx, head, headEncoded)
}

func parseHistoryKey(key string) (uint64, string, error) {
	base := key[strings.LastIndex(key, "/")+1:]
	parts := strings.Split(strings.TrimSuffix(base, ".json"), "-")
	if len(parts) != 2 || len(parts[0]) != 20 || len(parts[1]) != 64 {
		return 0, "", fmt.Errorf("malformed acceptance session watermark key %q", key)
	}
	version, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("malformed acceptance session watermark version: %w", err)
	}
	return version, parts[1], nil
}

func (store *CASSessionStore) readObject(ctx context.Context, key string) ([]byte, error) {
	reader, _, err := store.writer.GetWithETag(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	encoded, err := io.ReadAll(io.LimitReader(reader, maxSessionStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxSessionStateBytes {
		return nil, fmt.Errorf("acceptance session object exceeds %d bytes", maxSessionStateBytes)
	}
	return encoded, nil
}

func (store *CASSessionStore) headKey(id string) string {
	return store.sessionPrefix(id) + "/head.json"
}

func (store *CASSessionStore) historyKey(session Session) string {
	return fmt.Sprintf("%s/history/%020d-%s.json", store.sessionPrefix(session.ID), session.Version, strings.TrimPrefix(session.StateSeal.Digest, "sha256:"))
}

func (store *CASSessionStore) candidateKey(session Session) string {
	return fmt.Sprintf("%s/candidates/%020d-%s.json", store.sessionPrefix(session.ID), session.Version, strings.TrimPrefix(session.StateSeal.Digest, "sha256:"))
}

func (store *CASSessionStore) sessionPrefix(id string) string {
	sum := sha256.Sum256([]byte(id))
	return store.prefix + "/sessions/" + hex.EncodeToString(sum[:])
}

func encodeStoredSession(session Session) ([]byte, error) {
	encoded, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("encode acceptance session: %w", err)
	}
	if len(encoded) > maxSessionStateBytes {
		return nil, fmt.Errorf("acceptance session state exceeds %d bytes", maxSessionStateBytes)
	}
	return encoded, nil
}

func decodeStoredSession(reader io.Reader) (Session, error) {
	limited := io.LimitReader(reader, maxSessionStateBytes+1)
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return Session{}, err
	}
	if len(encoded) > maxSessionStateBytes {
		return Session{}, fmt.Errorf("acceptance session state exceeds %d bytes", maxSessionStateBytes)
	}
	var session Session
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		return Session{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Session{}, fmt.Errorf("acceptance session state contains trailing data")
	}
	return session, nil
}
