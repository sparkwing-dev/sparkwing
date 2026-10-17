package blob_test

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type logDeletePage struct {
	XMLName   xml.Name          `xml:"ListBucketResult"`
	Truncated bool              `xml:"IsTruncated"`
	Next      string            `xml:"NextContinuationToken,omitempty"`
	Classes   []logDeleteClass  `xml:"CommonPrefixes"`
	Objects   []logDeleteObject `xml:"Contents"`
}

type logDeleteClass struct {
	Prefix string `xml:"Prefix"`
}
type logDeleteObject struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
}

func TestS3LogDeletionScope(t *testing.T) {
	property := func(seed uint32) bool {
		prefix := fmt.Sprintf("deployment-%d/nested root/%%2e%%2e/", seed)
		team, run := fmt.Sprintf("team-%d", seed), fmt.Sprintf("run-%d", seed)
		objects := map[string]int64{}
		for _, owner := range []string{team, team + "-other"} {
			for _, id := range []string{run, run + "-other"} {
				for _, class := range []string{"r30", "r90", "r365"} {
					for _, index := range []int64{0, 1} {
						key, err := blob.LogObjectKey(blob.LogSubject{Kind: blob.LogOwnerUnit, Team: owner, RunID: id, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}}, class, index)
						if err != nil {
							t.Log(err)
							return false
						}
						objects[prefix+key] = 7
					}
				}
			}
		}
		cas := prefix + "teams/" + team + "/cas/" + string(digest("private"))
		objects[cas] = 11
		var scope string
		backend := logS3Prefix(t, prefix, func(r *http.Request) (*http.Response, error) {
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}
			if r.Method == http.MethodDelete {
				key := strings.TrimPrefix(r.URL.Path, "/bucket/")
				if !strings.HasPrefix(key, prefix+"logs/") || !strings.Contains(key, "/teams/"+team+"/") || (scope != "" && !strings.Contains(key, "/runs/"+scope+"/")) {
					return nil, errors.New("foreign delete")
				}
				delete(objects, key)
				return response, nil
			}
			if r.Method != http.MethodGet || r.URL.Query().Get("list-type") != "2" {
				return nil, errors.New("unexpected request")
			}
			listedPrefix, delimiter, cursor := r.URL.Query().Get("prefix"), r.URL.Query().Get("delimiter"), r.URL.Query().Get("continuation-token")
			if !strings.HasPrefix(listedPrefix, prefix+"logs/") {
				return nil, errors.New("listing escaped configured prefix")
			}
			entries := map[string]int64{}
			for key, size := range objects {
				if !strings.HasPrefix(key, listedPrefix) {
					continue
				}
				if delimiter == "/" {
					class, _, ok := strings.Cut(strings.TrimPrefix(key, listedPrefix), "/")
					if ok {
						entries[listedPrefix+class+"/"] = 0
					}
				} else {
					entries[key] = size
				}
			}
			keys := []string{}
			for key := range entries {
				if key > cursor {
					keys = append(keys, key)
				}
			}
			slices.Sort(keys)
			page := logDeletePage{Truncated: len(keys) > 2}
			keys = keys[:min(2, len(keys))]
			for _, key := range keys {
				if delimiter == "/" {
					page.Classes = append(page.Classes, logDeleteClass{key})
				} else {
					page.Objects = append(page.Objects, logDeleteObject{key, entries[key]})
				}
			}
			if page.Truncated {
				page.Next = keys[len(keys)-1]
			}
			raw, err := xml.Marshal(page)
			if err != nil {
				return nil, err
			}
			response.Header.Set("Content-Type", "application/xml")
			response.Body = io.NopCloser(strings.NewReader(string(raw)))
			return response, nil
		})
		for pass, target := range []string{run, "", ""} {
			scope = target
			n, err := backend.DeleteLogs(t.Context(), team, target)
			want := int64(42)
			if pass == 2 {
				want = 0
			}
			if n != want || err != nil {
				t.Log(n, want, err)
				return false
			}
			if objects[cas] != 11 {
				return false
			}
			wantObjects := 19
			if pass > 0 {
				wantObjects = 13
			}
			if len(objects) != wantObjects {
				t.Log(len(objects))
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}

func TestS3LogDeletionRefusesInvalidScopeAndListing(t *testing.T) {
	for _, fixture := range []struct{ team, run, body string }{
		{"../acme", "", ""},
		{"acme", "../run", ""},
		{"", "run", ""},
		{"acme", "run", `<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`},
		{"acme", "run", `<ListBucketResult><CommonPrefixes><Prefix>foreign/r90/</Prefix></CommonPrefixes></ListBucketResult>`},
	} {
		t.Run(fixture.team+fixture.run+fixture.body, func(t *testing.T) {
			hits := 0
			backend := logS3(t, func(r *http.Request) (*http.Response, error) {
				hits++
				if r.Method != http.MethodGet {
					t.Fatal("invalid scope caused deletion")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(strings.NewReader(fixture.body)), Request: r}, nil
			})
			if n, err := backend.DeleteLogs(t.Context(), fixture.team, fixture.run); n != 0 || err == nil {
				t.Fatalf("invalid deletion = %d, %v", n, err)
			}
			if fixture.body == "" && hits != 0 {
				t.Fatalf("invalid selector reached S3: %d", hits)
			}
		})
	}
}

func TestS3LogDeletionStopsOnForeignObjectsAndServiceErrors(t *testing.T) {
	prefix := "logs/r90/teams/acme/runs/run/"
	for _, failure := range []string{"foreign", "listing", "delete", "cursor"} {
		t.Run(failure, func(t *testing.T) {
			deletes := 0
			backend := logS3(t, func(r *http.Request) (*http.Response, error) {
				response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/xml"}}, Request: r}
				raw := `<ListBucketResult><CommonPrefixes><Prefix>logs/r90/</Prefix></CommonPrefixes></ListBucketResult>`
				if r.Method == http.MethodDelete {
					deletes++
					if r.URL.Path != "/bucket/"+prefix+"segment" {
						t.Fatal("foreign deletion reached S3")
					}
					response.StatusCode = http.StatusInternalServerError
					raw = `<Error><Code>InternalError</Code></Error>`
				} else if r.URL.Query().Get("prefix") == prefix {
					page := logDeletePage{}
					switch failure {
					case "foreign":
						page.Objects = []logDeleteObject{{"logs/r90/teams/acme-other/runs/run/segment", 7}}
					case "listing":
						response.StatusCode, raw = http.StatusInternalServerError, `<Error><Code>InternalError</Code></Error>`
					case "delete":
						page.Objects = []logDeleteObject{{prefix + "segment", 7}}
					case "cursor":
						page.Truncated = true
					}
					if failure != "listing" {
						body, err := xml.Marshal(page)
						if err != nil {
							return nil, err
						}
						raw = string(body)
					}
				}
				response.Body = io.NopCloser(strings.NewReader(raw))
				return response, nil
			})
			if n, err := backend.DeleteLogs(t.Context(), "acme", "run"); n != 0 || err == nil {
				t.Fatalf("failed deletion = %d, %v", n, err)
			}
			want := 0
			if failure == "delete" {
				want = 1
			}
			if deletes != want {
				t.Fatalf("DELETE calls = %d, want %d", deletes, want)
			}
		})
	}
}
