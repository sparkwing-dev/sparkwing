package nodehost

import (
	"bytes"
	"encoding/json"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"

	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

func TestSDKZeroValueServiceRequestsConformToHostDecoder(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   any
		target any
	}{
		{"mask", nodewire.MaskRequest{}, &nodewire.MaskRequest{}},
		{"annotation", nodewire.AnnotationRequest{}, &nodewire.AnnotationRequest{}},
		{"summary", nodewire.SummaryRequest{}, &nodewire.SummaryRequest{}},
		{"oidc", map[string]string{"audience": ""}, &processOIDCRequest{}},
		{"child", sdkserve.ChildRequest{}, &ChildRequest{}},
		{"detached child", sdkserve.ChildRequest{Pipeline: "scan", Detached: true}, &ChildRequest{}},
		{"cleanup", map[string]any{"argv": []string(nil), "description": ""}, &processCleanupRequest{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := sdkserve.EncodeServiceRequest(test.body)
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/", bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if err := decodeService(request, test.target); err != nil {
				t.Fatalf("SDK encoded %s: %v", raw, err)
			}
			if test.name == "detached child" && !test.target.(*ChildRequest).Detached {
				t.Fatal("host dropped detached child intent")
			}
			value := reflect.ValueOf(test.target).Elem()
			for i := range value.NumField() {
				field := value.Field(i)
				if (field.Kind() == reflect.Map || field.Kind() == reflect.Slice) && field.IsNil() {
					t.Fatalf("collection %s decoded as nil: %s", value.Type().Field(i).Name, raw)
				}
			}
		})
	}
}

func TestTypeScriptZeroValueServiceRequestsConformToHostDecoder(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("TypeScript service conformance requires node")
	}
	fixture, err := filepath.Abs("../../sdk/typescript/test/fixtures/service-requests.mjs")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := exec.CommandContext(t.Context(), node, fixture).Output()
	if err != nil {
		t.Fatal(err)
	}
	var requests []struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &requests); err != nil {
		t.Fatal(err)
	}
	targets := map[string]any{
		"/node/v1/masks":       &nodewire.MaskRequest{},
		"/node/v1/annotations": &nodewire.AnnotationRequest{},
		"/node/v1/summary":     &nodewire.SummaryRequest{},
		"/node/v1/oidc-token":  &processOIDCRequest{},
		"/node/v1/children":    &ChildRequest{},
		"/node/v1/cleanups":    &processCleanupRequest{},
	}
	if len(requests) != 11 {
		t.Fatalf("service requests = %d, want 11", len(requests))
	}
	for _, request := range requests {
		t.Run(request.Method+" "+request.Path, func(t *testing.T) {
			if request.Method != http.MethodPost {
				if len(request.Body) != 0 {
					t.Fatalf("bodyless service encoded %s", request.Body)
				}
				return
			}
			target, ok := targets[request.Path]
			if !ok {
				t.Fatalf("unrecognized service request %s", request.Path)
			}
			httpRequest, err := http.NewRequestWithContext(t.Context(), request.Method, request.Path, bytes.NewReader(request.Body))
			if err != nil {
				t.Fatal(err)
			}
			if err := decodeService(httpRequest, target); err != nil {
				t.Fatalf("TypeScript encoded %s: %v", request.Body, err)
			}
			delete(targets, request.Path)
		})
	}
	if len(targets) != 0 {
		t.Fatalf("untested service request types: %v", targets)
	}
}
