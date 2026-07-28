package wingwire

import "testing"

func TestServedMajorMeetsClientsInsideTheServedRange(t *testing.T) {
	tests := []struct {
		name   string
		client int
		want   int
	}{
		{"oldest served major is answered on its own terms", MinProtocolMajor, MinProtocolMajor},
		{"a client on this build's major is answered natively", ProtocolMajor, ProtocolMajor},
		{"a client above the range is answered natively so it takes over", ProtocolMajor + 1, ProtocolMajor},
		{"a client below the floor is answered natively so it can refuse", MinProtocolMajor - 1, ProtocolMajor},
		{"an absent major is answered natively", 0, ProtocolMajor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServedMajor(tt.client); got != tt.want {
				t.Errorf("ServedMajor(%d) = %d, want %d", tt.client, got, tt.want)
			}
		})
	}
}

func TestServedMajorNeverAnswersAboveThisBuild(t *testing.T) {
	for client := MinProtocolMajor - 2; client <= ProtocolMajor+2; client++ {
		if got := ServedMajor(client); got > ProtocolMajor {
			t.Errorf("ServedMajor(%d) = %d, above this build's %d", client, got, ProtocolMajor)
		}
	}
}

func TestHelloAckCarriesNativeMajorSeparatelyFromTheServedOne(t *testing.T) {
	ack := HelloAck{ProtocolMajor: MinProtocolMajor, NativeProtocolMajor: ProtocolMajor}
	line, err := Encode(&ack)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	msg, err := Decode(line)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, ok := msg.(*HelloAck)
	if !ok {
		t.Fatalf("decoded %T, want *HelloAck", msg)
	}
	if got.ProtocolMajor != ack.ProtocolMajor || got.NativeProtocolMajor != ack.NativeProtocolMajor {
		t.Errorf("round trip = served %d native %d, want served %d native %d",
			got.ProtocolMajor, got.NativeProtocolMajor, ack.ProtocolMajor, ack.NativeProtocolMajor)
	}
}
