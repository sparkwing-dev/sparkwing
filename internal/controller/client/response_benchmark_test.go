package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func BenchmarkSnapshotResponse(b *testing.B) {
	snapshot := runstate.RunSnapshot{Nodes: make([]runstate.NodeRow, 4)}
	data, err := json.Marshal(snapshot)
	if err != nil {
		b.Fatal(err)
	}
	client := &engineClient{}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var result runstate.RunSnapshot
		response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}
		if _, err := client.readResponse(response, &result); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSnapshotResponseRefusesAmbiguousNestedFields(t *testing.T) {
	snapshot := runstate.RunSnapshot{Nodes: make([]runstate.NodeRow, 1)}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ name, from, to string }{
		{"valid", "", ""},
		{"duplicate", `"StateSeq":0`, `"StateSeq":0,"StateSeq":0`},
		{"escaped duplicate", `"StateSeq":0`, `"StateSeq":0,"StateSe\u0071":0`},
		{"case", `"StateSeq":0`, `"stateseq":0`},
		{"null", `"StateSeq":0`, `"StateSeq":null`},
		{"missing", `"StateSeq":0,`, ``},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			body := string(data)
			if mutation.name != "valid" {
				body = strings.Replace(body, mutation.from, mutation.to, 1)
				if body == string(data) {
					t.Fatal("mutation did not change fixture")
				}
			}
			var result runstate.RunSnapshot
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
			_, err := (&engineClient{}).readResponse(response, &result)
			if (err == nil) != (mutation.name == "valid") {
				t.Fatalf("response validation: %v", err)
			}
		})
	}
}
