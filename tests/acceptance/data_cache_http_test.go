package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type cacheHTTPTransactions struct {
	*dataTransactions
	memoHit blob.MemoHit
	depHit  blob.DepcacheHit
}

func (f *cacheHTTPTransactions) LookupMemo(ctx context.Context, r blob.MemoLookup) (blob.MemoHit, error) {
	_, err := f.dataTransactions.LookupMemo(ctx, r)
	return f.memoHit, err
}

func (f *cacheHTTPTransactions) CopyMemo(ctx context.Context, r blob.CopyMemoRequest) (blob.MemoHit, error) {
	_, err := f.dataTransactions.CopyMemo(ctx, r)
	return f.memoHit, err
}

func (f *cacheHTTPTransactions) LookupDepcache(ctx context.Context, r blob.DepcacheLookup) (blob.DepcacheHit, error) {
	_, err := f.dataTransactions.LookupDepcache(ctx, r)
	return f.depHit, err
}

func newCacheHTTPFixture(t testing.TB) (*dataHTTPFixture, *cacheHTTPTransactions) {
	t.Helper()
	f := newDataHTTPFixture()
	tenant, err := f.data.ResolveTenant(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	tx := &cacheHTTPTransactions{dataTransactions: tenant.(*dataTransactions)}
	f.data.ResolveTenant = func(_ context.Context, team store.Team) (controller.DataTransactions, error) {
		if team != "team" {
			t.Fatalf("foreign tenant %s", team)
		}
		return tx, nil
	}
	return f, tx
}

func cacheBody(operation string) string {
	if operation == "depcache/lookup" {
		return `{"name":"go","key":"sum","deadline":"2100-01-01T00:00:00Z"}`
	}
	return `{"pipeline":"build","job":"job","hash":"memo-hash","deadline":"2100-01-01T00:00:00Z"}`
}

func TestDataCacheHTTPUsesBoundScopeAndCopyDestination(t *testing.T) {
	for _, operation := range []string{"memo/lookup", "depcache/lookup", "memo/copy"} {
		t.Run(operation, func(t *testing.T) {
			f, tx := newCacheHTTPFixture(t)
			tx.memoHit = blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: dataDigest()}}
			tx.depHit = blob.DepcacheHit{Found: true, Record: blob.Record{Digest: dataDigest()}, Source: blob.RestoreDefaultBranch}
			w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation+"?team=foreign&ref=main", cacheBody(operation))
			var result struct{ Found bool }
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Found || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			want := dataAuthority(dataAuthorized())
			want.CapabilityRef = f.boundary.snapshot.Authorities[0].Principal.Authority.ID
			want.Principal = "runner"
			want.Upload.Value.CapabilityRef, want.Upload.Value.Principal = want.CapabilityRef, want.Principal
			var authority blob.Authority
			var repo runspec.RepoKey
			var ref blob.RefScope
			var deadline time.Time
			switch operation {
			case "memo/lookup":
				authority, repo, ref, deadline = tx.memo.Authority, tx.memo.Key.Repo, tx.memo.Key.Ref, tx.memo.Budget.Deadline
				if tx.memo.Key.Pipeline != "build" || tx.memo.Key.Job != "job" || tx.memo.Key.Hash != "memo-hash" {
					t.Fatalf("memo=%+v", tx.memo)
				}
			case "depcache/lookup":
				authority, repo, ref, deadline = tx.depcache.Authority, tx.depcache.Key.Repo, tx.depcache.Key.Ref, tx.depcache.Budget.Deadline
				if tx.depcache.Key.Name != "go" || tx.depcache.Key.Key != "sum" {
					t.Fatalf("depcache=%+v", tx.depcache)
				}
			case "memo/copy":
				authority, repo, ref, deadline = tx.copy.Lookup.Authority, tx.copy.Lookup.Key.Repo, tx.copy.Lookup.Key.Ref, tx.copy.Lookup.Budget.Deadline
				if tx.copy.Destination != want.Attempt.Value {
					t.Fatalf("destination=%+v", tx.copy.Destination)
				}
			}
			if authority != want || repo != want.Repo || ref != want.Ref.Scope || deadline.Year() != 2100 {
				t.Fatalf("authority=%+v repo=%s ref=%+v deadline=%v", authority, repo, ref, deadline)
			}
		})
	}
}

func TestDataCacheHTTPPrivateFactsNeverLeaveResponse(t *testing.T) {
	property := func(value string, operationIndex uint8) bool {
		f, tx := newCacheHTTPFixture(t)
		secret := "private:" + value
		tx.memoHit = blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: dataDigest()}, ReadSet: blob.ReadSet{Reader: runspec.AttemptIdentity{Team: secret, RunID: secret, NodeID: secret}}}
		tx.depHit = blob.DepcacheHit{Found: true, Source: blob.RestoreDefaultBranch, Record: blob.Record{Digest: dataDigest(), Uploader: blob.Principal(secret), Source: blob.UploadIdentity{CapabilityRef: secret, Principal: blob.Principal(secret), Team: secret, RunID: secret}, RefFacts: blob.RefFacts{SHA: secret, AppDeliveryKey: secret}}, ReadSet: tx.memoHit.ReadSet}
		operations := []string{"memo/lookup", "memo/copy", "depcache/lookup"}
		operation := operations[int(operationIndex)%len(operations)]
		w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, cacheBody(operation))
		var result map[string]json.RawMessage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result) != 3 {
			return false
		}
		if operation == "depcache/lookup" {
			var record map[string]json.RawMessage
			if json.Unmarshal(result["record"], &record) != nil || len(record) != 10 {
				return false
			}
			allowed := []string{"kind", "repo", "ref", "pipeline", "job", "key", "name", "digest", "provenance", "created_at"}
			for key := range record {
				if !slices.Contains(allowed, key) {
					return false
				}
			}
			return string(result["found"]) == "true" && string(result["source"]) == `"default_branch"` && string(record["digest"]) == `"`+string(dataDigest())+`"`
		}
		return string(result["found"]) == "true" && string(result["output"]) == `"`+string(dataDigest())+`"` && string(result["members"]) == "[]"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"memo/lookup", "memo/copy", "depcache/lookup"} {
		f, tx := newCacheHTTPFixture(t)
		tx.memoHit.Output = runspec.Optional[blob.Digest]{Set: true, Value: "private-stale-output"}
		tx.depHit.Record.Uploader = "private-uploader"
		w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, cacheBody(operation))
		if w.Code != 200 || strings.Contains(w.Body.String(), "private") || !strings.Contains(w.Body.String(), `"found":false`) {
			t.Fatalf("miss HTTP %d %s", w.Code, w.Body.String())
		}
	}
}

func TestDataCacheHTTPRepoReaderKeepsTrustedFallbackAndCannotCopy(t *testing.T) {
	for _, session := range []bool{false, true} {
		f, tx := newCacheHTTPFixture(t)
		f.boundary = authorityMember()
		p := &f.boundary.snapshot.Authorities[0].Principal
		p.Capability.Grants = []capability.Grant{capability.DataRead}
		p.Capability.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:12", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
		if session {
			p.Class, p.Authority.Kind = capability.CredentialSession, capability.AuthoritySession
		}
		authority := blob.Authority{CapabilityRef: p.Authority.ID, Principal: blob.Principal(p.Capability.Subject.ID), Repo: "gh:12"}
		f.data.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) { return authority, nil }
		h := f.handler()
		for _, operation := range []string{"memo/lookup", "depcache/lookup"} {
			w := authorityHTTP(h, f.boundary, "POST", "/api/v1/data/"+operation, cacheBody(operation))
			if w.Code != 200 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
		}
		if tx.memo.Authority != authority || tx.depcache.Authority != authority || tx.memo.Key.Ref != (blob.RefScope{}) || tx.depcache.Key.Ref != (blob.RefScope{}) {
			t.Fatalf("memo=%+v depcache=%+v", tx.memo, tx.depcache)
		}
		*f.events = nil
		w := authorityHTTP(h, f.boundary, "POST", "/api/v1/data/memo/copy", cacheBody("memo/copy"))
		assertEngineError(t, w, 403, apierr.Forbidden)
		if len(*f.events) != 0 {
			t.Fatal("read-only cache access reached copy")
		}
	}
}

func TestDataCacheHTTPRefusesDeadOrInsufficientAuthority(t *testing.T) {
	for _, operation := range []string{"memo/lookup", "depcache/lookup", "memo/copy"} {
		for _, state := range []string{"missing", "revoked", "cancelled", "no read", "no write", "edge", "resolver", "transaction", "missing controller"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				f, tx := newCacheHTTPFixture(t)
				p := &f.boundary.snapshot.Authorities[0].Principal
				status, code := 401, apierr.Unauthenticated
				switch state {
				case "missing":
					f.boundary.raw = ""
				case "revoked":
					p.Capability.RevokedAt = &f.boundary.snapshot.Now
				case "cancelled":
					f.boundary.snapshot.Binding.RunCancelled = true
					if operation != "memo/copy" {
						status, code = 403, apierr.Forbidden
					}
				case "no read", "no write":
					grant := capability.DataRead
					if state == "no write" {
						grant = capability.DataWrite
					}
					p.Capability.Grants = slices.DeleteFunc(p.Capability.Grants, func(g capability.Grant) bool { return g == grant })
					status, code = 403, apierr.Forbidden
					if state == "no write" && operation != "memo/copy" {
						status = 200
					}
				case "edge":
					f.edges.err = blob.ErrDigestNotReadable
					status, code = 403, apierr.Forbidden
				case "resolver":
					f.data.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) {
						return blob.Authority{}, errors.New("private origin failure")
					}
					status, code = 500, apierr.Internal
				case "transaction":
					tx.lookupErr, tx.settleErr = errors.New("private transaction failure"), errors.New("private transaction failure")
					status, code = 500, apierr.Internal
				case "missing controller":
					f.data = nil
					status, code = 500, apierr.PolicyMissing
				}
				w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, cacheBody(operation))
				if status == 200 {
					if w.Code != status {
						t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
					}
					return
				}
				assertEngineError(t, w, status, code)
				if strings.Contains(w.Body.String(), "private") || state != "transaction" && len(*f.events) > 1 {
					t.Fatalf("body=%s events=%v", w.Body.String(), *f.events)
				}
			})
		}
	}
}

func TestDataCacheHTTPRejectsCallerAuthority(t *testing.T) {
	property := func(value string, index uint8) bool {
		fields := []string{"authority", "repo", "ref", "verified", "default_branch", "destination", "run_id", "generation", "read_set", "provenance", "upload"}
		for _, operation := range []string{"memo/lookup", "depcache/lookup", "memo/copy"} {
			f, _ := newCacheHTTPFixture(t)
			var body map[string]any
			if err := json.Unmarshal([]byte(cacheBody(operation)), &body); err != nil {
				t.Fatal(err)
			}
			body[fields[int(index)%len(fields)]] = value
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, string(encoded))
			if w.Code != 400 || len(*f.events) != 0 {
				return false
			}
		}
		return true
	}
	for i := range uint8(11) {
		if !property("forged", i) {
			t.Fatalf("accepted field %d", i)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"memo/lookup", "depcache/lookup", "memo/copy"} {
		for _, body := range []string{"null", "{}", "[]", `{} {}`, `{"pipeline":"build","job":"job","hash":"h","hash":"other"}`, `{"pipeline":"build","job":"job","hash":"h","deadline":null}`} {
			f, _ := newCacheHTTPFixture(t)
			w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, body)
			assertEngineError(t, w, 400, apierr.BadRequest)
			if len(*f.events) != 0 {
				t.Fatal("invalid body reached records")
			}
		}
		for _, method := range []string{"GET", "HEAD", "PUT", "DELETE"} {
			f, _ := newCacheHTTPFixture(t)
			w := authorityHTTP(f.handler(), f.boundary, method, "/api/v1/data/"+operation, "")
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		}
	}
}

func TestDataCacheHTTPMemoCopyCannotBorrowReadGrant(t *testing.T) {
	f, _ := newCacheHTTPFixture(t)
	p := &f.boundary.snapshot.Authorities[0].Principal
	p.Capability.Grants = []capability.Grant{capability.DataWrite}
	w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/memo/copy", cacheBody("memo/copy"))
	assertEngineError(t, w, 403, apierr.Forbidden)
	if len(*f.events) != 0 {
		t.Fatalf("events=%v", *f.events)
	}
}
