package secrets

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestChildStreamPayloadsMaskBeforeLinesAndPreserveUTF8(t *testing.T) {
	masker := NewMasker()
	secret := "私密-first\nsecond-line"
	if err := masker.Register(secret); err != nil {
		t.Fatal(err)
	}
	input := "public\n" + secret + "\nfinish-λ"
	for width := 1; width <= len(input); width++ {
		logs := NewLogStreams(masker)
		var output []string
		data := input
		for len(data) > 0 {
			n := min(width, len(data))
			frames, err := logs.Push(nodeproto.FrameLog{Stream: "command-stdout", StreamData: base64.StdEncoding.EncodeToString([]byte(data[:n])), Step: "body", Event: "exec_line"})
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range frames {
				if frame.Stream != "" || frame.StreamData != "" || frame.StreamEnd || frame.Step != "body" {
					t.Fatal("transport escaped trusted envelope")
				}
				output = append(output, frame.Message)
			}
			data = data[n:]
		}
		frames, err := logs.Push(nodeproto.FrameLog{Stream: "command-stdout", StreamEnd: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range frames {
			output = append(output, frame.Message)
		}
		if got := strings.Join(output, "\n"); got != "public\n***\nfinish-λ" {
			t.Fatalf("width %d output %q", width, got)
		}
	}
}

func TestChildStreamRefusesMalformedTransportPayload(t *testing.T) {
	for _, frame := range []nodeproto.FrameLog{{StreamData: "abcd"}, {Stream: "child", StreamData: "!"}, {Stream: "child", Message: "plaintext"}, {Stream: "child", Attrs: []byte(`{}`)}} {
		if _, err := NewLogStreams(NewMasker()).Push(frame); err == nil {
			t.Fatal("accepted malformed stream payload")
		}
	}
}
