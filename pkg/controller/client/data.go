package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	var result wire.DataRecordLookupResult
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
	expected := input.Key
	if input.VerifiedDefaultOnly {
		if !record.Key.Scope.Ref.Verified || record.Key.Scope.Ref.Ref == "" {
			return blob.RecordLookupResult{}, dataProtocolError("record lookup did not select a verified default ref")
		}
		expected.Scope.Ref = record.Key.Scope.Ref
	}
	if record.Key != expected {
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
	body := wire.DataRecordWriteRequest{Kind: input.Key.Kind, Pipeline: input.Key.Scope.Pipeline, Job: input.Key.Scope.Job, Key: input.Key.Scope.Key, Name: input.Key.Name, Digest: input.Digest, Members: []wire.DataMember{}}
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

func (c *dataClient) LookupMemo(ctx context.Context, input blob.MemoLookup) (blob.MemoHit, error) {
	return c.memo(ctx, input, "lookup")
}

func (c *dataClient) LookupDepcache(ctx context.Context, input blob.DepcacheLookup) (blob.DepcacheHit, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.DepcacheHit{}, err
	}
	authority, key := input.Authority, input.Key
	team := authority.Attempt.Value.Team
	if team == "" && authority.Upload.Set {
		team = authority.Upload.Value.Team
	}
	if team == "" || key.Repo != authority.Repo || key.Ref != authority.Ref.Scope {
		return blob.DepcacheHit{}, localHostClientRefusal()
	}
	body := wire.DataDepcacheLookupRequest{Name: key.Name, Key: key.Key, Deadline: dataDeadline(input.Budget)}
	var result wire.DataDepcacheLookupResult
	err := c.withBudget(ctx, input.Budget, func(ctx context.Context) error {
		return c.post(ctx, "depcache/lookup", "", body, &result)
	})
	if err != nil {
		return blob.DepcacheHit{}, err
	}
	if !result.Found {
		if result.Record != nil || result.Source != "" {
			return blob.DepcacheHit{}, dataProtocolError("missing cache includes a payload")
		}
		return blob.DepcacheHit{}, nil
	}
	if result.Record == nil {
		return blob.DepcacheHit{}, dataProtocolError("cache lookup has no payload")
	}
	record := decodeDataRecord(*result.Record, team)
	ref := key.Ref
	switch result.Source {
	case blob.RestoreOwnRef:
	case blob.RestorePRBase:
		if !authority.Ref.PullRequestBase.Set {
			return blob.DepcacheHit{}, dataProtocolError("cache lookup selected an unknown PR base")
		}
		ref = blob.RefScope{Ref: authority.Ref.PullRequestBase.Value, Verified: true}
	case blob.RestoreDefaultBranch:
		ref = blob.RefScope{Ref: authority.Ref.DefaultBranch, Verified: true}
	default:
		return blob.DepcacheHit{}, dataProtocolError("cache lookup selected an unknown source")
	}
	expected := blob.RecordKey{Team: team, Kind: blob.KindDepcache, Name: key.Name, Scope: blob.Scope{Repo: key.Repo, Ref: ref, Key: key.Key}}
	if ref.Ref == "" && ref.Verified || record.Key != expected || !dataDigestValid(record.Digest) {
		return blob.DepcacheHit{}, dataProtocolError("cache lookup changed selected key or digest")
	}
	return blob.DepcacheHit{Found: true, Record: record, Source: result.Source}, nil
}

func (c *dataClient) CopyMemo(ctx context.Context, input blob.CopyMemoRequest) (blob.MemoHit, error) {
	if !input.Lookup.Authority.Attempt.Set || input.Destination != input.Lookup.Authority.Attempt.Value {
		return blob.MemoHit{}, localHostClientRefusal()
	}
	return c.memo(ctx, input.Lookup, "copy")
}

func (c *dataClient) memo(ctx context.Context, input blob.MemoLookup, operation string) (blob.MemoHit, error) {
	if err := c.checkAuthority(input.Authority); err != nil {
		return blob.MemoHit{}, err
	}
	a, key := input.Authority, input.Key
	team := a.Attempt.Value.Team
	if team == "" && a.Upload.Set {
		team = a.Upload.Value.Team
	}
	if team == "" || key.Repo != a.Repo || key.Ref != a.Ref.Scope || key.Pipeline == "" || key.Job == "" || key.Hash == "" {
		return blob.MemoHit{}, localHostClientRefusal()
	}
	var result wire.DataMemoLookupResult
	body := wire.DataMemoLookupRequest{Pipeline: key.Pipeline, Job: key.Job, Hash: key.Hash, Deadline: dataDeadline(input.Budget)}
	if err := c.withBudget(ctx, input.Budget, func(ctx context.Context) error { return c.post(ctx, "memo/"+operation, "", body, &result) }); err != nil {
		return blob.MemoHit{}, err
	}
	if !result.Found {
		if result.Output != nil || result.Manifest != nil || len(result.Members) != 0 || result.Reader != nil {
			return blob.MemoHit{}, dataProtocolError("missing memo includes a payload")
		}
		return blob.MemoHit{}, nil
	}
	if result.Reader == nil || *result.Reader != a.Attempt.Value || result.Output == nil && result.Manifest == nil || result.Manifest == nil && len(result.Members) != 0 {
		return blob.MemoHit{}, dataProtocolError("memo differs from its authenticated reader or declared references")
	}
	for _, digest := range []*blob.Digest{result.Output, result.Manifest} {
		if digest != nil && !dataDigestValid(*digest) {
			return blob.MemoHit{}, dataProtocolError("memo result has an invalid digest")
		}
	}
	hit := blob.MemoHit{Found: true, ReadSet: blob.ReadSet{Reader: *result.Reader}, Members: make([]blob.Member, 0, len(result.Members))}
	if result.Output != nil {
		hit.Output = runspec.Optional[blob.Digest]{Set: true, Value: *result.Output}
	}
	if result.Manifest != nil {
		hit.Manifest = runspec.Optional[blob.Digest]{Set: true, Value: *result.Manifest}
	}
	for _, m := range result.Members {
		if !dataDigestValid(m.Digest) || m.Path == "" || m.Size < 0 {
			return blob.MemoHit{}, dataProtocolError("memo member has invalid file metadata")
		}
		hit.Members = append(hit.Members, blob.Member{Team: team, ManifestDigest: hit.Manifest.Value, Digest: m.Digest, Path: m.Path, Size: m.Size, Mode: m.Mode})
	}
	return hit, nil
}

func dataDigestValid(digest blob.Digest) bool {
	decoded, err := hex.DecodeString(string(digest))
	return err == nil && len(decoded) == 32 && string(digest) == strings.ToLower(string(digest))
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
