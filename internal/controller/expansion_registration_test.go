package controller

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestExpansionTransportPreservesPrivateMaskRegistration(t *testing.T) {
	want := controllerwire.ExpansionSubmission{
		SourceGeneration: 7,
		Body:             json.RawMessage(`{"jobs":[]}`),
		Registration: runspec.SecretRegistration{
			Defaults: []runspec.SecretValue{{Raw: "first-secret,second-secret", List: true}},
			Values:   []string{"first-secret", "second-secret", "first-secret,second-secret"},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	got, err := decodeExpansion(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("private registration changed in transport: %+v", got)
	}
}
