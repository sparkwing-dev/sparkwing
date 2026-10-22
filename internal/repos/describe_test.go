//go:build unix

package repos

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func fakeSDK(t *testing.T, body string) (bin, requests string) {
	t.Helper()
	dir := t.TempDir()
	bin, requests = filepath.Join(dir, "pipeline"), filepath.Join(dir, "requests")
	script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --sw-node-protocol ] || exit 9\n" +
		"read -r line <&3\n" +
		"printf '%s\\n' '{\"reply\":\"1\",\"ok\":true,\"result\":{\"protocol\":2,\"sdk\":{\"language\":\"go\",\"version\":\"fixture\"},\"features\":[]}}' >&4\n" +
		"read -r line <&3\nprintf '%s\\n' \"$line\" > " + requests + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, requests
}

func TestDescribeReadsTheReplyPastLogRecords(t *testing.T) {
	bin, requests := fakeSDK(t, `printf '{"ts":"2026-10-12T00:00:00Z","msg":"planning"}\n' >&4
printf '{"reply":"2","ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"env","type":"string","required":true}]}]}}\n' >&4`)
	doc, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Declarations().Pipelines) != 1 || doc.Declarations().Pipelines[0].Name != "demo" || len(doc.Declarations().Pipelines[0].Args) != 1 || !doc.Declarations().Pipelines[0].Args[0].Required {
		t.Fatalf("pipelines = %+v", doc.Declarations().Pipelines)
	}
	raw, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	req, err := nodeproto.ParseRequest([]byte(strings.TrimSpace(string(raw))))
	if err != nil || req.Op != nodeproto.OpDescribe {
		t.Fatalf("request = %+v, %v", req, err)
	}
}

func TestDescribeRefusesAnotherProtocolVersion(t *testing.T) {
	bin, _ := fakeSDK(t, `exit 0`)
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), `"protocol":2`, `"protocol":1`, 1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "details withheld") {
		t.Fatalf("err = %v, want a protocol refusal", err)
	}
}

func TestDescribeReturnsAFailedReply(t *testing.T) {
	bin, _ := fakeSDK(t, `printf '{"reply":"2","ok":false,"error":{"code":"plan_failed","message":"boom"}}\n' >&4`)
	_, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	var perr *apierr.Error
	if !errors.As(err, &perr) || perr.Code != apierr.PlanFailed {
		t.Fatalf("err = %v, want the reply's plan_failed error", err)
	}
}

func TestDescribeReportsAnExitWithoutAReply(t *testing.T) {
	bin, _ := fakeSDK(t, `echo "registry init failed" >&2; exit 3`)
	_, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "details withheld") || strings.Contains(err.Error(), "registry init failed") {
		t.Fatalf("err = %v, want withheld unclassified stderr", err)
	}
}

func TestDescribeFiltersCredentialsAndMasksDiagnostics(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "describe-aws-secret")
	t.Setenv("GH_TOKEN", "describe-gh-secret")
	t.Setenv("CUSTOM_API_TOKEN", "describe-custom-secret")
	t.Setenv("HTTPS_PROXY", "https://alice:proxy-private-password@proxy.example")
	bin, _ := fakeSDK(t, `printf '%s|%s|%s|%s\n' "$AWS_SECRET_ACCESS_KEY" "$GH_TOKEN" "$CUSTOM_API_TOKEN" "$HTTPS_PROXY" >&2; exit 3`)
	_, err := Describe(t.Context(), pipelineentry.Binary(bin), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "details withheld") {
		t.Fatalf("describe did not filter and mask pipeline output: %v", err)
	}
	for _, value := range []string{"describe-aws-secret", "describe-gh-secret", "describe-custom-secret", "proxy-private-password", "alice"} {
		if strings.Contains(err.Error(), value) {
			t.Errorf("credential reached diagnostics: %s", value)
		}
	}
}

func TestDescribeMasksProtocolErrors(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "https://alice:proxy-private-password@proxy.example")
	for _, message := range []string{"proxy-private-password", "a safe message"} {
		bin, _ := fakeSDK(t, `printf '{"reply":"2","ok":false,"error":{"code":"plan_failed","message":"`+message+`","hint":"proxy-private-password","details":{"secret":"proxy-private-password"}}}\n' >&4`)
		_, err := Describe(t.Context(), pipelineentry.Binary(bin), t.TempDir())
		var typed *apierr.Error
		if err == nil || strings.Contains(err.Error(), "proxy-private-password") || !errors.As(err, &typed) || typed.Code != apierr.PlanFailed {
			t.Fatalf("protocol error was not masked with its type retained: %v", err)
		}
		var output bytes.Buffer
		if err := apierr.WriteJSON(&output, typed); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "proxy-private-password") {
			t.Fatalf("typed error exposed credential: %s", output.String())
		}
	}
}

func FuzzDescribeErrorsNeverExposeProxyCredentials(f *testing.F) {
	f.Add([]byte("private-password"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) == 0 || len(input) > 64 {
			t.Skip()
		}
		password := hex.EncodeToString(input)
		t.Setenv("HTTPS_PROXY", "https://alice:"+password+"@proxy.example")
		failure := &apierr.Error{Code: apierr.PlanFailed, Message: password, Hint: password, Details: map[string]any{password: []any{password}}}
		body, err := json.Marshal(map[string]any{"reply": "2", "ok": false, "error": failure})
		if err != nil {
			t.Fatal(err)
		}
		bin, _ := fakeSDK(t, "printf '%s\\n' '"+string(body)+"' >&4")
		_, err = Describe(t.Context(), pipelineentry.Binary(bin), t.TempDir())
		var typed *apierr.Error
		if err == nil || !errors.As(err, &typed) {
			t.Fatalf("missing typed failure: %v", err)
		}
		var output bytes.Buffer
		if err := apierr.WriteJSON(&output, typed); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), password) || strings.Contains(err.Error(), password) {
			t.Fatal("process error exposed a registered credential")
		}
	})
}

func TestDescribeFailsAProtocolViolationAtOnce(t *testing.T) {
	for name, body := range map[string]string{
		"unknown reply id": `printf '{"reply":"3","ok":true,"result":{}}\n' >&4; exec sleep 300`,
		"garbage frame":    `printf 'not json\n' >&4; exec sleep 300`,
		// safety: the child stays blocked writing the rest of a line the host
		// stopped reading, which is the wait that must not happen.
		"oversized frame": `head -c 20000000 /dev/zero | tr '\0' a >&4; exec sleep 300`,
	} {
		t.Run(name, func(t *testing.T) {
			bin, _ := fakeSDK(t, body)
			_, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
			var perr *apierr.Error
			if !errors.As(err, &perr) || perr.Code != apierr.ProtocolError {
				t.Fatalf("err = %v, want protocol_error", err)
			}
		})
	}
}

func TestDescribeStopsAtTheDeadline(t *testing.T) {
	bin, _ := fakeSDK(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Describe(ctx, pipelineentry.Binary(bin), t.TempDir()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
}

func TestResolverDescribesOverTheProtocol(t *testing.T) {
	bin, _ := fakeSDK(t, `printf '{"reply":"2","ok":true,"result":{"pipelines":[{"name":"build","args":[]},{"name":"deploy","args":[]}]}}\n' >&4`)
	schemas, err := describePipelines(pipelineentry.Binary(bin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pipelineNames(schemas.Presentation().Pipelines), ","); got != "build,deploy" {
		t.Fatalf("names = %s", got)
	}
	bad, _ := fakeSDK(t, `exit 1`)
	if _, err := describePipelines(pipelineentry.Binary(bad), t.TempDir()); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("err = %v, want it to name the binary", err)
	}
}

func TestDescribeWithholdsUnknownDiscoveryDiagnostics(t *testing.T) {
	bin, _ := fakeSDK(t, `printf '{"reply":"2","ok":true,"result":{"pipelines":[{"name":"failed","args":[],"error":{"code":"plan_failed","message":"unknown-private-default","hint":"unknown-private-default","details":{"value":"unknown-private-default"}}},{"name":"healthy","args":[],"help":"unknown-private-default"}]}}\n' >&4`)
	doc, err := Describe(t.Context(), pipelineentry.Binary(bin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc.Presentation())
	if strings.Contains(string(raw), "unknown-private-default") || !strings.Contains(string(raw), "details withheld") {
		t.Fatalf("unsafe discovery projection: %s", raw)
	}
}

func TestDescribePresentationMasksDefaultsAndAuthorText(t *testing.T) {
	bin, _ := fakeSDK(t, `printf '{"reply":"2","ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"token","type":"string","required":false,"secret":true,"default":"private-default","desc":"private-default","enum":["private-default"]}],"help":"private-default","examples":[{"command":"private-default","comment":"private-default"}]}]}}\n' >&4`)
	doc, err := Describe(t.Context(), pipelineentry.Binary(bin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc.Presentation())
	if strings.Contains(string(raw), "private-default") || doc.Presentation().Pipelines[0].Args[0].Default == nil || *doc.Presentation().Pipelines[0].Args[0].Default != "***" {
		t.Fatalf("unsafe discovery projection: %s", raw)
	}
}
