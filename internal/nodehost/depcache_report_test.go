package nodehost

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type cacheLine struct{ level, message string }

func recordCacheLines(lines *[]cacheLine) CacheReporter {
	return func(level, message string) { *lines = append(*lines, cacheLine{level, message}) }
}

func TestDepcacheReportsOneOutcomePerCache(t *testing.T) {
	key := `[0-9a-f]{12}`
	for _, tc := range []struct {
		name    string
		before  func(t *testing.T, input NodeInput, adapter DepcacheRecords, backend *depcacheBackendFixture, job nodewire.JobSpec)
		during  func(t *testing.T, workspace string)
		outcome runstate.NodeState
		level   string
		want    string
	}{
		{name: "miss_saved", during: writeVendor, outcome: runstate.NodeSuccess, level: "info", want: `^cache dependencies: miss \(key ` + key + `\); saved \d+ B$`},
		{name: "hit", before: seedDepcache, outcome: runstate.NodeSuccess, level: "info", want: `^cache dependencies: hit \(key ` + key + `\); restored$`},
		{name: "present", before: func(t *testing.T, input NodeInput, _ DepcacheRecords, _ *depcacheBackendFixture, _ nodewire.JobSpec) {
			writePublishedFile(t, input.Entry.Directory, "vendor/old", "warm", 0o600)
		}, outcome: runstate.NodeSuccess, level: "info", want: `^cache dependencies: present on this host \(key ` + key + `\); not restored or saved$`},
		{name: "empty", outcome: runstate.NodeSuccess, level: "info", want: `^cache dependencies: miss \(key ` + key + `\); nothing to save, the cache directory is empty after the job$`},
		{name: "job_failed", during: writeVendor, outcome: runstate.NodeFailed, level: "info", want: `^cache dependencies: miss \(key ` + key + `\); not saved because the job did not succeed$`},
		{name: "no_key_file", before: func(t *testing.T, input NodeInput, _ DepcacheRecords, _ *depcacheBackendFixture, _ nodewire.JobSpec) {
			if err := os.Remove(filepath.Join(input.Entry.Directory, "lockfile")); err != nil {
				t.Fatal(err)
			}
		}, outcome: runstate.NodeSuccess, level: "warn", want: `^cache dependencies: no key file \(lockfile\) in the workspace; running uncached$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, adapter, backend, job := depcacheFixture(t)
			if tc.before != nil {
				tc.before(t, input, adapter, backend, job)
			}
			var lines []cacheLine
			finish, err := adapter.Prepare(t.Context(), input, job, recordCacheLines(&lines))
			if err != nil || finish == nil {
				t.Fatalf("prepare=%v", err)
			}
			if tc.during != nil {
				tc.during(t, input.Entry.Directory)
			}
			result := runstate.NodeResult{Outcome: tc.outcome}
			if err := finish(t.Context(), input, &result); err != nil {
				t.Fatal(err)
			}
			if len(lines) != 1 || lines[0].level != tc.level || !regexp.MustCompile(tc.want).MatchString(lines[0].message) {
				t.Fatalf("cache lines = %+v, want one %s line matching %s", lines, tc.level, tc.want)
			}
		})
	}
}

func writeVendor(t *testing.T, workspace string) {
	writePublishedFile(t, workspace, "vendor/module/file", "downloaded dependency", 0o644)
}

func TestCacheReporterWritesJobLogAndAnnotation(t *testing.T) {
	var frames []nodewire.FrameLog
	var annotations []nodewire.Annotation
	h := &nodeExecutor{deps: ExecutorDependencies{
		Log: func(_ context.Context, _ NodeInput, frame nodewire.FrameLog) error {
			frames = append(frames, frame)
			return nil
		},
		Annotate: func(_ context.Context, _ NodeInput, body nodewire.AnnotationRequest) error {
			annotations = append(annotations, body.Annotation)
			return nil
		},
	}}
	h.cacheReporter(t.Context(), NodeInput{})("info", "cache npm: hit (key 0123456789ab); restored")
	if len(frames) != 1 || frames[0].Event != "cache" || frames[0].Level != "info" || frames[0].Message != "cache npm: hit (key 0123456789ab); restored" {
		t.Fatalf("job log frames = %+v", frames)
	}
	if len(annotations) != 1 || annotations[0].Level != "info" || annotations[0].Message != frames[0].Message {
		t.Fatalf("annotations = %+v", annotations)
	}
}
