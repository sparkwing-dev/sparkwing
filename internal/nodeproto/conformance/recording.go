package conformance

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type recordingScope interface {
	Helper()
	Fatal(...any)
	Cleanup(func())
	Failed() bool
}

var recordProtocol = flag.Int("record-protocol", 0, "capture conformance traffic for this protocol version")

// Exchange records one engine request and the SDK frames preceding its reply.
type Exchange struct {
	WireRequest json.RawMessage   `json:"wire_request"`
	Expected    nodewire.Reply    `json:"expected"`
	Request     nodewire.Request  `json:"request"`
	Frames      []json.RawMessage `json:"frames"`
}

// Recording retains traffic from an accepted conformance session.
type Recording struct {
	Protocol  int        `json:"protocol"`
	Language  string     `json:"language"`
	Exchanges []Exchange `json:"exchanges"`
}

// NewRecording writes a fixture at test completion when -record-protocol is set.
func NewRecording(t recordingScope, language, name string) *Recording {
	t.Helper()
	r := &Recording{Protocol: nodewire.Version, Language: language}
	if *recordProtocol == 0 {
		return r
	}
	if *recordProtocol != nodewire.Version {
		t.Fatal("record the SDK's announced protocol version")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "internal", "nodeproto", "conformance", "testdata")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("conformance testdata not found")
		}
		root = parent
	}
	path := filepath.Join(root, "internal", "nodeproto", "conformance", "testdata", "recordings", fmt.Sprintf("v%d", r.Protocol), language, name+".json")
	t.Cleanup(func() {
		if t.Failed() || len(r.Exchanges) == 0 {
			return
		}
		var hello nodewire.Reply
		frames := r.Exchanges[0].Frames
		if len(frames) == 0 || json.Unmarshal(frames[len(frames)-1], &hello) != nil || !hello.OK {
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	return r
}

// Request starts the next exchange.
func (r *Recording) Request(t recordingScope, request nodewire.Request) {
	t.Helper()
	if *recordProtocol != 0 {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		r.Exchanges = append(r.Exchanges, Exchange{Request: request, WireRequest: raw})
	}
}

// Frame copies SDK bytes before the transport reuses its read buffer.
func (r *Recording) Frame(t recordingScope, raw []byte) {
	t.Helper()
	if *recordProtocol != 0 {
		if len(r.Exchanges) == 0 {
			t.Fatal("frame before request")
		}
		i := len(r.Exchanges) - 1
		var reply nodewire.Reply
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID != "" {
			r.Exchanges[i].Expected = reply
		}
		r.Exchanges[i].Frames = append(r.Exchanges[i].Frames, append(json.RawMessage(nil), raw...))
	}
}
