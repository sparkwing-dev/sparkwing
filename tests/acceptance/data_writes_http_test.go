package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestDataWriteHTTPUploadAndCommit(t *testing.T) {
	f := newDataHTTPFixture()
	h := f.handler()
	w := authorityHTTP(h, f.boundary, "POST", "/api/v1/data/upload?team=foreign", `{"digest":"`+string(dataDigest())+`","size":14}`)
	var ticket struct {
		UploadID string `json:"upload_id"`
		URL      string `json:"url"`
		SHA256   string `json:"sha256"`
		Size     int64  `json:"size"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &ticket) != nil || ticket.UploadID == "" || ticket.URL == "" || ticket.SHA256 != string(dataDigest()) || ticket.Size != 14 {
		t.Fatalf("upload HTTP %d %s", w.Code, w.Body.String())
	}
	if !slices.Equal(*f.events, []string{"authorize", "reserve", "sign"}) || f.objects.signed.Key != "teams/team/pending/"+ticket.UploadID || strings.Contains(w.Body.String(), f.boundary.raw) {
		t.Fatalf("events=%v object=%+v body=%s", *f.events, f.objects.signed, w.Body.String())
	}
	*f.events = nil
	w = authorityHTTP(h, f.boundary, "POST", "/api/v1/data/commit", `{"upload_id":"`+ticket.UploadID+`"}`)
	var receipt struct {
		Digest blob.Digest `json:"digest"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Digest != dataDigest() || !slices.Contains(*f.events, "settle") || !slices.Contains(*f.events, "verify") {
		t.Fatalf("commit HTTP %d %s events=%v", w.Code, w.Body.String(), *f.events)
	}
	for _, private := range []string{"CapabilityRef", "capability_ref", "Principal", "principal", "Identity", "identity"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("private receipt: %s", w.Body.String())
		}
	}
}

func TestDataWriteHTTPRecordEdgesAndLookup(t *testing.T) {
	f := newDataHTTPFixture()
	a := dataAuthorized()
	data, tx, objects, edges, events := dataControllerFixture(a)
	f.data, f.objects, f.edges, f.events = data, objects, edges, events
	manifest, member := strings.Repeat("c", 64), strings.Repeat("d", 64)
	body := `{"kind":"output","pipeline":"build","job":"job","name":"out","digest":"` + string(dataDigest()) + `","manifest_digest":"` + manifest + `","members":[{"digest":"` + member + `","path":"file","size":1,"mode":420}]}`
	h := f.handler()
	w := authorityHTTP(h, f.boundary, "POST", "/api/v1/data/records?team=foreign", body)
	if w.Code != http.StatusOK || tx.record.Authority != dataAuthority(a) || tx.record.Source != dataAuthority(a).Upload.Value || tx.record.Key.Team != "team" || tx.record.Key.Scope.Repo != "gh:12" || tx.record.Key.Scope.Ref != fixtureMainRef || len(tx.record.Members) != 1 || tx.record.Members[0].Team != "team" || tx.record.Members[0].ManifestDigest != blob.Digest(manifest) {
		t.Fatalf("record HTTP %d %s transaction=%+v", w.Code, w.Body.String(), tx.record)
	}
	if !reflect.DeepEqual(*events, []string{"authorize", "authorize", "authorize", "commit record"}) {
		t.Fatalf("events=%v", *events)
	}
	tx.lookupResult = blob.RecordLookupResult{Found: true, Record: blob.Record{Key: tx.record.Key, Digest: tx.record.Digest, Source: tx.record.Source, Uploader: "private-uploader"}, Members: tx.record.Members}
	w = authorityHTTP(h, f.boundary, "GET", "/api/v1/data/records?kind=output&ref=manual%3Aaccount%2Ffixture%3Amain&pipeline=build&job=job&name=out", "")
	var lookup struct {
		Found   bool `json:"found"`
		Members []struct {
			Digest blob.Digest `json:"digest"`
		} `json:"members"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &lookup) != nil || !lookup.Found || len(lookup.Members) != 1 || lookup.Members[0].Digest != blob.Digest(member) || tx.lookup.Key != tx.record.Key || strings.Contains(w.Body.String(), "private-uploader") {
		t.Fatalf("lookup HTTP %d %s transaction=%+v", w.Code, w.Body.String(), tx.lookup)
	}
}

func TestDataRecordHTTPRefScopeProperty(t *testing.T) {
	property := func(value [16]byte, verified bool) bool {
		ref := blob.RefScope{Ref: hex.EncodeToString(value[:]) + "/release +%&?#", Verified: verified}
		a := dataAuthorized()
		a.Principal.Capability.Data.WriteRef = ref.CapabilityScope()
		a.Principal.Capability.Data.ReadRefs = []string{ref.CapabilityScope()}
		f := newDataHTTPFixture()
		f.boundary.snapshot.Authorities[0].Principal.Capability.Data = a.Principal.Capability.Data
		data, tx, objects, edges, events := dataControllerFixture(a)
		authority := dataAuthority(a)
		authority.Ref.Scope = ref
		data.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) { return authority, nil }
		f.data, f.objects, f.edges, f.events = data, objects, edges, events
		h := f.handler()
		w := authorityHTTP(h, f.boundary, "POST", "/api/v1/data/records", `{"kind":"output","job":"job","name":"out","digest":"`+string(dataDigest())+`"}`)
		var written struct{ Ref string }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &written) != nil || written.Ref != ref.CapabilityScope() || tx.record.Key.Scope.Ref != ref {
			t.Logf("write ref=%+v: HTTP %d %s key=%+v", ref, w.Code, w.Body, tx.record.Key)
			return false
		}
		tx.lookupResult = blob.RecordLookupResult{Found: true, Record: blob.Record{Key: tx.record.Key, Digest: dataDigest()}}
		query := url.Values{"kind": {"output"}, "job": {"job"}, "name": {"out"}, "ref": {written.Ref}}
		w = authorityHTTP(h, f.boundary, "GET", "/api/v1/data/records?"+query.Encode(), "")
		var read struct{ Record struct{ Ref string } }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &read) != nil || read.Record.Ref != written.Ref || tx.lookup.Key != tx.record.Key {
			t.Logf("lookup ref=%+v: HTTP %d %s key=%+v", ref, w.Code, w.Body, tx.lookup.Key)
			return false
		}
		*events = nil
		ref.Verified = !ref.Verified
		query.Set("ref", ref.CapabilityScope())
		w = authorityHTTP(h, f.boundary, "GET", "/api/v1/data/records?"+query.Encode(), "")
		if w.Code != http.StatusForbidden || len(*events) != 0 {
			t.Logf("trust substitution: HTTP %d %s events=%v", w.Code, w.Body, *events)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestDataWriteHTTPRejectsAuthorityAndMalformedInput(t *testing.T) {
	for _, operation := range []string{"upload", "commit", "records"} {
		for _, body := range []string{`null`, `[]`, `{}`, `{} {}`, `{"authority":{"team":"foreign"}}`, `{"source":{"principal":"foreign"}}`, `{"run_id":"foreign"}`, `{"provenance":"cloud"}`} {
			t.Run(operation+"/"+body, func(t *testing.T) {
				f := newDataHTTPFixture()
				w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, body)
				assertEngineError(t, w, 400, apierr.BadRequest)
				if len(*f.events) != 0 {
					t.Fatalf("invalid input reached storage: %v", *f.events)
				}
				f.boundary.raw = ""
				w = authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, body)
				assertEngineError(t, w, 401, apierr.Unauthenticated)
			})
		}
	}
}

func FuzzDataWriteHTTPForeignEdgeNeverCommits(f *testing.F) {
	f.Add([]byte("sibling job"), uint8(0))
	f.Add([]byte("foreign manifest"), uint8(1))
	f.Add([]byte("foreign member"), uint8(2))
	f.Fuzz(func(t *testing.T, value []byte, edge uint8) {
		sum := sha256.Sum256(value)
		digest := blob.Digest(hex.EncodeToString(sum[:]))
		fixture := newDataHTTPFixture()
		fixture.edges.denyDigest = digest
		digests := []blob.Digest{dataDigest(), blob.Digest(strings.Repeat("c", 64)), blob.Digest(strings.Repeat("d", 64))}
		digests[int(edge)%3] = digest
		body, err := json.Marshal(map[string]any{"kind": "output", "job": "job", "name": "out", "digest": digests[0], "manifest_digest": digests[1], "members": []map[string]any{{"digest": digests[2], "path": "file", "size": 1}}})
		if err != nil {
			t.Fatal(err)
		}
		w := authorityHTTP(fixture.handler(), fixture.boundary, "POST", "/api/v1/data/records", string(body))
		assertEngineError(t, w, 403, apierr.Forbidden)
		if slices.Contains(*fixture.events, "commit record") {
			t.Fatal("unreadable edge reached committing transaction")
		}
	})
}

func TestDataWriteHTTPAuthenticationAndFailures(t *testing.T) {
	for _, state := range []string{"missing", "swcg", "revoked", "parent revoked", "finished", "stale generation", "lease expired", "cancelled", "machine root", "no grant", "scope refused", "resolver revoked", "no controller", "no backend", "quota", "sign", "integrity", "settlement revoked"} {
		t.Run(state, func(t *testing.T) {
			f := newDataHTTPFixture()
			var tx *dataTransactions
			resolve := f.data.ResolveTenant
			f.data.ResolveTenant = func(ctx context.Context, team store.Team) (controller.TenantHandle, error) {
				result, err := resolve(ctx, team)
				tx = result.(*dataTransactions)
				if state == "quota" {
					tx.reserveErr = store.ErrStorageQuota
				}
				return result, err
			}
			status, code := 401, apierr.Unauthenticated
			p := &f.boundary.snapshot.Authorities[0].Principal
			switch state {
			case "missing":
				f.boundary.raw = ""
			case "swcg":
				f.boundary.raw = "swcg_removed"
			case "revoked":
				p.Capability.RevokedAt = &f.boundary.snapshot.Now
			case "parent revoked":
				f.boundary.snapshot.Authorities[1].Principal.Capability.RevokedAt = &f.boundary.snapshot.Now
			case "finished":
				f.boundary.snapshot.Binding.FinishedAt = &f.boundary.snapshot.Now
			case "stale generation":
				f.boundary.snapshot.Binding.Current.Generation++
			case "lease expired":
				f.boundary.snapshot.Binding.LeaseExpiresAt = f.boundary.snapshot.Now
			case "cancelled":
				f.boundary.snapshot.Binding.RunCancelled = true
				status, code = 403, apierr.Forbidden
			case "machine root":
				p.Capability.Unit, p.Capability.Issuance, p.Capability.Parent = "", capability.Delegate, nil
				p.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
				f.boundary.snapshot.Authorities = f.boundary.snapshot.Authorities[:1]
				status, code = 403, apierr.Forbidden
			case "no grant":
				p.Capability.Grants = []capability.Grant{capability.DataRead}
				status, code = 403, apierr.Forbidden
			case "scope refused":
				f.edges.err = blob.ErrDigestNotReadable
				status, code = 403, apierr.Forbidden
			case "resolver revoked":
				f.data.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) {
					return blob.Authority{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked"}
				}
			case "no controller":
				f.data = nil
				status, code = 500, apierr.PolicyMissing
			case "no backend":
				f.data.Objects = nil
				status, code = 500, apierr.PolicyMissing
			case "quota":
				status, code = 429, apierr.QuotaExceeded
			case "sign":
				f.objects.signErr = errors.New("private-sign-error")
				status, code = 500, apierr.Internal
			case "integrity", "settlement revoked":
			}
			h := f.handler()
			w := authorityHTTP(h, f.boundary, "POST", "/api/v1/data/upload", `{"digest":"`+string(dataDigest())+`","size":14}`)
			if state == "integrity" || state == "settlement revoked" {
				if w.Code != 200 || tx == nil {
					t.Fatalf("upload HTTP %d %s", w.Code, w.Body.String())
				}
				if state == "integrity" {
					f.objects.verifyErr = blob.ErrIntegrity
					status, code = 422, apierr.IntegrityFailure
				} else {
					f.objects.onVerified = func() { tx.settleErr = &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked"} }
				}
				w = authorityHTTP(h, f.boundary, "POST", "/api/v1/data/commit", `{"upload_id":"`+tx.ticket.ID+`"}`)
			}
			assertEngineError(t, w, status, code)
			if slices.Contains(*f.events, "commit record") || strings.Contains(w.Body.String(), "private-") || (state != "settlement revoked" && slices.Contains(*f.events, "settle")) {
				t.Fatalf("refusal leaked or committed: events=%v response=%s", *f.events, w.Body.String())
			}
			if state == "sign" && !slices.Contains(*f.events, "abort") {
				t.Fatal("failed signing retained the reservation")
			}
		})
	}
}

func TestDataWriteHTTPUploadRequiresExplicitNonnegativeSize(t *testing.T) {
	for _, size := range []string{"0", "-1", "null", "9223372036854775808"} {
		f := newDataHTTPFixture()
		w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/upload", `{"digest":"`+string(dataDigest())+`","size":`+size+`}`)
		if size == "0" {
			if w.Code != 200 || f.objects.signed.Size != 0 {
				t.Fatalf("empty upload HTTP %d %s", w.Code, w.Body.String())
			}
		} else {
			assertEngineError(t, w, 400, apierr.BadRequest)
			if len(*f.events) != 0 {
				t.Fatal("invalid size reserved or signed an object")
			}
		}
	}
}

func TestDataRecordHTTPQueryValidationAndMiss(t *testing.T) {
	for _, query := range []string{"", "kind=log&name=out", "kind=output&name=out&name=other", "kind=output&name=out&team=foreign", "kind=output&name=out&authority=foreign", "kind=output&name=out&deadline=bad", "kind=output&name=out&verified_default_only=bad", "kind=output&name=%xx"} {
		t.Run(query, func(t *testing.T) {
			f := newDataHTTPFixture()
			w := authorityHTTP(f.handler(), f.boundary, "GET", "/api/v1/data/records?"+query, "")
			assertEngineError(t, w, 400, apierr.BadRequest)
			if len(*f.events) != 0 {
				t.Fatalf("invalid query reached storage: %v", *f.events)
			}
		})
	}
	f := newDataHTTPFixture()
	w := authorityHTTP(f.handler(), f.boundary, "GET", "/api/v1/data/records?kind=output&name=absent&ref=manual%3Aaccount%2Ffixture%3Amain", "")
	var response struct {
		Found   bool  `json:"found"`
		Members []any `json:"members"`
		Record  any   `json:"record"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Found || response.Members == nil || response.Record != nil {
		t.Fatalf("miss HTTP %d %s", w.Code, w.Body.String())
	}
}

func FuzzDataRecordHTTPMalformedQueryNeverReachesStorage(f *testing.F) {
	f.Add("private-selector", uint8(0))
	f.Add("private-selector", uint8(1))
	f.Add("private-selector", uint8(2))
	f.Fuzz(func(t *testing.T, value string, invalid uint8) {
		fixture := newDataHTTPFixture()
		badEscape := []string{"%xx", "%", "%a"}[invalid%3]
		query := "kind=output&name=out&key=" + url.QueryEscape(value) + badEscape
		w := authorityHTTP(fixture.handler(), fixture.boundary, "GET", "/api/v1/data/records?"+query, "")
		assertEngineError(t, w, http.StatusBadRequest, apierr.BadRequest)
		if len(*fixture.events) != 0 {
			t.Fatalf("malformed selector reached storage: %v", *fixture.events)
		}
	})
}

func TestDataRecordHTTPRemoteCacheCannotBroadenReadOrWrite(t *testing.T) {
	f := newDataHTTPFixture()
	f.boundary = authorityMember()
	p := &f.boundary.snapshot.Authorities[0].Principal
	p.Capability.Grants = []capability.Grant{capability.DataRead}
	p.Capability.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:12", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
	a := dataAuthorized()
	a.Principal, a.Binding = *p, p.Capability.Binding
	a.Access = capability.AccessRule{Credential: capability.CredentialRepoReader, Grants: []capability.Grant{capability.DataRead}, Binding: capability.BindData, Liveness: capability.LiveCredential}
	data, tx, objects, edges, events := dataControllerFixture(a)
	authority := dataAuthority(a)
	authority.Attempt, authority.Upload = runspec.Optional[runspec.AttemptIdentity]{}, runspec.Optional[blob.UploadIdentity]{}
	authority.Ref.Scope.Ref = "main"
	data.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) { return authority, nil }
	f.data, f.objects, f.edges, f.events = data, objects, edges, events
	h := f.handler()
	w := authorityHTTP(h, f.boundary, "GET", "/api/v1/data/records?kind=depcache&name=go&ref=main&verified_default_only=false", "")
	if w.Code != 200 || !tx.lookup.VerifiedDefaultOnly || tx.lookup.Key.Team != "team" || tx.lookup.Key.Scope.Repo != "gh:12" || tx.lookup.Authority != authority {
		t.Fatalf("lookup HTTP %d %s transaction=%+v", w.Code, w.Body.String(), tx.lookup)
	}
	*events = nil
	for _, operation := range []string{"upload", "commit", "records"} {
		w = authorityHTTP(h, f.boundary, "POST", "/api/v1/data/"+operation, `{}`)
		assertEngineError(t, w, 403, apierr.Forbidden)
	}
	w = authorityHTTP(h, f.boundary, "GET", "/api/v1/data/records?kind=binary&name=engine&ref=main", "")
	assertEngineError(t, w, 403, apierr.Forbidden)
	if len(*events) != 0 {
		t.Fatalf("remote-cache write or binary read reached storage: %v", *events)
	}
}
