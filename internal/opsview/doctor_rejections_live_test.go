package opsview_test

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// rejectAdmission asks a live daemon to admit a request it must refuse as
// malformed, and reports the refusal so a caller cannot mistake a granted
// lease for a rejection.
func rejectAdmission(t *testing.T, home, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), strayTestWait)
	defer cancel()
	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{
		Home:        home,
		Version:     "v1.0.0",
		DialTimeout: 500 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
		Spawn:       wingdclient.NoHostSpawn,
	})
	if err != nil {
		t.Fatalf("connect to the daemon at %s: %v", home, err)
	}
	defer func() { _ = cl.Close() }()
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID:      runID,
		Pipeline:   "demo",
		CostSource: wingwire.CostSource("not-a-cost-source"),
	}, nil)
	if err == nil {
		_ = lease.Release()
		t.Fatal("the daemon admitted a request naming a cost source it cannot know")
	}
}

// awaitRejectionTally waits for the daemon to tally what it already refused.
// It sends the eviction before it records the event, so a sweep that runs
// between the two sees a short count.
func awaitRejectionTally(t *testing.T, home, cause string, want int) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	deadline := time.Now().Add(strayTestWait)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		qs, err := wingdclient.ProbeQueue(ctx, sock)
		cancel()
		if err == nil && qs.Events != nil {
			for _, r := range qs.Events.Rejections {
				if r.Cause == cause && r.Count >= want {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never tallied %d %q rejections (last probe error %v)", want, cause, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDiagnose_NamesARepeatedRejectionCauseFromALiveDaemon drives the wiring
// doctor's rejection-pattern check depends on: the daemon's own tally, its
// events window, and the cause label doctor prints. Hand-built reports pin the
// rendering but would stay green if any of that stopped working.
func TestDiagnose_NamesARepeatedRejectionCauseFromALiveDaemon(t *testing.T) {
	home := shortHome(t)
	p := paths.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	serveDaemon(t, home, "v1.0.0")

	const cause = "cost_source"
	const rejections = 3
	for i := range rejections {
		rejectAdmission(t, home, "run-invalid-"+strconv.Itoa(i))
	}
	awaitRejectionTally(t, home, cause, rejections)

	ctx, cancel := context.WithTimeout(context.Background(), strayTestWait)
	defer cancel()
	report, err := opsview.Diagnose(ctx, p, home, "v1.0.0", true)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.AdmissionRejections) == 0 {
		t.Fatalf("doctor reported no rejection pattern after %d refusals of one cause: %+v", rejections, report.Daemon)
	}
	// The daemon's cause key is what selects doctor's explanation, so a rename
	// on either side has to fail here rather than degrade to generic advice.
	i := slices.IndexFunc(report.AdmissionRejections, func(r opsview.DoctorRejection) bool {
		return r.Cause == cause
	})
	if i < 0 {
		t.Fatalf("doctor named %+v, none of them %q", report.AdmissionRejections, cause)
	}
	got := report.AdmissionRejections[i]
	if got.Count != rejections {
		t.Errorf("rejection count = %d, want %d", got.Count, rejections)
	}
	if report.Clean() {
		t.Error("a machine refusing every admission read as clean")
	}
	var pretty bytes.Buffer
	if err := opsview.RenderDoctor(&pretty, report, "pretty", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(pretty.String(), got.Cause) {
		t.Errorf("the pretty report does not name the cause %q:\n%s", got.Cause, pretty.String())
	}
	if !strings.Contains(pretty.String(), "cost source this box's daemon does not recognize") {
		t.Errorf("the pretty report gave generic advice instead of the cost-source explanation:\n%s", pretty.String())
	}
}
