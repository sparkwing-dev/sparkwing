package wingwire

import (
	"encoding/json"
	"testing"
)

func TestHTTPStatusCarriesControllerContract(t *testing.T) {
	ack := HelloAck{ProtocolMajor: ProtocolMajor, NativeProtocolMajor: ProtocolMajor}
	body, err := json.Marshal(&ack)
	if err != nil {
		t.Fatal(err)
	}
	var got HelloAck
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ProtocolMajor != ProtocolMajor || got.NativeProtocolMajor != ProtocolMajor {
		t.Fatalf("HTTP controller contract = %+v", got)
	}
}
