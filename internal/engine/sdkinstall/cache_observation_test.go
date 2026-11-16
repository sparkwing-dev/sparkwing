package sdkinstall

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestSDKInstallReportsBinaryCacheOutcome(t *testing.T) {
	installNativeToolFixture(t)
	dir := t.TempDir()
	writeInput(t, dir, "go.mod", []byte("module fixture\ngo 1.26\nrequire "+engine.GoModule+" v0.20.0\n"))
	writeInput(t, dir, "main.go", []byte("package main\nfunc main(){}"))
	for _, want := range []string{"compile", "hit", "compile"} {
		request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engineid.GoSDK, ExecutableDirectory: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		var output, diagnostics bytes.Buffer
		if err := Run(t.Context(), bytes.NewReader(request), &output, &diagnostics); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Cache struct {
				Outcome      string `json:"outcome"`
				BytesFetched *int64 `json:"bytes_fetched"`
			} `json:"cache"`
		}
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Cache.Outcome != want || got.Cache.BytesFetched == nil || *got.Cache.BytesFetched != 0 {
			t.Fatalf("cache observation=%+v; want %s and zero pipeline-cache transfer bytes", got.Cache, want)
		}
		if want == "hit" {
			writeInput(t, dir, "main.go", []byte("package main\nfunc main(){println(2)}"))
		}
	}
}
