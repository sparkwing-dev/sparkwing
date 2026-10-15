package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type dataTransport func(*http.Request) (*http.Response, error)

func (f dataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func dataResponse(t *testing.T, output any) *http.Response {
	t.Helper()
	body, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func testDataClient(t *testing.T, transport dataTransport) (blob.DataSigner, blob.RecordStore) {
	t.Helper()
	signer, records, err := NewDataClient(EngineOptions{URL: "http://controller/prefix/", HTTP: &http.Client{Transport: transport}, Bearer: func(context.Context) (string, error) { return "private-data-bearer", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return signer, records
}

func dataFixture() (blob.Authority, blob.UploadIdentity, wire.DataRecord, blob.Record, []wire.DataMember, []blob.Member, wire.DataReadSet, blob.ReadSet) {
	a := runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 2, Generation: 3}
	identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: "team", RunID: "run", Principal: "principal", CapabilityRef: "opaque-ref", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: a}}
	authority := blob.Authority{CapabilityRef: "opaque-ref", Principal: "principal", Repo: "gh:42", Attempt: identity.Attempt, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: identity}, Ref: blob.RefFacts{AppDeliveryKey: "server-only-evidence"}}
	key := blob.RecordKey{Team: "team", Kind: blob.KindManifest, Name: "artifact", Scope: blob.Scope{Repo: "gh:42", Ref: blob.RefScope{Ref: "refs/heads/main", Verified: true}, Pipeline: "pipeline", Job: "job", Key: "logical-key"}}
	wk := wire.DataRecordKey{Team: "team", Kind: blob.KindManifest, Name: "artifact", Scope: wire.DataScope{Repo: "gh:42", Ref: wire.DataRefScope{Ref: "refs/heads/main", Verified: true}, Pipeline: "pipeline", Job: "job", Key: "logical-key"}}
	wi := wire.DataUploadIdentity{Kind: blob.UploadJob, Team: "team", RunID: "run", Principal: "principal", CapabilityRef: "opaque-ref", Attempt: identity.Attempt}
	when := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	expires := runspec.Optional[time.Time]{Set: true, Value: when.Add(time.Hour)}
	manifest := runspec.Optional[blob.Digest]{Set: true, Value: "manifest"}
	base := runspec.Optional[string]{Set: true, Value: "refs/heads/main"}
	record := blob.Record{Key: key, Digest: "output", ManifestDigest: manifest, Uploader: "principal", Source: identity, Provenance: blob.ProvenanceCloud, CreatedAt: when, ExpiresAt: expires, Pinned: true, RefFacts: blob.RefFacts{Scope: key.Scope.Ref, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "delivery", SHA: "sha", PullRequestBase: base}}
	wr := wire.DataRecord{Key: wk, Digest: "output", ManifestDigest: manifest, Uploader: "principal", Source: wi, Provenance: blob.ProvenanceCloud, CreatedAt: when, ExpiresAt: expires, Pinned: true, RefFacts: wire.DataRefFacts{Scope: wk.Scope.Ref, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "delivery", SHA: "sha", PullRequestBase: base}}
	members := []blob.Member{{Team: "team", ManifestDigest: "manifest", Digest: "member", Path: "nested/file", Size: 4, Mode: 0o755}}
	wm := []wire.DataMember{{Team: "team", ManifestDigest: "manifest", Digest: "member", Path: "nested/file", Size: 4, Mode: 0o755}}
	readSet := blob.ReadSet{Reader: a, Edges: []blob.ReadEdge{{Kind: blob.EdgeDependency, Reader: a, Source: a, Record: key, Digest: "member"}}}
	ws := wire.DataReadSet{Reader: a, Edges: []wire.DataReadEdge{{Kind: blob.EdgeDependency, Reader: a, Source: a, Record: wk, Digest: "member"}}}
	return authority, identity, wr, record, wm, members, ws, readSet
}

func TestDataClientRoutesAndMetadata(t *testing.T) {
	authority, identity, record, wantRecord, members, wantMembers, readSet, wantReadSet := dataFixture()
	memoKey := blob.MemoKey{Repo: "gh:42", Ref: blob.RefScope{Ref: "refs/heads/feature"}, Pipeline: "pipeline", Job: "job", Hash: "hash"}
	memoHit := wire.DataMemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: "output"}, Manifest: record.ManifestDigest, Members: members, ReadSet: readSet}
	wantMemo := blob.MemoHit{Found: true, Output: memoHit.Output, Manifest: memoHit.Manifest, Members: wantMembers, ReadSet: wantReadSet}
	transfer := wire.DataSignedTransfer{URL: "https://objects/object?signature=one-object", Headers: map[string]string{"x-object": "signature"}, ExpiresAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), Digest: "output", Size: 4}
	for _, tt := range []struct {
		name, method, operation, lookup string
		response, want                  any
		call                            func(blob.DataSigner, blob.RecordStore) (any, error)
	}{
		{"upload", "POST", "upload", "", wire.DataUploadTicket{ID: "ticket", Transfer: transfer}, blob.UploadTicket{ID: "ticket", Transfer: blob.SignedTransfer(transfer)}, func(s blob.DataSigner, _ blob.RecordStore) (any, error) {
			return s.Upload(t.Context(), blob.UploadIntent{Authority: authority, Identity: identity, Digest: "output", Size: 4})
		}},
		{"commit", "POST", "commit", "", wire.DataReceipt{Identity: record.Source, Digest: "output", CreatedAt: record.CreatedAt}, blob.Receipt{Identity: identity, Digest: "output", CreatedAt: record.CreatedAt}, func(s blob.DataSigner, _ blob.RecordStore) (any, error) {
			return s.Commit(t.Context(), blob.CommitUpload{Authority: authority, TicketID: "ticket"})
		}},
		{"download", "POST", "download", "", transfer, blob.SignedTransfer(transfer), func(s blob.DataSigner, _ blob.RecordStore) (any, error) {
			return s.Download(t.Context(), blob.GetRequest{Authority: authority, Digest: "output"})
		}},
		{"has-many", "POST", "has-many", "", wire.DataPresence{Present: []blob.Digest{"output"}}, blob.Presence{Present: []blob.Digest{"output"}}, func(s blob.DataSigner, _ blob.RecordStore) (any, error) {
			return s.HasMany(t.Context(), blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{"output", "absent"}})
		}},
		{"record", "GET", "records", "record", wire.DataRecordLookupResult{Found: true, Record: record, Members: members, ReadSet: readSet}, blob.RecordLookupResult{Found: true, Record: wantRecord, Members: wantMembers, ReadSet: wantReadSet}, func(_ blob.DataSigner, r blob.RecordStore) (any, error) {
			return r.LookupRecord(t.Context(), blob.RecordLookup{Authority: authority, Key: wantRecord.Key, VerifiedDefaultOnly: true})
		}},
		{"memo", "GET", "records", "memo", memoHit, wantMemo, func(_ blob.DataSigner, r blob.RecordStore) (any, error) {
			return r.LookupMemo(t.Context(), blob.MemoLookup{Authority: authority, Key: memoKey})
		}},
		{"depcache", "GET", "records", "depcache", wire.DataDepcacheHit{Found: true, Record: record, Source: blob.RestoreDefaultBranch, ReadSet: readSet}, blob.DepcacheHit{Found: true, Record: wantRecord, Source: blob.RestoreDefaultBranch, ReadSet: wantReadSet}, func(_ blob.DataSigner, r blob.RecordStore) (any, error) {
			return r.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: authority, Key: blob.DepcacheKey{Repo: "gh:42", Ref: memoKey.Ref, Name: "node_modules", Key: "lockfile"}})
		}},
		{"commit-record", "POST", "records", "", record, wantRecord, func(_ blob.DataSigner, r blob.RecordStore) (any, error) {
			return r.CommitRecord(t.Context(), blob.CommitRecordRequest{Source: identity, Authority: authority, Key: wantRecord.Key, Digest: "output", ManifestDigest: record.ManifestDigest, Members: wantMembers, ExpiresAt: wantRecord.ExpiresAt})
		}},
		{"copy-memo", "POST", "records", "", memoHit, wantMemo, func(_ blob.DataSigner, r blob.RecordStore) (any, error) {
			return r.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: authority, Key: memoKey}, Destination: wantReadSet.Reader})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			s, r := testDataClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != tt.method || req.URL.Path != "/prefix/api/v1/data/"+tt.operation || req.Header.Get("Authorization") != "Bearer private-data-bearer" {
					t.Fatalf("incorrect route or authentication: %s %s", req.Method, req.URL.Path)
				}
				var body []byte
				if tt.lookup != "" {
					if req.URL.Query().Get("lookup") != tt.lookup || req.Body != nil && req.ContentLength != 0 {
						t.Fatal("lookup must select its GET query variant without a body")
					}
					body = []byte(req.URL.Query().Get("selector"))
					var selector wire.DataLookupRequest
					if err := json.Unmarshal(body, &selector); err != nil {
						t.Fatal(err)
					}
					if selector.VerifiedDefaultOnly != (tt.lookup == "record") || (selector.Record != nil) != (tt.lookup == "record") || (selector.Memo != nil) != (tt.lookup == "memo") || (selector.Depcache != nil) != (tt.lookup == "depcache") {
						t.Fatal("lookup selector changed")
					}
				} else {
					var err error
					body, err = io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					if req.Header.Get("Content-Type") != "application/json" {
						t.Fatal("missing JSON content type")
					}
				}
				assertDataRequestSafe(t, body)
				if tt.operation == "upload" && req.Header.Get("Idempotency-Key") == "" {
					t.Fatal("upload reservation lacks an idempotency key")
				}
				if tt.name == "commit-record" || tt.name == "copy-memo" {
					var write wire.DataRecordWriteRequest
					if err := json.Unmarshal(body, &write); err != nil {
						t.Fatal(err)
					}
					if (write.Commit != nil) != (tt.name == "commit-record") || (write.CopyMemo != nil) != (tt.name == "copy-memo") || write.Operation != map[string]string{"commit-record": "commit", "copy-memo": "copy_memo"}[tt.name] {
						t.Fatal("record write union changed")
					}
				}
				return dataResponse(t, tt.response), nil
			})
			got, err := tt.call(s, r)
			if err != nil || !reflect.DeepEqual(got, tt.want) || calls != 1 {
				t.Fatalf("transport changed metadata: got %#v, want %#v, error %v, calls %d", got, tt.want, err, calls)
			}
		})
	}
}

func assertDataRequestSafe(t *testing.T, body []byte) {
	t.Helper()
	for _, forbidden := range []string{"private-data-bearer", "server-only-evidence", `"read_set"`, `"ref_facts"`, `"app_delivery_key"`} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("request contains forbidden authority evidence %q", forbidden)
		}
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	var check func(any)
	check = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, nested := range value {
				if strings.ToLower(key) != key {
					t.Fatalf("wire key is not snake_case: %s", key)
				}
				check(nested)
			}
		case []any:
			for _, nested := range value {
				check(nested)
			}
		}
	}
	check(value)
}

func TestDataClientOwnerUploadIdentity(t *testing.T) {
	source := runspec.AttemptIdentity{Team: "t", RunID: "r", NodeID: "generator", Attempt: 4, Generation: 9}
	identity := blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "t", RunID: "r", Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadEvalUnit, ID: "eval", Generation: 7, Source: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: source}}}}
	roundTrip := blobIdentity(dataIdentity(identity))
	if !reflect.DeepEqual(roundTrip, identity) {
		t.Fatalf("owner identity changed: %#v", roundTrip)
	}
	assertDataRequestSafe(t, mustDataJSON(t, dataIdentity(identity)))
}

func TestDataClientRecordSelectorsAndWriteMetadata(t *testing.T) {
	authority, identity, record, wantRecord, _, members, _, _ := dataFixture()
	for _, verifiedDefault := range []bool{false, true} {
		_, records := testDataClient(t, func(req *http.Request) (*http.Response, error) {
			var selector wire.DataLookupRequest
			body := []byte(req.URL.Query().Get("selector"))
			if err := json.Unmarshal(body, &selector); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"verified_default_only":`) || selector.VerifiedDefaultOnly != verifiedDefault || selector.Record == nil || !reflect.DeepEqual(*selector.Record, record.Key) {
				t.Fatal("record selector or explicit restriction changed")
			}
			if selector.Authority.CapabilityRef != authority.CapabilityRef || selector.Authority.Principal != authority.Principal || selector.Authority.Repo != authority.Repo || selector.Authority.Attempt == nil || *selector.Authority.Attempt != authority.Attempt.Value || selector.Authority.Upload == nil || !reflect.DeepEqual(*selector.Authority.Upload, record.Source) {
				t.Fatal("authority comparisons changed")
			}
			return dataResponse(t, wire.DataRecordLookupResult{}), nil
		})
		if _, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: authority, Key: wantRecord.Key, VerifiedDefaultOnly: verifiedDefault}); err != nil {
			t.Fatal(err)
		}
	}
	_, records := testDataClient(t, func(req *http.Request) (*http.Response, error) {
		var input wire.DataRecordWriteRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		commit := input.Commit
		if commit == nil || !reflect.DeepEqual(commit.Source, record.Source) || !reflect.DeepEqual(commit.Key, record.Key) || commit.Digest != wantRecord.Digest || commit.ManifestDigest != wantRecord.ManifestDigest || commit.ExpiresAt != wantRecord.ExpiresAt || !reflect.DeepEqual(blobMembers(commit.Members), members) {
			t.Fatal("record write metadata changed")
		}
		return dataResponse(t, record), nil
	})
	if _, err := records.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: authority, Source: identity, Key: wantRecord.Key, Digest: wantRecord.Digest, ManifestDigest: wantRecord.ManifestDigest, Members: members, ExpiresAt: wantRecord.ExpiresAt}); err != nil {
		t.Fatal(err)
	}
}

func mustDataJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDataClientRetriesPreserveUploadReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var bodies, keys []string
		s, _ := testDataClient(t, func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, string(body))
			keys = append(keys, req.Header.Get("Idempotency-Key"))
			if len(keys) == 1 {
				return nil, io.EOF
			}
			return dataResponse(t, wire.DataUploadTicket{ID: "ticket", Transfer: wire.DataSignedTransfer{URL: "http://objects/upload", Digest: "digest", Size: 4, ExpiresAt: time.Now().Add(time.Minute)}}), nil
		})
		_, err := s.Upload(t.Context(), blob.UploadIntent{Digest: "digest", Size: 4})
		if err != nil || len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || bodies[0] != bodies[1] {
			t.Fatalf("upload replay changed: %v (%v)", err, keys)
		}
	})
}

func TestDataClientRetriesPreserveExactCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var first []byte
		calls := 0
		s, _ := testDataClient(t, func(req *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if calls == 1 {
				first = body
				return nil, io.EOF
			}
			if string(body) != string(first) || !strings.Contains(string(body), `"ticket_id":"immutable-ticket"`) {
				t.Fatal("commit replay changed")
			}
			return dataResponse(t, wire.DataReceipt{}), nil
		})
		if _, err := s.Commit(t.Context(), blob.CommitUpload{TicketID: "immutable-ticket"}); err != nil || calls != 2 {
			t.Fatalf("exact-body replay failed: %v calls=%d", err, calls)
		}
	})
}

func TestDataClientStructuredErrorsAndRetry(t *testing.T) {
	for _, code := range []apierr.Code{apierr.Unavailable, apierr.RateLimited, apierr.Forbidden, "future_data_error"} {
		t.Run(string(code), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				s, _ := testDataClient(t, func(*http.Request) (*http.Response, error) {
					calls++
					body := `{"error":{"code":"` + string(code) + `","message":"safe","hint":"retry","details":{"reason":"binding"},"future":true}}`
					status := apierr.Status(code)
					resp := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
					resp.Header.Set("Retry-After", "1")
					return resp, nil
				})
				_, err := s.Download(t.Context(), blob.GetRequest{Digest: "digest"})
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != code || failure.Hint != "retry" || failure.Details["reason"] != "binding" {
					t.Fatalf("structured error lost: %v", err)
				}
				want := 1
				if code == apierr.Unavailable || code == apierr.RateLimited {
					want += UnavailableRetries
				}
				if calls != want {
					t.Fatalf("calls=%d want=%d", calls, want)
				}
			})
		})
	}
}

func TestDataClientBudgetsBoundTransport(t *testing.T) {
	for _, operation := range []string{"has-many", "record", "memo", "depcache", "copy-memo"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				deadline := time.Now().Add(time.Second)
				s, records := testDataClient(t, func(req *http.Request) (*http.Response, error) {
					if got, ok := req.Context().Deadline(); !ok || !got.Equal(deadline) {
						t.Fatal("request lost explicit deadline")
					}
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					if req.Method == "GET" {
						body = []byte(req.URL.Query().Get("selector"))
					}
					if !strings.Contains(string(body), `"deadline":"`+deadline.Format(time.RFC3339Nano)+`"`) {
						t.Fatal("server lost explicit deadline")
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				})
				budget := blob.Budget{Deadline: deadline}
				var err error
				switch operation {
				case "has-many":
					_, err = s.HasMany(t.Context(), blob.HasManyRequest{Budget: budget})
				case "record":
					_, err = records.LookupRecord(t.Context(), blob.RecordLookup{Budget: budget})
				case "memo":
					_, err = records.LookupMemo(t.Context(), blob.MemoLookup{Budget: budget})
				case "depcache":
					_, err = records.LookupDepcache(t.Context(), blob.DepcacheLookup{Budget: budget})
				case "copy-memo":
					_, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: blob.MemoLookup{Budget: budget}})
				}
				if !errors.Is(err, blob.ErrBudget) {
					t.Fatalf("budget exhaustion = %v", err)
				}
			})
		})
	}
	s, _ := testDataClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("expired request reached transport")
		return nil, nil
	})
	if _, err := s.HasMany(t.Context(), blob.HasManyRequest{Budget: blob.Budget{Deadline: time.Now().Add(-time.Second)}}); !errors.Is(err, blob.ErrBudget) {
		t.Fatalf("expired budget = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.HasMany(ctx, blob.HasManyRequest{Budget: blob.Budget{Deadline: time.Now().Add(time.Second)}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation = %v", err)
	}
}

func TestDataClientRefusesUnsafeTransfers(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*wire.DataSignedTransfer)
	}{
		{"bearer", func(s *wire.DataSignedTransfer) {
			s.Headers = map[string]string{"aUtHoRiZaTiOn": "Bearer private-data-bearer"}
		}},
		{"cookie", func(s *wire.DataSignedTransfer) { s.Headers = map[string]string{"Cookie": "controller-session"} }},
		{"url-credentials", func(s *wire.DataSignedTransfer) { s.URL = "https://user:secret@objects/object" }},
		{"foreign-digest", func(s *wire.DataSignedTransfer) { s.Digest = "unrequested" }},
		{"negative-size", func(s *wire.DataSignedTransfer) { s.Size = -1 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := testDataClient(t, func(*http.Request) (*http.Response, error) {
				result := wire.DataSignedTransfer{URL: "https://objects/object?signature=value", Digest: "digest", Size: 4, ExpiresAt: time.Now().Add(time.Minute)}
				tt.edit(&result)
				return dataResponse(t, result), nil
			})
			got, err := s.Download(t.Context(), blob.GetRequest{Digest: "digest"})
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || got.URL != "" || strings.Contains(err.Error(), "private-data-bearer") {
				t.Fatalf("unsafe transfer escaped: %#v %v", got, err)
			}
		})
	}
}

func TestDataClientRefusesMalformedResponsesAndRedirects(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"present":[],"present":[]}`, `{"present":[]} {}`, `{"present":[],"PRESENT":[]}`, `{"present":false}`} {
		s, _ := testDataClient(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		if _, err := s.HasMany(t.Context(), blob.HasManyRequest{}); err == nil {
			t.Fatalf("accepted malformed response %s", body)
		}
	}
	calls := 0
	s, _ := testDataClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"http://foreign/steal"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := s.Download(t.Context(), blob.GetRequest{}); err == nil || calls != 1 {
		t.Fatalf("followed redirect: %v calls=%d", err, calls)
	}
}

func TestDataClientRejectsUnrequestedPresence(t *testing.T) {
	s, _ := testDataClient(t, func(*http.Request) (*http.Response, error) {
		return dataResponse(t, wire.DataPresence{Present: []blob.Digest{"unrequested"}}), nil
	})
	if _, err := s.HasMany(t.Context(), blob.HasManyRequest{Digests: []blob.Digest{"requested"}}); err == nil {
		t.Fatal("accepted an unrelated existence disclosure")
	}
}

func TestDataClientValidatesBaseAndBearerProvider(t *testing.T) {
	for _, base := range []string{"", "file:///tmp/controller", "http://user:secret@controller", "http://controller?token=secret", "http://controller#fragment"} {
		if s, r, err := NewDataClient(EngineOptions{URL: base}); err == nil || s != nil || r != nil {
			t.Fatal("accepted invalid controller base")
		}
	}
	failure := errors.New("credential provider failed")
	s, _, err := NewDataClient(EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: dataTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("failed credentials reached transport")
		return nil, nil
	})}, Bearer: func(context.Context) (string, error) { return "", failure }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(t.Context(), blob.GetRequest{}); !errors.Is(err, failure) {
		t.Fatalf("provider error lost: %v", err)
	}
}
