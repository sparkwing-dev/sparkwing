package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
)

type signedStore struct {
	signer DataSigner
	client HTTPClient
}

func newSignedStore(options SignedStoreOptions) Store {
	client := options.HTTPClient
	if client == nil {
		client = TransferClient{}
	}
	return &signedStore{signer: options.Signer, client: client}
}

func (s *signedStore) Put(ctx context.Context, req PutRequest) (Receipt, error) {
	if s.signer == nil {
		return Receipt{}, fmt.Errorf("blob signer is required")
	}
	if !validUpload(req.Identity) || !validDigest(req.Digest) || req.Size < 0 || req.Body == nil {
		return Receipt{}, fmt.Errorf("invalid blob upload")
	}
	ticket, err := s.signer.Upload(ctx, UploadIntent{Identity: req.Identity, Authority: req.Authority, Digest: req.Digest, Size: req.Size})
	if err != nil {
		return Receipt{}, err
	}
	if ticket.ID == "" || ticket.Transfer.Digest != req.Digest || ticket.Transfer.Size != req.Size {
		return Receipt{}, ErrIntegrity
	}
	reader := newVerifiedReader(io.NopCloser(req.Body), ticket.Transfer)
	response, err := s.client.Transfer(ctx, ticket.Transfer, reader)
	if response != nil {
		err = errors.Join(err, response.Close())
	}
	if err != nil {
		return Receipt{}, err
	}
	if reader.count != req.Size {
		return Receipt{}, ErrIntegrity
	}
	// safety: HTTP may consume exactly Content-Length without asking the source for EOF.
	if !reader.Verified() {
		var extra [1]byte
		_, err = reader.Read(extra[:])
		if !errors.Is(err, io.EOF) {
			return Receipt{}, errors.Join(ErrIntegrity, err)
		}
	}
	if !reader.Verified() {
		return Receipt{}, ErrIntegrity
	}
	receipt, err := s.signer.Commit(ctx, CommitUpload{Authority: req.Authority, TicketID: ticket.ID})
	if err != nil {
		return Receipt{}, err
	}
	if receipt.Digest != req.Digest || receipt.Identity != req.Identity {
		return Receipt{}, ErrIntegrity
	}
	return receipt, nil
}

func (s *signedStore) Get(ctx context.Context, req GetRequest) (VerifiedReader, error) {
	if s.signer == nil {
		return nil, fmt.Errorf("blob signer is required")
	}
	if !validDigest(req.Digest) {
		return nil, ErrIntegrity
	}
	transfer, err := s.signer.Download(ctx, req)
	if err != nil {
		return nil, err
	}
	if transfer.Digest != req.Digest || transfer.Size < 0 {
		return nil, ErrIntegrity
	}
	body, err := s.client.Transfer(ctx, transfer, nil)
	if err != nil {
		if body != nil {
			_ = body.Close()
		}
		return nil, err
	}
	if body == nil {
		return nil, fmt.Errorf("blob transfer returned no body")
	}
	return newVerifiedReader(body, transfer), nil
}

func (s *signedStore) HasMany(ctx context.Context, req HasManyRequest) (Presence, error) {
	if s.signer == nil {
		return Presence{}, fmt.Errorf("blob signer is required")
	}
	if req.Budget.Deadline.IsZero() {
		return Presence{}, fmt.Errorf("blob presence requires an explicit budget")
	}
	ctx, cancel := context.WithDeadline(ctx, req.Budget.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Presence{}, budgetError(err)
	}
	unique := make([]Digest, 0, len(req.Digests))
	requested := make(map[Digest]bool, len(req.Digests))
	for _, d := range req.Digests {
		if !validDigest(d) {
			return Presence{}, ErrIntegrity
		}
		if !requested[d] {
			unique = append(unique, d)
			requested[d] = true
		}
	}
	req.Digests = unique
	result, err := s.signer.HasMany(ctx, req)
	if err != nil {
		return Presence{}, budgetError(err)
	}
	if err := ctx.Err(); err != nil {
		return Presence{}, budgetError(err)
	}
	seen := make(map[Digest]bool, len(result.Present))
	var present []Digest
	for _, d := range result.Present {
		if !requested[d] {
			return Presence{}, ErrIntegrity
		}
		if !seen[d] {
			present = append(present, d)
			seen[d] = true
		}
	}
	return Presence{Present: present}, nil
}

func budgetError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrBudget, err)
	}
	return err
}

type verifiedReader struct {
	body             io.ReadCloser
	want             Digest
	size, count      int64
	hash             hash.Hash
	verified, closed bool
	err              error
}

func newVerifiedReader(body io.ReadCloser, transfer SignedTransfer) *verifiedReader {
	return &verifiedReader{body: body, want: transfer.Digest, size: transfer.Size, hash: sha256.New()}
}

func (r *verifiedReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.verified {
		return 0, io.EOF
	}
	n, err := r.body.Read(p)
	_, _ = r.hash.Write(p[:n])
	if int64(n) > r.size-r.count {
		r.err = ErrIntegrity
		return n, r.err
	}
	r.count += int64(n)
	if errors.Is(err, io.EOF) {
		if r.count != r.size || Digest(hex.EncodeToString(r.hash.Sum(nil))) != r.want {
			r.err = ErrIntegrity
			return n, r.err
		}
		r.verified = true
	} else if err != nil {
		r.err = err
	}
	return n, err
}

func (r *verifiedReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.body.Close()
}

func (r *verifiedReader) Verified() bool { return r.verified }
