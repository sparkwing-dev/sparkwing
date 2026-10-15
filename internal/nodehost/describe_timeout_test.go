//go:build unix

package nodehost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const testHelloScript = `IFS= read -r line <&3
id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
printf '{"reply":"%s","ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"test"},"features":[]}}\n' "$id" >&4
`

// Every line describe's output held counts against the cap and in the notice,
// empty ones and those the cap dropped included.
func TestTheWithheldNoticeCountsEveryLine(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "describe-fails")
	script := "#!/bin/sh\n" + testHelloScript + "i=0; while [ $i -lt 140000 ]; do echo; i=$((i+1)); done\nexit 3\n"
	if err := os.WriteFile(entry, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	sink := &planSinkText{masker: secrets.NewMasker()}
	// hack: the stalled-describe deadline can interrupt the output loop before all lines reach the sink.
	_, err := PlanDocument(t.Context(), SessionConfig{
		Entry: pipelineentry.Binary(entry), Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}, Sink: sink, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	}, PlanRequest{Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails", Pipeline: "demo"}, Masker: secrets.NewMasker()})
	var perr *apierr.Error
	if !errors.As(err, &perr) || perr.Code != apierr.SDKExited {
		t.Fatalf("err = %v, want the process to exit after writing every line", err)
	}
	if got := sink.String(); !strings.Contains(got, "(140000 lines)") {
		t.Fatalf("plan output = %q, want a notice counting all 140000 lines", got[max(0, len(got)-200):])
	}
}
