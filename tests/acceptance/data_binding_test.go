package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// fixtureMainRef is the unverified main scope of a run account/fixture submitted.
var fixtureMainRef = blob.RefScope{Ref: "main", Writer: "account/fixture"}

func dataAuthorized() capability.Authorized {
	p, _ := policyFixture("awarded_or_delegated_claim", "data", "controller", []capability.Grant{capability.DataRead, capability.DataWrite})
	p.Capability.Binding.Generation, p.Capability.Binding.Attempt = 4, 2
	p.Capability.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:12", RunID: "run", NodeID: "job", Generation: 4, Attempt: 2, WriteRef: fixtureMainRef.CapabilityScope(), ReadRefs: []string{fixtureMainRef.CapabilityScope(), "main"}}
	return capability.Authorized{
		Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "team"},
		Access: capability.AccessRule{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.DataRead, capability.DataWrite}, Binding: capability.BindData, Liveness: capability.LiveSensitive},
	}
}

type dataTransactions struct {
	events                                     *[]string
	ticket                                     store.UploadReservation
	reserveErr, lookupErr, settleErr, abortErr error
	lookup                                     blob.RecordLookup
	lookupResult                               blob.RecordLookupResult
	record                                     blob.CommitRecordRequest
	memo                                       blob.MemoLookup
	depcache                                   blob.DepcacheLookup
	copy                                       blob.CopyMemoRequest
}

func (f *dataTransactions) CommitRecord(_ context.Context, r blob.CommitRecordRequest) (blob.Record, error) {
	*f.events = append(*f.events, "commit record")
	f.record = r
	return blob.Record{Key: r.Key, Digest: r.Digest, Source: r.Source}, f.settleErr
}

func (f *dataTransactions) LookupMemo(_ context.Context, r blob.MemoLookup) (blob.MemoHit, error) {
	*f.events = append(*f.events, "memo")
	f.memo = r
	return blob.MemoHit{Found: true}, f.lookupErr
}

func (f *dataTransactions) LookupDepcache(_ context.Context, r blob.DepcacheLookup) (blob.DepcacheHit, error) {
	*f.events = append(*f.events, "depcache")
	f.depcache = r
	return blob.DepcacheHit{Found: true}, f.lookupErr
}

func (f *dataTransactions) CopyMemo(_ context.Context, r blob.CopyMemoRequest) (blob.MemoHit, error) {
	*f.events = append(*f.events, "copy")
	f.copy = r
	return blob.MemoHit{Found: true}, f.settleErr
}

func TestDataRecordRequiresEveryReferenceEdge(t *testing.T) {
	for _, state := range []string{"allowed", "primary denied", "manifest denied", "member denied", "sibling source", "foreign repository", "real-ref forgery", "foreign member"} {
		t.Run(state, func(t *testing.T) {
			a := dataAuthorized()
			s, f, _, edges, events := dataControllerFixture(a)
			binding, err := s.BindData(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			manifest, member := blob.Digest(strings.Repeat("c", 64)), blob.Digest(strings.Repeat("d", 64))
			request := blob.CommitRecordRequest{
				Authority: binding.Authority, Source: binding.Authority.Upload.Value,
				Key: blob.RecordKey{Team: "team", Kind: blob.KindOutput, Scope: blob.Scope{Repo: "gh:12", Ref: fixtureMainRef, Job: "job"}, Name: "output"}, Digest: dataDigest(),
				ManifestDigest: runspec.Optional[blob.Digest]{Set: true, Value: manifest}, Members: []blob.Member{{Team: "team", ManifestDigest: manifest, Digest: member, Path: "file", Size: 1}},
			}
			switch state {
			case "primary denied":
				edges.denyDigest = dataDigest()
			case "manifest denied":
				edges.denyDigest = manifest
			case "member denied":
				edges.denyDigest = member
			case "sibling source":
				request.Source.Attempt.Value.NodeID = "sibling"
			case "foreign repository":
				request.Key.Scope.Repo = "gh:13"
			case "real-ref forgery":
				request.Key.Scope.Ref = blob.RefScope{Ref: "main", Verified: true}
			case "foreign member":
				request.Members[0].Team = "other"
			}
			record, err := binding.Records.CommitRecord(t.Context(), request)
			committed := slices.Contains(*events, "commit record")
			if state == "allowed" {
				if err != nil || !committed || record.Digest != dataDigest() || !reflect.DeepEqual(f.record, request) {
					t.Fatalf("record=%+v request=%+v err=%v", record, f.record, err)
				}
			} else if err == nil || committed || record.Digest != "" {
				t.Fatalf("unauthorized record: %+v events=%v err=%v", record, *events, err)
			}
		})
	}
}

func TestDataCacheOperationsPreserveBoundAuthorityAndDestination(t *testing.T) {
	a := dataAuthorized()
	s, f, _, _, _ := dataControllerFixture(a)
	binding, err := s.BindData(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	memo := blob.MemoLookup{Authority: binding.Authority, Key: blob.MemoKey{Repo: "gh:12", Ref: fixtureMainRef, Pipeline: "build", Job: "job", Hash: "memo-hash"}, Budget: blob.Budget{Deadline: time.Unix(42, 0)}}
	if _, err := binding.Records.LookupMemo(t.Context(), memo); err != nil || f.memo != memo {
		t.Fatalf("memo=%+v err=%v", f.memo, err)
	}
	depcache := blob.DepcacheLookup{Authority: binding.Authority, Key: blob.DepcacheKey{Repo: "gh:12", Ref: memo.Key.Ref, Name: "go", Key: "sum"}, Budget: memo.Budget}
	if _, err := binding.Records.LookupDepcache(t.Context(), depcache); err != nil || f.depcache != depcache {
		t.Fatalf("depcache=%+v err=%v", f.depcache, err)
	}
	copy := blob.CopyMemoRequest{Lookup: memo, Destination: binding.Authority.Attempt.Value}
	if _, err := binding.Records.CopyMemo(t.Context(), copy); err != nil || f.copy != copy {
		t.Fatalf("copy=%+v err=%v", f.copy, err)
	}
	copy.Destination.NodeID = "sibling"
	if _, err := binding.Records.CopyMemo(t.Context(), copy); err == nil {
		t.Fatal("copied memo into sibling")
	}
	copy.Destination = binding.Authority.Attempt.Value
	copy.Lookup.Key.Ref.Ref = "unreadable-ref"
	if _, err := binding.Records.CopyMemo(t.Context(), copy); err == nil {
		t.Fatal("copied memo from an unauthorized ref")
	}
	memo.Key.Repo = "gh:13"
	if _, err := binding.Records.LookupMemo(t.Context(), memo); err == nil {
		t.Fatal("read memo from foreign repository")
	}
}

func TestDataMemoCopyRequiresReadAndWriteAtTheBoundInterface(t *testing.T) {
	for _, grant := range []capability.Grant{capability.DataRead, capability.DataWrite} {
		a := dataAuthorized()
		a.Access.Grants = []capability.Grant{grant}
		s, _, _, _, events := dataControllerFixture(a)
		binding, err := s.BindData(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
		request := blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: binding.Authority, Key: blob.MemoKey{Repo: "gh:12", Ref: binding.Authority.Ref.Scope, Pipeline: "build", Job: "job", Hash: "hash"}}, Destination: binding.Authority.Attempt.Value}
		if _, err := binding.Records.CopyMemo(t.Context(), request); err == nil || slices.Contains(*events, "copy") {
			t.Fatalf("single grant %s copied memo: %v %v", grant, err, *events)
		}
	}
}

func (f *dataTransactions) LookupRecord(_ context.Context, r blob.RecordLookup) (blob.RecordLookupResult, error) {
	*f.events = append(*f.events, "lookup record")
	f.lookup = r
	return f.lookupResult, f.lookupErr
}

func TestDataRemoteCacheForcesVerifiedDefaultLookup(t *testing.T) {
	a := dataAuthorized()
	p, _ := policyFixture("repo_cache_reader", "data", "controller", []capability.Grant{capability.DataRead})
	p.Capability.Data.RepositoryKey = "gh:12"
	a.Principal, a.Binding = p, p.Capability.Binding
	a.Access = capability.AccessRule{Credential: capability.CredentialRepoReader, Grants: []capability.Grant{capability.DataRead}, Binding: capability.BindData, Liveness: capability.LiveCredential}
	s, f, _, _, _ := dataControllerFixture(a)
	authority := dataAuthority(a)
	authority.Attempt, authority.Upload = runspec.Optional[runspec.AttemptIdentity]{}, runspec.Optional[blob.UploadIdentity]{}
	s.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) { return authority, nil }
	binding, err := s.BindData(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	key := blob.RecordKey{Team: "team", Kind: blob.KindDepcache, Scope: blob.Scope{Repo: "gh:12"}, Name: "cache"}
	_, err = binding.Records.LookupRecord(t.Context(), blob.RecordLookup{Authority: binding.Authority, Key: key})
	if err != nil || !f.lookup.VerifiedDefaultOnly || f.lookup.Authority != authority {
		t.Fatalf("lookup=%+v err=%v", f.lookup, err)
	}
	_, err = binding.Records.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: binding.Authority, Key: key, Digest: dataDigest()})
	assertPolicyCode(t, err, apierr.Forbidden)
	key.Kind = blob.KindBinary
	_, err = binding.Records.LookupRecord(t.Context(), blob.RecordLookup{Authority: binding.Authority, Key: key})
	assertPolicyCode(t, err, apierr.Forbidden)
	key.Kind = blob.KindDepcache
	for _, state := range []string{"verified", "manual", "unverified default", "missing delivery", "missing SHA", "foreign repo", "foreign team"} {
		t.Run(state, func(t *testing.T) {
			record := blob.Record{Key: key, Digest: dataDigest(), RefFacts: blob.RefFacts{Scope: blob.RefScope{Ref: "main", Verified: true}, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "verified-delivery", SHA: strings.Repeat("a", 40)}}
			record.Key.Scope.Ref = record.RefFacts.Scope
			switch state {
			case "manual":
				record.RefFacts.Scope.Verified, record.Key.Scope.Ref.Verified = false, false
			case "unverified default":
				record.RefFacts.DefaultBranchVerified = false
			case "missing delivery":
				record.RefFacts.AppDeliveryKey = ""
			case "missing SHA":
				record.RefFacts.SHA = ""
			case "foreign repo":
				record.Key.Scope.Repo = "gh:13"
			case "foreign team":
				record.Key.Team = "other"
			}
			f.lookupResult = blob.RecordLookupResult{Found: true, Record: record}
			got, err := binding.Records.LookupRecord(t.Context(), blob.RecordLookup{Authority: binding.Authority, Key: key})
			if state == "verified" {
				if err != nil || !got.Found || got.Record.Digest != dataDigest() {
					t.Fatalf("record=%+v err=%v", got, err)
				}
			} else if err == nil || got.Found {
				t.Fatalf("unsafe remote cache record=%+v err=%v", got, err)
			}
		})
	}
}

func (f *dataTransactions) ReserveBlobUpload(_ context.Context, r store.UploadReservationRequest) (store.UploadReservation, error) {
	*f.events = append(*f.events, "reserve")
	f.ticket = store.UploadReservation{
		ID: r.ID, Identity: r.Intent.Identity, Blob: blob.Blob{Team: "team", Digest: r.Intent.Digest, Size: r.Intent.Size},
		StagingKey: "teams/team/pending/" + r.ID, ObjectKey: "teams/team/cas/" + string(r.Intent.Digest), ExpiresAt: r.ExpiresAt,
	}
	return f.ticket, f.reserveErr
}

func (f *dataTransactions) LookupBlobUpload(_ context.Context, _ blob.Authority, ticket string) (store.UploadReservation, error) {
	*f.events = append(*f.events, "ticket")
	if ticket != f.ticket.ID {
		return store.UploadReservation{}, store.ErrNotFound
	}
	return f.ticket, f.lookupErr
}

func (f *dataTransactions) CommitBlobUpload(_ context.Context, r store.VerifiedUploadCommit) (blob.Receipt, error) {
	*f.events = append(*f.events, "settle")
	if r.TicketID != f.ticket.ID || r.Verified != f.ticket.Blob {
		panic("substituted committed upload")
	}
	return blob.Receipt{Identity: f.ticket.Identity, Digest: f.ticket.Blob.Digest}, f.settleErr
}

func (f *dataTransactions) AbortBlobUpload(context.Context, blob.Authority, string) error {
	*f.events = append(*f.events, "abort")
	return f.abortErr
}

type dataObjects struct {
	events                        *[]string
	signed                        capability.ObjectToSign
	signErr, verifyErr, deleteErr error
	onVerified                    func()
}

func (f *dataObjects) Sign(_ context.Context, o capability.ObjectToSign) (capability.SignedObject, error) {
	*f.events = append(*f.events, "sign")
	f.signed = o
	return capability.SignedObject{URL: "https://objects.example/one", Size: o.Size, SHA256: o.SHA256, ExpiresAt: o.ExpiresAt}, f.signErr
}

func (f *dataObjects) CommitObject(context.Context, string, blob.Blob) error {
	*f.events = append(*f.events, "verify")
	if f.onVerified != nil {
		f.onVerified()
	}
	return f.verifyErr
}

func (f *dataObjects) DeleteBlob(context.Context, blob.Blob) error {
	*f.events = append(*f.events, "delete-blob")
	return f.deleteErr
}

func (f *dataObjects) VerifyManifest(context.Context, string, blob.Digest, []blob.Member) error {
	*f.events = append(*f.events, "verify-manifest")
	return blob.ErrNotFound
}

func (f *dataObjects) ReadManifest(context.Context, string, blob.Digest) ([]blob.Member, error) {
	*f.events = append(*f.events, "read-manifest")
	return nil, blob.ErrNotFound
}

func (f *dataObjects) VerifyBlob(context.Context, blob.Blob) error {
	return blob.ErrNotFound
}

func (f *dataObjects) DeleteObject(context.Context, string) error {
	*f.events = append(*f.events, "delete")
	return f.deleteErr
}

type dataEdges struct {
	events     *[]string
	err        error
	denyDigest blob.Digest
}

func (f *dataEdges) CheckDataAccess(_ context.Context, _ capability.Authorized, target capability.DataTarget) error {
	*f.events = append(*f.events, "authorize")
	if target.Digest != "" && blob.Digest(target.Digest) == f.denyDigest {
		return blob.ErrDigestNotReadable
	}
	return f.err
}

func TestDataHasManyHidesUnreadableAndMissingDigests(t *testing.T) {
	a := dataAuthorized()
	s, _, _, edges, _ := dataControllerFixture(a)
	denied, missing := blob.Digest(strings.Repeat("a", 64)), blob.Digest(strings.Repeat("b", 64))
	edges.denyDigest = denied
	reads := []blob.Digest{}
	s.ReadBlob = func(_ context.Context, _ blob.Authority, digest blob.Digest) (blob.Blob, error) {
		reads = append(reads, digest)
		if digest == missing {
			return blob.Blob{}, blob.ErrNotFound
		}
		return blob.Blob{Team: "team", Digest: digest, Size: 14}, nil
	}
	binding, err := s.BindData(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	request := blob.HasManyRequest{Authority: binding.Authority, Digests: []blob.Digest{denied, missing, dataDigest(), dataDigest()}}
	got, err := binding.Signer.HasMany(t.Context(), request)
	if err != nil || !reflect.DeepEqual(got.Present, []blob.Digest{dataDigest()}) || !reflect.DeepEqual(reads, []blob.Digest{missing, dataDigest()}) {
		t.Fatalf("presence=%+v reads=%v err=%v", got, reads, err)
	}
	request.Budget.Deadline = time.Unix(1, 0)
	if _, err := binding.Signer.HasMany(t.Context(), request); !errors.Is(err, blob.ErrBudget) {
		t.Fatalf("expired budget: %v", err)
	}
	request.Budget.Deadline = time.Time{}
	edges.err = &apierr.Error{Code: apierr.Forbidden, Message: "not readable", Details: map[string]any{"reason": apierr.ReasonDigestNotReadable}}
	if got, err := binding.Signer.HasMany(t.Context(), request); err != nil || len(got.Present) != 0 {
		t.Fatalf("typed unreadable reason: %+v %v", got, err)
	}
	edges.err = &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked"}
	if _, err := binding.Signer.HasMany(t.Context(), request); err == nil {
		t.Fatal("revoked authority returned absence")
	}
}

func TestDataDownloadRequiresLiveExistenceAndReadAuthority(t *testing.T) {
	for _, state := range []string{"readable", "denied", "missing", "foreign blob", "corrupt size", "scope changed"} {
		t.Run(state, func(t *testing.T) {
			a := dataAuthorized()
			s, _, objects, edges, events := dataControllerFixture(a)
			reads := 0
			s.ReadBlob = func(_ context.Context, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
				reads++
				if authority != dataAuthority(a) || digest != dataDigest() {
					t.Fatal("unbound blob read")
				}
				switch state {
				case "missing":
					return blob.Blob{}, blob.ErrNotFound
				case "foreign blob":
					return blob.Blob{Team: "other", Digest: digest, Size: 14}, nil
				case "corrupt size":
					return blob.Blob{Team: "team", Digest: digest, Size: -1}, nil
				}
				return blob.Blob{Team: "team", Digest: digest, Size: 14}, nil
			}
			binding, err := s.BindData(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			if state == "denied" {
				edges.err = blob.ErrDigestNotReadable
			}
			if state == "scope changed" {
				s.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) {
					return blob.Authority{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked"}
				}
			}
			transfer, err := binding.Signer.Download(t.Context(), blob.GetRequest{Authority: binding.Authority, Digest: dataDigest()})
			if state == "readable" {
				if err != nil || transfer.Digest != dataDigest() || transfer.Size != 14 || objects.signed.Key != "teams/team/cas/"+string(dataDigest()) || objects.signed.Method != http.MethodGet || reads != 1 {
					t.Fatalf("transfer=%+v signed=%+v reads=%d err=%v", transfer, objects.signed, reads, err)
				}
			} else if err == nil || transfer.URL != "" || objects.signed.Key != "" {
				t.Fatalf("unauthorized signing: transfer=%+v events=%v err=%v", transfer, *events, err)
			}
			if (state == "denied" || state == "scope changed") && reads != 0 {
				t.Fatal("unauthorized existence probe")
			}
		})
	}
}

func dataControllerFixture(a capability.Authorized) (*controller.DataController, *dataTransactions, *dataObjects, *dataEdges, *[]string) {
	events := []string{}
	f := &dataTransactions{events: &events}
	o := &dataObjects{events: &events}
	e := &dataEdges{events: &events}
	s := &controller.DataController{
		Objects: o, Access: e,
		ChargeDownload:   func(context.Context, blob.Authority, blob.Blob) error { return nil },
		ResolveAuthority: func(context.Context, capability.Authorized) (blob.Authority, error) { return dataAuthority(a), nil },
		ResolveTenant: func(_ context.Context, team store.Team) (controller.TenantHandle, error) {
			if team != "team" {
				panic("wrong data tenant")
			}
			return f, nil
		},
	}
	return s, f, o, e, &events
}

func dataDigest() blob.Digest {
	digest := sha256.Sum256([]byte("verified bytes"))
	return blob.Digest(hex.EncodeToString(digest[:]))
}

func TestDataUploadReservesBeforeSigningAndSettlesVerifiedBytes(t *testing.T) {
	a := dataAuthorized()
	s, f, objects, _, events := dataControllerFixture(a)
	binding, err := s.BindData(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := binding.Signer.Upload(t.Context(), blob.UploadIntent{Authority: binding.Authority, Identity: binding.Authority.Upload.Value, Digest: dataDigest(), Size: 14})
	if err != nil || ticket.ID == "" || ticket.ID != f.ticket.ID || ticket.Transfer.Digest != dataDigest() {
		t.Fatalf("ticket=%+v err=%v", ticket, err)
	}
	if !reflect.DeepEqual(*events, []string{"authorize", "reserve", "sign"}) || objects.signed.Key != "teams/team/pending/"+ticket.ID || objects.signed.Method != http.MethodPut || objects.signed.Size != 14 || objects.signed.SHA256 != string(dataDigest()) {
		t.Fatalf("events=%v signed=%+v", *events, objects.signed)
	}
	*events = nil
	receipt, err := binding.Signer.Commit(t.Context(), blob.CommitUpload{Authority: binding.Authority, TicketID: ticket.ID})
	if err != nil || receipt.Identity != binding.Authority.Upload.Value || receipt.Digest != dataDigest() {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if !reflect.DeepEqual(*events, []string{"ticket", "authorize", "verify", "settle", "delete"}) {
		t.Fatalf("events=%v", *events)
	}
	if !ticket.Transfer.ExpiresAt.Equal(f.ticket.ExpiresAt) || ticket.Transfer.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expiry=%v", ticket.Transfer.ExpiresAt)
	}
}

func dataAuthority(a capability.Authorized) blob.Authority {
	identity := runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Generation: 4, Attempt: 2}
	authority := blob.Authority{
		CapabilityRef: a.Principal.Authority.ID, Principal: blob.Principal(a.Principal.Capability.Subject.ID),
		Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: identity}, Repo: "gh:12",
		Ref: blob.RefFacts{Scope: fixtureMainRef, DefaultBranch: "main", SHA: "sealed-sha"},
	}
	authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: blob.UploadIdentity{
		Kind: blob.UploadJob, Team: "team", RunID: "run", Principal: authority.Principal, CapabilityRef: authority.CapabilityRef, Attempt: authority.Attempt,
	}}
	return authority
}

func TestDataAuthorityUsesPersistedUploader(t *testing.T) {
	a := dataAuthorized()
	want := dataAuthority(a)
	calls := 0
	s := &controller.DataController{ResolveAuthority: func(_ context.Context, got capability.Authorized) (blob.Authority, error) {
		calls++
		if got.Principal.Authority != a.Principal.Authority || got.Binding != a.Binding {
			t.Fatal("resolver lost authenticated authority or fence")
		}
		return want, nil
	}}
	got, err := s.DataAuthority(t.Context(), a)
	if err != nil || got != want || calls != 1 {
		t.Fatalf("authority=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestDataAuthorityRefusesSubstitutedIdentityAndScope(t *testing.T) {
	for _, state := range []string{"missing authority reference", "principal", "repo", "team", "run", "job", "attempt", "generation", "unit on job", "unknown kind", "write ref", "upload principal", "upload capability", "binding", "remote uploader"} {
		t.Run(state, func(t *testing.T) {
			a := dataAuthorized()
			value := dataAuthority(a)
			switch state {
			case "missing authority reference":
				value.CapabilityRef = ""
			case "principal":
				value.Principal = ""
			case "repo":
				value.Repo = "gh:13"
			case "team":
				value.Upload.Value.Team = "other"
			case "run":
				value.Upload.Value.RunID = "other"
			case "job":
				value.Upload.Value.Attempt.Value.NodeID = "sibling"
			case "attempt":
				value.Attempt.Value.Attempt++
			case "generation":
				value.Attempt.Value.Generation++
			case "unit on job":
				value.Upload.Value.Unit.Set = true
			case "unknown kind":
				value.Upload.Value.Kind = "run"
			case "write ref":
				value.Ref.Scope = blob.RefScope{Ref: "main", Verified: true}
			case "upload principal":
				value.Upload.Value.Principal = "other"
			case "upload capability":
				value.Upload.Value.CapabilityRef = "other"
			case "binding":
				a.Binding.Generation++
			case "remote uploader":
				a.Principal.Capability.Data.Use = capability.DataRemoteCache
			}
			s := &controller.DataController{ResolveAuthority: func(context.Context, capability.Authorized) (blob.Authority, error) { return value, nil }}
			_, err := s.DataAuthority(t.Context(), a)
			assertPolicyCode(t, err, apierr.Forbidden)
		})
	}
}

func TestDataUploadAndCommitFailuresProduceNoReceipt(t *testing.T) {
	for _, state := range []string{"reserve", "sign", "abort", "verify", "settle", "delete", "ticket", "revoked before commit", "foreign caller"} {
		t.Run(state, func(t *testing.T) {
			a := dataAuthorized()
			s, f, objects, edges, events := dataControllerFixture(a)
			refusal := errors.New("fixture refusal")
			switch state {
			case "reserve":
				f.reserveErr = refusal
			case "sign":
				objects.signErr = refusal
			case "abort":
				objects.signErr, f.abortErr = refusal, errors.New("abort refusal")
			}
			binding, err := s.BindData(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			ticket, err := binding.Signer.Upload(t.Context(), blob.UploadIntent{Authority: binding.Authority, Identity: binding.Authority.Upload.Value, Digest: dataDigest(), Size: 14})
			if state == "reserve" || state == "sign" || state == "abort" {
				if err == nil || ticket.ID != "" || slices.Contains(*events, "settle") {
					t.Fatalf("ticket=%+v events=%v err=%v", ticket, *events, err)
				}
				if state != "reserve" && !slices.Contains(*events, "abort") {
					t.Fatal("signing failure retained quota reservation")
				}
				if state == "abort" && slices.Contains(*events, "delete") {
					t.Fatal("unsettled staging was deleted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			*events = nil
			request := blob.CommitUpload{Authority: binding.Authority, TicketID: ticket.ID}
			switch state {
			case "verify":
				objects.verifyErr = refusal
			case "settle":
				f.settleErr = refusal
			case "delete":
				objects.deleteErr = refusal
			case "ticket":
				f.lookupErr = refusal
			case "revoked before commit":
				edges.err = &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked"}
			case "foreign caller":
				request.Authority.CapabilityRef = "other"
			}
			receipt, err := binding.Signer.Commit(t.Context(), request)
			if err == nil || receipt.Digest != "" {
				t.Fatalf("receipt=%+v events=%v err=%v", receipt, *events, err)
			}
			if state != "delete" && slices.Contains(*events, "delete") {
				t.Fatal("staging was deleted before settlement")
			}
			if state == "verify" && slices.Contains(*events, "settle") {
				t.Fatal("unverified bytes reached settlement")
			}
		})
	}
}

func TestDataOwnerAndPublicationUploadsHaveNoSyntheticAttempt(t *testing.T) {
	for _, kind := range []string{"trigger", "plan", "publication", "imported publication"} {
		t.Run(kind, func(t *testing.T) {
			a := dataAuthorized()
			a.Principal.Capability.Data.NodeID, a.Principal.Capability.Data.Attempt = "", 0
			a.Binding.NodeID, a.Binding.Attempt = "", 0
			a.Binding.Kind = capability.BindingTrigger
			a.Principal.Capability.Unit, a.Principal.Capability.Purpose = capability.UnitTrigger, capability.PurposeTrigger
			if kind == "plan" {
				a.Principal.Capability.Unit, a.Principal.Capability.Purpose = capability.UnitPlan, capability.PurposePlan
				a.Binding.Kind = capability.BindingRun
			}
			if strings.Contains(kind, "publication") {
				a.Principal.Capability.Unit, a.Principal.Capability.Purpose, a.Principal.Capability.UnitID = "", "", ""
				a.Principal.Capability.Subject.Kind = capability.SubjectAccount
				a.Principal.Capability.Data.Use, a.Principal.Capability.Data.Generation = capability.DataPublish, 0
				a.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
				a.Access = capability.AccessRule{Credential: capability.CredentialPublisher, Grants: []capability.Grant{capability.DataWrite}, Binding: capability.BindData, Liveness: capability.LiveCredential}
			}
			a.Principal.Capability.Binding = a.Binding
			s, f, _, _, _ := dataControllerFixture(a)
			authority := dataAuthority(a)
			authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
			upload := blob.UploadIdentity{
				Kind: blob.UploadOwnerUnit, Team: "team", RunID: "run", Principal: authority.Principal, CapabilityRef: authority.CapabilityRef,
				Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadUnitKind(kind), ID: "claim", Generation: 4}},
			}
			if strings.Contains(kind, "publication") {
				upload.Kind, upload.Unit = blob.UploadPublication, runspec.Optional[blob.UploadUnit]{}
				if kind == "imported publication" {
					authority.Attempt = dataAuthority(dataAuthorized()).Attempt
					upload.Attempt = authority.Attempt
				}
			}
			authority.Upload.Value = upload
			s.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) { return authority, nil }
			binding, err := s.BindData(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			_, err = binding.Signer.Upload(t.Context(), blob.UploadIntent{Authority: binding.Authority, Identity: upload, Digest: dataDigest(), Size: 14})
			if err != nil || f.ticket.Identity != upload {
				t.Fatalf("identity=%+v err=%v", f.ticket.Identity, err)
			}
			if kind != "imported publication" && (f.ticket.Identity.Attempt.Set || binding.Authority.Attempt.Set) {
				t.Fatal("invented job attempt for preplan or publication bytes")
			}
			key := blob.RecordKey{Team: "team", Kind: blob.KindBinary, Scope: blob.Scope{Repo: "gh:12", Ref: authority.Ref.Scope}, Name: "binary"}
			if kind == "imported publication" {
				key.Kind, key.Scope.Job = blob.KindOutput, "job"
			}
			if _, err := binding.Records.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: binding.Authority, Source: upload, Key: key, Digest: dataDigest()}); err != nil {
				t.Fatal(err)
			}
			key.Kind = blob.KindMemo
			_, err = binding.Records.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: binding.Authority, Source: upload, Key: key, Digest: dataDigest()})
			assertPolicyCode(t, err, apierr.Forbidden)
		})
	}
}

func TestDataBindingRetainsOpaqueSessionAuthority(t *testing.T) {
	a := dataAuthorized()
	p, _ := policyFixture("local_run_publisher", "data", "controller", []capability.Grant{capability.DataWrite})
	p.Class, p.Authority = capability.CredentialSession, sessionReference(p)
	p.Capability.Data.RepositoryKey, p.Capability.Data.WriteRef = "gh:12", fixtureMainRef.CapabilityScope()
	a.Principal, a.Binding = p, p.Capability.Binding
	a.Access = capability.AccessRule{Credential: capability.CredentialPublisher, Grants: []capability.Grant{capability.DataWrite}, Binding: capability.BindData, Liveness: capability.LiveCredential}
	s, f, _, _, _ := dataControllerFixture(a)
	authority := dataAuthority(a)
	authority.CapabilityRef, authority.Principal = "session:credential", "account:account"
	authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
	authority.Upload.Value = blob.UploadIdentity{Kind: blob.UploadPublication, Team: "team", RunID: "run", CapabilityRef: authority.CapabilityRef, Principal: authority.Principal}
	s.ResolveAuthority = func(_ context.Context, got capability.Authorized) (blob.Authority, error) {
		if got.Principal.Authority.Kind != capability.AuthoritySession {
			t.Fatal("session became capability authority")
		}
		return authority, nil
	}
	binding, err := s.BindData(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Signer.Upload(t.Context(), blob.UploadIntent{Authority: binding.Authority, Identity: authority.Upload.Value, Digest: dataDigest(), Size: 14})
	if err != nil || f.ticket.Identity.CapabilityRef != "session:credential" || f.ticket.Identity.Principal != "account:account" {
		t.Fatalf("identity=%+v err=%v", f.ticket.Identity, err)
	}
}

func TestDataCommitLosesAuthorityAfterVerification(t *testing.T) {
	a := dataAuthorized()
	s, f, objects, _, events := dataControllerFixture(a)
	binding, err := s.BindData(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := binding.Signer.Upload(t.Context(), blob.UploadIntent{Authority: binding.Authority, Identity: binding.Authority.Upload.Value, Digest: dataDigest(), Size: 14})
	if err != nil {
		t.Fatal(err)
	}
	objects.onVerified = func() { f.settleErr = &apierr.Error{Code: apierr.LockHeld, Message: "generation changed"} }
	*events = nil
	receipt, err := binding.Signer.Commit(t.Context(), blob.CommitUpload{Authority: binding.Authority, TicketID: ticket.ID})
	assertPolicyCode(t, err, apierr.LockHeld)
	if receipt.Digest != "" || slices.Contains(*events, "delete") || !slices.Contains(*events, "settle") {
		t.Fatalf("receipt=%+v events=%v", receipt, *events)
	}
}

func (f *dataTransactions) Team() store.Team { return "team" }
