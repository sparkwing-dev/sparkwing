package secrets

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestPresentationAdaptersKeepTrustedEnvelopeNames(t *testing.T) {
	m := NewMasker()
	if err := m.Register("message"); err != nil {
		t.Fatal(err)
	}
	frame := m.Log(nodeproto.FrameLog{Message: "message", Attrs: json.RawMessage(`{"message":"message"}`)})
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"msg":"***"`) || !strings.Contains(string(encoded), `"attrs":{"***":"***"}`) {
		t.Fatalf("trusted log envelope changed: %s", encoded)
	}
	source := &apierr.Error{Code: apierr.Internal, Message: "message", Details: map[string]any{"message": "message"}}
	projected := m.Error(source)
	encoded, err = json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"message":"***"`) || !strings.Contains(string(encoded), `"details":{"***":"***"}`) {
		t.Fatalf("trusted error envelope changed: %s", encoded)
	}
}

func TestPresentationAdapterFallsBackOnAttributeCollision(t *testing.T) {
	m := NewMasker()
	if err := m.Register("private-token"); err != nil {
		t.Fatal(err)
	}
	frame := m.Log(nodeproto.FrameLog{Attrs: json.RawMessage(`{"private-token":1,"***":2}`)})
	if string(frame.Attrs) != maskedAttributes {
		t.Fatalf("Attrs = %s", frame.Attrs)
	}
}
