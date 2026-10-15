package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type dataClient struct{ transport *engineClient }

var (
	_ blob.DataSigner  = (*dataClient)(nil)
	_ blob.RecordStore = (*dataClient)(nil)
)

func (c *dataClient) Upload(ctx context.Context, input blob.UploadIntent) (blob.UploadTicket, error) {
	var result wire.DataUploadTicket
	request := wire.DataUploadRequest{Authority: dataAuthority(input.Authority), Identity: dataIdentity(input.Identity), Digest: input.Digest, Size: input.Size}
	err := c.post(ctx, "upload", rand.Text(), request, &result)
	if err == nil && (result.ID == "" || result.Transfer.Digest != input.Digest || result.Transfer.Size != input.Size) {
		err = dataProtocolError("upload ticket does not match the requested object")
	}
	if err == nil {
		err = validateDataTransfer(result.Transfer)
	}
	if err != nil {
		return blob.UploadTicket{}, err
	}
	return blob.UploadTicket{ID: result.ID, Transfer: blob.SignedTransfer(result.Transfer)}, nil
}

func (c *dataClient) Commit(ctx context.Context, input blob.CommitUpload) (blob.Receipt, error) {
	var result wire.DataReceipt
	if !validSelector(input.TicketID) {
		return blob.Receipt{}, &apierr.Error{Code: apierr.BadRequest, Message: "invalid upload ticket selector"}
	}
	err := c.post(ctx, "commit", "", wire.DataCommitRequest{Authority: dataAuthority(input.Authority), TicketID: input.TicketID}, &result)
	return blob.Receipt{Identity: blobIdentity(result.Identity), Digest: result.Digest, CreatedAt: result.CreatedAt}, err
}

func (c *dataClient) Download(ctx context.Context, input blob.GetRequest) (blob.SignedTransfer, error) {
	var result wire.DataSignedTransfer
	err := c.post(ctx, "download", "", wire.DataDownloadRequest{Authority: dataAuthority(input.Authority), Digest: input.Digest}, &result)
	if err == nil && result.Digest != input.Digest {
		err = dataProtocolError("download transfer does not match the requested object")
	}
	if err == nil {
		err = validateDataTransfer(result)
	}
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	return blob.SignedTransfer(result), nil
}

func (c *dataClient) HasMany(ctx context.Context, input blob.HasManyRequest) (blob.Presence, error) {
	var result wire.DataPresence
	err := c.withBudget(ctx, input.Budget, func(ctx context.Context) error {
		return c.post(ctx, "has-many", "", wire.DataHasManyRequest{Authority: dataAuthority(input.Authority), Digests: input.Digests, Deadline: dataDeadline(input.Budget)}, &result)
	})
	if err != nil {
		return blob.Presence{}, err
	}
	requested := make(map[blob.Digest]bool, len(input.Digests))
	for _, digest := range input.Digests {
		requested[digest] = true
	}
	for _, digest := range result.Present {
		if !requested[digest] {
			return blob.Presence{}, dataProtocolError("presence response includes an unrequested digest")
		}
	}
	return blob.Presence(result), nil
}

func (c *dataClient) LookupRecord(ctx context.Context, input blob.RecordLookup) (blob.RecordLookupResult, error) {
	var result wire.DataRecordLookupResult
	key := dataRecordKey(input.Key)
	err := c.lookup(ctx, "record", input.Budget, wire.DataLookupRequest{Authority: dataAuthority(input.Authority), Record: &key, VerifiedDefaultOnly: input.VerifiedDefaultOnly}, &result)
	if err != nil {
		return blob.RecordLookupResult{}, err
	}
	return blob.RecordLookupResult{Found: result.Found, Record: blobRecord(result.Record), Members: blobMembers(result.Members), ReadSet: blobReadSet(result.ReadSet)}, nil
}

func (c *dataClient) LookupMemo(ctx context.Context, input blob.MemoLookup) (blob.MemoHit, error) {
	var result wire.DataMemoHit
	key := dataMemoKey(input.Key)
	err := c.lookup(ctx, "memo", input.Budget, wire.DataLookupRequest{Authority: dataAuthority(input.Authority), Memo: &key}, &result)
	if err != nil {
		return blob.MemoHit{}, err
	}
	return blobMemoHit(result), nil
}

func (c *dataClient) LookupDepcache(ctx context.Context, input blob.DepcacheLookup) (blob.DepcacheHit, error) {
	var result wire.DataDepcacheHit
	key := wire.DataDepcacheKey{Repo: input.Key.Repo, Ref: wire.DataRefScope(input.Key.Ref), Name: input.Key.Name, Key: input.Key.Key}
	err := c.lookup(ctx, "depcache", input.Budget, wire.DataLookupRequest{Authority: dataAuthority(input.Authority), Depcache: &key}, &result)
	if err != nil {
		return blob.DepcacheHit{}, err
	}
	return blob.DepcacheHit{Found: result.Found, Record: blobRecord(result.Record), Source: result.Source, ReadSet: blobReadSet(result.ReadSet)}, nil
}

func (c *dataClient) CommitRecord(ctx context.Context, input blob.CommitRecordRequest) (blob.Record, error) {
	var result wire.DataRecord
	commit := wire.DataRecordCommit{
		Source: dataIdentity(input.Source), Authority: dataAuthority(input.Authority), Key: dataRecordKey(input.Key),
		Digest: input.Digest, ManifestDigest: input.ManifestDigest, Members: dataMembers(input.Members), ExpiresAt: input.ExpiresAt,
	}
	err := c.post(ctx, "records", "", wire.DataRecordWriteRequest{Operation: "commit", Commit: &commit}, &result)
	if err != nil {
		return blob.Record{}, err
	}
	return blobRecord(result), nil
}

func (c *dataClient) CopyMemo(ctx context.Context, input blob.CopyMemoRequest) (blob.MemoHit, error) {
	var result wire.DataMemoHit
	key := dataMemoKey(input.Lookup.Key)
	copy := wire.DataMemoCopy{Lookup: wire.DataLookupRequest{Authority: dataAuthority(input.Lookup.Authority), Memo: &key, Deadline: dataDeadline(input.Lookup.Budget)}, Destination: input.Destination}
	err := c.withBudget(ctx, input.Lookup.Budget, func(ctx context.Context) error {
		return c.post(ctx, "records", "", wire.DataRecordWriteRequest{Operation: "copy_memo", CopyMemo: &copy}, &result)
	})
	if err != nil {
		return blob.MemoHit{}, err
	}
	return blobMemoHit(result), nil
}

func (c *dataClient) post(ctx context.Context, operation, key string, input, output any) error {
	path := "/api/v1/data/" + operation
	return c.transport.request(ctx, "POST", path, path, key, input, output)
}

func (c *dataClient) lookup(ctx context.Context, kind string, budget blob.Budget, input wire.DataLookupRequest, output any) error {
	input.Deadline = dataDeadline(budget)
	selector, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode data lookup selector: %w", err)
	}
	query := url.Values{"lookup": {kind}, "selector": {string(selector)}}
	return c.withBudget(ctx, budget, func(ctx context.Context) error {
		return c.transport.request(ctx, "GET", "/api/v1/data/records", "/api/v1/data/records?"+query.Encode(), "", nil, output)
	})
}

func (c *dataClient) withBudget(ctx context.Context, budget blob.Budget, call func(context.Context) error) error {
	if budget.Deadline.IsZero() {
		return call(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(budget.Deadline) {
		return blob.ErrBudget
	}
	bounded, cancel := context.WithDeadline(ctx, budget.Deadline)
	defer cancel()
	err := call(bounded)
	if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) && bounded.Err() == context.DeadlineExceeded {
		return blob.ErrBudget
	}
	return err
}

func dataDeadline(budget blob.Budget) *time.Time {
	if budget.Deadline.IsZero() {
		return nil
	}
	return &budget.Deadline
}

func validateDataTransfer(transfer wire.DataSignedTransfer) error {
	u, err := url.Parse(transfer.URL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || transfer.Size < 0 || transfer.ExpiresAt.IsZero() {
		return dataProtocolError("invalid signed object transfer")
	}
	for name, value := range transfer.Headers {
		if strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Proxy-Authorization") || strings.EqualFold(name, "Authorization") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") {
			return dataProtocolError("signed object transfer contains reusable credentials")
		}
	}
	return nil
}

func dataProtocolError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}

// NewDataClient returns the shared authenticated signing and record transports.
// It uses EngineOptions' HTTP/api.sock transport and private bearer provider.
// Authority and upload identity in interface arguments are selectors to compare;
// the server derives both from current authentication and persisted state.
// Object transfer clients receive signed headers only, never the controller bearer.
func NewDataClient(options EngineOptions) (blob.DataSigner, blob.RecordStore, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return nil, nil, err
	}
	data := &dataClient{transport: c}
	return data, data, nil
}
