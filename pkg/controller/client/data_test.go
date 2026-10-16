package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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

func TestDataClientAcceptsServerSelectedVerifiedDefaultRef(t *testing.T) {
	key := blob.RecordKey{Team: "team", Kind: blob.KindMemo, Name: "output", Scope: blob.Scope{Repo: "gh:42", Pipeline: "ci", Job: "build", Key: "hash"}}
	_, records := testDataClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("verified_default_only") != "true" {
			t.Fatal("lookup lost its verified-default selection")
		}
		return dataResponse(t, wire.DataRecordLookupResult{Found: true, Record: &wire.DataRecord{
			Kind: blob.KindMemo, Repo: "gh:42", Ref: "main", Pipeline: "ci", Job: "build", Key: "hash", Name: "output", Digest: "digest",
		}, Members: []wire.DataMember{}}), nil
	})
	result, err := records.LookupRecord(t.Context(), blob.RecordLookup{Key: key, VerifiedDefaultOnly: true})
	if err != nil || !result.Found || result.Record.Key.Scope.Ref != (blob.RefScope{Ref: "main", Verified: true}) || result.Record.Digest != "digest" {
		t.Fatalf("verified-default record: %+v %v", result, err)
	}
}

func FuzzDataClientDefaultRefNeverChangesRecordSelectors(f *testing.F) {
	for change := uint8(0); change < 7; change++ {
		f.Add("main", change, true)
	}
	f.Add("", uint8(0), true)
	f.Add("manual:main", uint8(0), true)
	f.Add("main", uint8(0), false)
	f.Add("manual:", uint8(0), false)
	f.Fuzz(func(t *testing.T, ref string, change uint8, fallback bool) {
		key := blob.RecordKey{Team: "team", Kind: blob.KindMemo, Name: "output", Scope: blob.Scope{Repo: "gh:42", Pipeline: "ci", Job: "build", Key: "hash"}}
		record := wire.DataRecord{Kind: blob.KindMemo, Repo: "gh:42", Ref: ref, Pipeline: "ci", Job: "build", Key: "hash", Name: "output", Digest: "digest"}
		switch change % 7 {
		case 1:
			record.Kind = blob.KindOutput
		case 2:
			record.Repo = "gh:99"
		case 3:
			record.Pipeline = "other"
		case 4:
			record.Job = "other"
		case 5:
			record.Key = "other"
		case 6:
			record.Name = "other"
		}
		_, records := testDataClient(t, func(*http.Request) (*http.Response, error) {
			return dataResponse(t, wire.DataRecordLookupResult{Found: true, Record: &record, Members: []wire.DataMember{}}), nil
		})
		result, err := records.LookupRecord(t.Context(), blob.RecordLookup{Key: key, VerifiedDefaultOnly: fallback})
		want := change%7 == 0 && (fallback && ref != "" && !strings.HasPrefix(ref, "manual:") || !fallback && ref == "manual:")
		if want {
			if err != nil || !result.Found {
				t.Fatalf("selected record was rejected: %+v %v", result, err)
			}
		} else if err == nil || result.Found {
			t.Fatalf("record selector substitution accepted: %+v %v", result, err)
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
					want += unavailableRetries
				}
				if calls != want {
					t.Fatalf("calls=%d want=%d", calls, want)
				}
			})
		})
	}
}

func TestDataClientBudgetsBoundTransport(t *testing.T) {
	for _, operation := range []string{"has-many", "record"} {
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
						body = []byte(`{"deadline":"` + req.URL.Query().Get("deadline") + `"}`)
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
				return dataResponse(t, map[string]any{"url": result.URL, "headers": result.Headers, "sha256": result.Digest, "size": result.Size, "expires": result.ExpiresAt}), nil
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
