package secrets

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func knownValues(values ...string) *ChildValues {
	v := &ChildValues{masker: NewMasker(), done: true}
	v.cond = sync.NewCond(&v.mu)
	for _, value := range values {
		v.masker.Register(value)
	}
	return v
}

func TestLineWriterMasksASecretAcrossALongLinesChunkBoundary(t *testing.T) {
	const secret = "chunk-boundary-secret-91d2"
	filler := strings.Repeat("x", maxMaskedLine)
	for name, writes := range map[string][]string{
		"split between writes": {filler + secret[:6], secret[6:] + " tail"},
		"whole inside the cut": {filler + secret + "yy", " tail"},
	} {
		t.Run(name, func(t *testing.T) {
			v := knownValues(secret)
			var out bytes.Buffer
			w := v.Writer(&out)
			for _, p := range writes {
				if _, err := w.Write([]byte(p)); err != nil {
					t.Fatal(err)
				}
			}
			v.Close()
			got := out.String()
			if strings.Contains(got, secret[:6]) || strings.Contains(got, secret[6:]) {
				t.Fatalf("a piece of the secret reached the destination: ...%q", got[max(0, len(got)-80):])
			}
			if !strings.HasSuffix(got, "*** tail") && !strings.HasSuffix(got, "***yy tail") {
				t.Fatalf("tail = %q, want the secret masked in place", got[max(0, len(got)-80):])
			}
			if strings.Count(got, "x") != maxMaskedLine {
				t.Fatalf("got %d filler bytes, want %d", strings.Count(got, "x"), maxMaskedLine)
			}
		})
	}
}

func TestChildValuesMaskRecordKeepsFieldsNamedLikeASecret(t *testing.T) {
	v := knownValues("msg")
	rec := v.MaskRecord(sparkwing.LogRecord{Level: "info", Msg: "a msg", Attrs: map[string]any{"msg": "msg"}})
	if rec.Level != "info" || rec.Msg != "a ***" || rec.Attrs["msg"] != "***" {
		t.Fatalf("MaskRecord = %+v", rec)
	}
}
