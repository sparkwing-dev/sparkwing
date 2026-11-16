package nodehost

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func nativeCacheReportLog(t *testing.T, cache nodewire.CacheSpec, jobSecret string) string {
	t.Helper()
	cfg, _ := nativeConfigFixture(t)
	records := &nativeLogRecords{objects: map[string][]byte{}}
	cfg.Logs = func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		records.view.Subject = subject
		return records, nil
	}
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.Close() })
	input, _ := nativeArtifactInput(t)
	if _, err := services.Config.Executor.BindStorage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	masks := services.Config.Masks
	h := &nodeExecutor{deps: ExecutorDependencies{
		Masker: masks.masker, Masks: masks, PrepareCaches: services.Config.Executor.PrepareCaches,
		Log: func(ctx context.Context, in NodeInput, frame nodewire.FrameLog) error {
			return services.Config.Processes.Log(ctx, in.Accepted.RunID, in.NodeID, frame)
		},
	}}
	job := nodewire.JobSpec{ID: input.NodeID, Caches: []nodewire.CacheSpec{cache}}
	if _, err := h.layers(t.Context(), &input, job, func() (runstate.NodeResult, error) {
		if jobSecret != "" {
			scoped := masks.job(input.Accepted.RunID, input.NodeID)
			if err := scoped.registerValue(jobSecret); err != nil {
				t.Fatal(err)
			}
			masks.releaseJob(input.Accepted.RunID, input.NodeID)
		}
		return runstate.NodeResult{Outcome: runstate.NodeSuccess}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if masks.mask(jobSecret) == "***" && jobSecret != "" {
		t.Fatal("job secret reached the run-wide masker, so this test proves nothing about job scope")
	}
	<-services.Config.Store.(logHostStore).logs.finish(t.Context(), input.NodeID)
	var body bytes.Buffer
	if _, err := (logseg.CommittedReader{Records: records, Objects: records}).Read(t.Context(), runstate.Actor{}, blob.LogReadRequest{Subject: records.view.Subject}, &body); err != nil {
		t.Fatal(err)
	}
	log := body.String()
	for _, want := range []string{`"level":"warn"`, `"event":"cache"`, "demo", "package.json", "uncached"} {
		if !strings.Contains(log, want) {
			t.Fatalf("job log does not explain the disabled cache (missing %q): %s", want, log)
		}
	}
	return log
}

func TestNativeCacheReportMasksJobSecretsAfterTheAttempt(t *testing.T) {
	const secret = "cache-job-secret-92"
	log := nativeCacheReportLog(t, nodewire.CacheSpec{Name: "demo", Path: ".cache/" + secret, KeyFiles: []string{"package.json", secret + ".lock"}}, secret)
	if strings.Contains(log, secret) {
		t.Fatalf("a job-registered secret reached the cache line: %s", log)
	}
}

func TestNativeCacheReportShowsOnlyCheckoutRelativePaths(t *testing.T) {
	log := nativeCacheReportLog(t, nodewire.CacheSpec{Name: "demo", Resolver: nodewire.CacheResolverNpm, Path: "/outside-checkout/private-cache", KeyFiles: []string{"package.json"}}, "")
	if strings.Contains(log, "/outside-checkout/") {
		t.Fatalf("cache line showed a path outside the checkout: %s", log)
	}
}

func TestCacheKeyFilesShowOnlyCheckoutRelativeNames(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "checkout")
	got := checkoutKeyFiles(workspace, []string{"package.json", filepath.Join(workspace, "web", "package-lock.json"), filepath.Join(filepath.Dir(workspace), "elsewhere.lock"), "../escape.lock"})
	if want := "package.json, web/package-lock.json, a file outside the checkout, a file outside the checkout"; got != want {
		t.Fatalf("key files shown as %q, want %q", got, want)
	}
}
