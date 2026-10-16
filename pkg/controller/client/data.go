package client

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type dataClient struct {
	transport *engineClient
	authority *blob.Authority
}

func (c *dataClient) checkAuthority(authority blob.Authority) error {
	if c.authority != nil && !reflect.DeepEqual(authority, *c.authority) {
		return localHostClientRefusal()
	}
	return nil
}

var (
	_ blob.DataSigner  = (*dataClient)(nil)
	_ blob.RecordStore = (*dataClient)(nil)
)

func (c *dataClient) Upload(ctx context.Context, input blob.UploadIntent) (blob.UploadTicket, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.UploadTicket{}, err
	}
	if c.authority != nil && input.Identity != c.authority.Upload.Value {
		return blob.UploadTicket{}, localHostClientRefusal()
	}
	var result wire.DataUploadTicket
	body := struct {
		Digest blob.Digest `json:"digest"`
		Size   int64       `json:"size"`
	}{input.Digest, input.Size}
	err := c.post(ctx, "upload", rand.Text(), body, &result)
	transfer := wire.DataSignedTransfer{URL: result.URL, Headers: result.Headers, ExpiresAt: result.ExpiresAt, Digest: result.SHA256, Size: result.Size}
	if err == nil && (result.UploadID == "" || transfer.Digest != input.Digest || transfer.Size != input.Size) {
		err = dataProtocolError("upload ticket differs from requested object")
	}
	if err == nil {
		err = validateDataTransfer(transfer)
	}
	if err != nil {
		return blob.UploadTicket{}, err
	}
	return blob.UploadTicket{ID: result.UploadID, Transfer: blob.SignedTransfer(transfer)}, nil
}

func (c *dataClient) Commit(ctx context.Context, input blob.CommitUpload) (blob.Receipt, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.Receipt{}, err
	}
	var result struct {
		Digest    blob.Digest `json:"digest"`
		CreatedAt time.Time   `json:"created_at"`
	}
	if !validSelector(input.TicketID) {
		return blob.Receipt{}, &apierr.Error{Code: apierr.BadRequest, Message: "invalid upload ticket selector"}
	}
	err := c.post(ctx, "commit", "", struct {
		UploadID string `json:"upload_id"`
	}{input.TicketID}, &result)
	return blob.Receipt{Identity: input.Authority.Upload.Value, Digest: result.Digest, CreatedAt: result.CreatedAt}, err
}

func (c *dataClient) Download(ctx context.Context, input blob.GetRequest) (blob.SignedTransfer, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.SignedTransfer{}, err
	}
	var result struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		SHA256  blob.Digest       `json:"sha256"`
		Size    int64             `json:"size"`
		Expires time.Time         `json:"expires"`
	}
	err := c.post(ctx, "download", "", struct {
		Digest blob.Digest `json:"digest"`
	}{input.Digest}, &result)
	transfer := wire.DataSignedTransfer{URL: result.URL, Headers: result.Headers, Digest: result.SHA256, Size: result.Size, ExpiresAt: result.Expires}
	if err == nil && result.SHA256 != input.Digest {
		err = dataProtocolError("download differs from requested digest")
	}
	if err == nil {
		err = validateDataTransfer(transfer)
	}
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	return blob.SignedTransfer(transfer), nil
}

func (c *dataClient) HasMany(ctx context.Context, input blob.HasManyRequest) (blob.Presence, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.Presence{}, err
	}
	var result wire.DataPresence
	body := struct {
		Digests  []blob.Digest `json:"digests"`
		Deadline *time.Time    `json:"deadline,omitempty"`
	}{input.Digests, dataDeadline(input.Budget)}
	if body.Digests == nil {
		body.Digests = []blob.Digest{}
	}
	err := c.withBudget(ctx, input.Budget, func(ctx context.Context) error { return c.post(ctx, "has-many", "", body, &result) })
	if err != nil {
		return blob.Presence{}, err
	}
	requested := map[blob.Digest]bool{}
	for _, digest := range input.Digests {
		requested[digest] = true
	}
	for _, digest := range result.Present {
		if !requested[digest] {
			return blob.Presence{}, dataProtocolError("presence includes unrequested digest")
		}
	}
	return blob.Presence(result), nil
}

func (c *dataClient) LookupRecord(ctx context.Context, input blob.RecordLookup) (blob.RecordLookupResult, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.RecordLookupResult{}, err
	}
	var result struct {
		Found   bool              `json:"found"`
		Record  *wire.DataRecord  `json:"record,omitempty"`
		Members []wire.DataMember `json:"members"`
	}
	key := input.Key
	query := url.Values{"kind": {string(key.Kind)}, "name": {key.Name}, "ref": {key.Scope.Ref.CapabilityScope()}, "pipeline": {key.Scope.Pipeline}, "job": {key.Scope.Job}, "key": {key.Scope.Key}, "verified_default_only": {strconv.FormatBool(input.VerifiedDefaultOnly)}}
	if !input.Budget.Deadline.IsZero() {
		query.Set("deadline", input.Budget.Deadline.Format(time.RFC3339Nano))
	}
	err := c.withBudget(ctx, input.Budget, func(ctx context.Context) error {
		return c.transport.request(ctx, "GET", "/api/v1/data/records", "/api/v1/data/records?"+query.Encode(), "", nil, &result)
	})
	if err != nil {
		return blob.RecordLookupResult{}, err
	}
	if !result.Found {
		if result.Record != nil {
			return blob.RecordLookupResult{}, dataProtocolError("missing record includes a payload")
		}
		return blob.RecordLookupResult{}, nil
	}
	if result.Record == nil {
		return blob.RecordLookupResult{}, dataProtocolError("record lookup has no payload")
	}
	record := decodeDataRecord(*result.Record, input.Key.Team)
	if record.Key != input.Key {
		return blob.RecordLookupResult{}, dataProtocolError("record lookup changed selected key")
	}
	members := make([]blob.Member, 0, len(result.Members))
	manifest := record.ManifestDigest.Value
	if record.Key.Kind == blob.KindManifest {
		manifest = record.Digest
	}
	for _, member := range result.Members {
		members = append(members, blob.Member{Team: key.Team, ManifestDigest: manifest, Digest: member.Digest, Path: member.Path, Size: member.Size, Mode: member.Mode})
	}
	return blob.RecordLookupResult{Found: true, Record: record, Members: members}, nil
}

func (c *dataClient) CommitRecord(ctx context.Context, input blob.CommitRecordRequest) (blob.Record, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.Record{}, err
	}
	if c.authority != nil && input.Source != c.authority.Upload.Value {
		return blob.Record{}, localHostClientRefusal()
	}
	body := struct {
		Kind           blob.Kind         `json:"kind"`
		Pipeline       string            `json:"pipeline"`
		Job            string            `json:"job"`
		Key            string            `json:"key"`
		Name           string            `json:"name"`
		Digest         blob.Digest       `json:"digest"`
		ManifestDigest *blob.Digest      `json:"manifest_digest,omitempty"`
		Members        []wire.DataMember `json:"members"`
		ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
	}{Kind: input.Key.Kind, Pipeline: input.Key.Scope.Pipeline, Job: input.Key.Scope.Job, Key: input.Key.Scope.Key, Name: input.Key.Name, Digest: input.Digest, Members: []wire.DataMember{}}
	if input.ManifestDigest.Set {
		body.ManifestDigest = &input.ManifestDigest.Value
	}
	if input.ExpiresAt.Set {
		body.ExpiresAt = &input.ExpiresAt.Value
	}
	for _, member := range input.Members {
		body.Members = append(body.Members, wire.DataMember{Digest: member.Digest, Path: member.Path, Size: member.Size, Mode: member.Mode})
	}
	var result wire.DataRecord
	if err := c.post(ctx, "records", "", body, &result); err != nil {
		return blob.Record{}, err
	}
	record := decodeDataRecord(result, input.Key.Team)
	if record.Key != input.Key || record.Digest != input.Digest || record.ManifestDigest != input.ManifestDigest {
		return blob.Record{}, dataProtocolError("record commit changed selected payload")
	}
	record.Source, record.Uploader, record.RefFacts = input.Authority.Upload.Value, input.Authority.Principal, input.Authority.Ref
	return record, nil
}

func decodeDataRecord(r wire.DataRecord, team string) blob.Record {
	ref, manual := strings.CutPrefix(r.Ref, "manual:")
	record := blob.Record{Key: blob.RecordKey{Team: team, Kind: r.Kind, Name: r.Name, Scope: blob.Scope{Repo: r.Repo, Ref: blob.RefScope{Ref: ref, Verified: !manual}, Pipeline: r.Pipeline, Job: r.Job, Key: r.Key}}, Digest: r.Digest, Provenance: r.Provenance, CreatedAt: r.CreatedAt}
	if r.ManifestDigest != nil {
		record.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: *r.ManifestDigest}
	}
	if r.ExpiresAt != nil {
		record.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: *r.ExpiresAt}
	}
	return record
}

func (c *dataClient) LookupMemo(context.Context, blob.MemoLookup) (blob.MemoHit, error) {
	return blob.MemoHit{}, missingDataProof()
}
func (c *dataClient) LookupDepcache(context.Context, blob.DepcacheLookup) (blob.DepcacheHit, error) {
	return blob.DepcacheHit{}, missingDataProof()
}
func (c *dataClient) CopyMemo(context.Context, blob.CopyMemoRequest) (blob.MemoHit, error) {
	return blob.MemoHit{}, missingDataProof()
}
func missingDataProof() error {
	return &apierr.Error{Code: apierr.PolicyMissing, Message: "controller cache lookup does not transport canonical read-set proof"}
}

func (c *dataClient) post(ctx context.Context, operation, key string, input, output any) error {
	path := "/api/v1/data/" + operation
	return c.transport.request(ctx, "POST", path, path, key, input, output)
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

// NewDataClient returns the authenticated byte and record transports.
func NewDataClient(options EngineOptions) (blob.DataSigner, blob.RecordStore, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return nil, nil, err
	}
	data := &dataClient{transport: c}
	return data, data, nil
}
