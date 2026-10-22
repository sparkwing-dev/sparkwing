package nodehost

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzNativeCleanupMasksAcrossWriteBoundaries(f *testing.F) {
	f.Add("split-secret", uint16(17), false)
	f.Add("\x00\xff\r\n", uint16(0), true)
	f.Fuzz(func(t *testing.T, value string, chunk uint16, encoded bool) {
		if len(value) > 128 {
			value = value[:128]
		}
		secret := "registered-" + hex.EncodeToString([]byte(value)) + "-secret"
		masks := secrets.NewMasker()
		masks.Register(secret)
		content := secret
		if encoded {
			content = base64.StdEncoding.EncodeToString([]byte(secret))
		}
		var messages []string
		logs := &planningLogs{known: true, masker: masks, sink: func(_ context.Context, record nodeproto.FrameLog) error {
			messages = append(messages, record.Message)
			return nil
		}}
		writer := logs.writer(t.Context(), LevelInfo)
		text := strings.Repeat("p", (1<<20)-len(content)/2) + content + "\r\n" + content
		width := 256 + int(chunk)
		for len(text) > 0 {
			part := text[:min(width, len(text))]
			if _, err := writer.Write([]byte(part)); err != nil {
				t.Fatal(err)
			}
			text = text[len(part):]
		}
		if err := writer.close(); err != nil {
			t.Fatal(err)
		}
		output := strings.Join(messages, "\n")
		if strings.Contains(output, content) || masks.ContainsSecret(output) || !strings.Contains(output, "***") {
			t.Fatal("native cleanup output exposed a registered secret or lost masking")
		}
	})
}
