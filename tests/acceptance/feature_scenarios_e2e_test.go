package acceptance_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func testFeaturePlacement(f *featureController) {
	f.poll(f.fast, "lane=fast")
	run := f.plan(runspec.Request{}, []nodeproto.JobSpec{{ID: "job", Prefers: []string{"lane=fast"}}})
	if f.claim(f.slow, run, "job", "lane=slow") != nil {
		f.t.Fatal("fallback bypassed a live preferred runner's hold")
	}
	preferred := f.mustClaim(f.fast, run, "job", "lane=fast")
	row, err := f.host(preferred).ReadNode(f.ctx, runspec.NodeRef{RunID: runspec.RunID(run), NodeID: "job"})
	if err != nil || row.QueueStartedAt == nil {
		f.t.Fatalf("ready timestamp: %+v %v", row.ReadyAt, err)
	}
	ready := *row.QueueStartedAt
	f.delay(time.Second)
	attempt := int64(0)
	if _, err := f.client(preferred.Capability).TransitionJob(f.ctx, run, "job", controllerwire.JobTransitionRequest{To: "pending", Cause: "release", SourceAttempt: &attempt}); err != nil {
		f.t.Fatalf("return unstarted award: %v", err)
	}
	if f.claim(f.slow, run, "job", "lane=slow") != nil {
		f.t.Fatal("returned award discarded the preference hold while the preferred runner still had a free slot")
	}
	var fallback *controllerwire.ClaimedUnit
	f.await("original placement deadline", func() bool { fallback = f.claim(f.slow, run, "job", "lane=slow"); return fallback != nil })
	claimed := time.Now()
	elapsed := claimed.Sub(ready)
	if elapsed < 3*time.Second || elapsed > 4*time.Second {
		f.t.Fatalf("fallback after %s, want original ready+3s (within 1s polling/scheduling allowance)", elapsed)
	}
	returned, err := f.host(*fallback).ReadNode(f.ctx, row.Ref)
	if err != nil || returned.QueueStartedAt == nil || !returned.QueueStartedAt.Equal(ready) {
		f.t.Fatalf("return renewed ready time: original=%s returned=%v error=%v", ready, returned.QueueStartedAt, err)
	}
	f.finish(*fallback, "success")
	f.awaitStatus(run, "success")
	f.t.Logf("run=%s fallback opened after %s; unstarted return preserved ready=%s", run, elapsed, ready.Format(time.RFC3339Nano))

	f.poll(f.fast, "lane=fast", "mid-hold")
	run = f.plan(runspec.Request{}, []nodeproto.JobSpec{{ID: "job", Prefers: []string{"lane=fast,lane=late"}, Requires: []string{"mid-hold"}}})
	f.delay(500 * time.Millisecond)
	joined := f.mustClaim(f.slow, run, "job", "lane=late", "mid-hold")
	joinedRow, err := f.host(joined).ReadNode(f.ctx, runspec.NodeRef{RunID: runspec.RunID(run), NodeID: "job"})
	if err != nil || joinedRow.QueueStartedAt == nil {
		f.t.Fatalf("joining runner queue time: %v", err)
	}
	joinedAt := time.Now()
	if elapsed := joinedAt.Sub(*joinedRow.QueueStartedAt); elapsed < 500*time.Millisecond || elapsed >= 3*time.Second {
		f.t.Fatalf("runner joined outside hold: %s", elapsed)
	}
	f.finish(joined, "success")
	detail := f.awaitStatus(run, "success")
	if len(detail.Attempts) != 1 || detail.Attempts[0].ExecutorName != "slow" {
		f.t.Fatalf("joining preferred runner did not execute: %+v", detail.Attempts)
	}
	f.t.Logf("run=%s runner joined during hold and completed the sole attempt", run)
}

func testFeatureRetry(f *featureController) {
	ids := []string{"a", "b", "c", "d"}
	var jobs []nodeproto.JobSpec
	for i, id := range ids {
		job := nodeproto.JobSpec{ID: id, Artifacts: []string{"payload.txt"}}
		if i > 0 {
			job.Needs = []string{ids[i-1]}
		}
		jobs = append(jobs, job)
	}
	last := nodeproto.JobSpec{ID: "failed", Needs: ids}
	for _, id := range ids {
		last.Consumes = append(last.Consumes, nodeproto.ArtifactInput{Producer: id, Into: id})
	}
	jobs = append(jobs, last)
	source := f.plan(runspec.Request{}, jobs)
	for _, id := range ids {
		award := f.mustClaim(f.slow, source, id)
		f.start(award)
		result := f.publish(award, id)
		f.report(award, result)
	}
	failed := f.mustClaim(f.slow, source, "failed")
	f.finish(failed, "failed")
	f.awaitStatus(source, "failed")
	parent := source
	for i := range 3 {
		retry := f.retryCLI(parent)
		award := f.mustClaim(f.slow, retry, "failed")
		f.start(award)
		f.verifyReused(award, ids)
		outcome := "failed"
		if i == 2 {
			outcome = "success"
		}
		f.report(award, &controllerwire.JobResult{Outcome: outcome})
		detail := f.awaitStatus(retry, outcome)
		if len(detail.Attempts) != 1 || detail.Attempts[0].JobID != "failed" {
			f.t.Fatalf("retry reran a successful producer: %+v", detail.Attempts)
		}
		f.t.Logf("retry=%s source=%s status=%s attempts=1 reused=%v", retry, parent, outcome, ids)
		parent = retry
	}
	retry := f.retryCLI(source)
	award := f.mustClaim(f.slow, retry, "failed")
	f.start(award)
	f.verifyReused(award, ids)
	second := f.client(f.second)
	cancelled := make(chan error, 1)
	go func() {
		_, err := second.Cancel(f.ctx, retry, controllerwire.CancelRequest{Reason: "actor two cancels a retry while its failed job runs"})
		cancelled <- err
	}()
	f.await("held runner sees second actor cancellation", func() bool {
		beat, err := f.client(award.Capability).Renew(f.ctx, award.UnitID, controllerwire.RenewalRequest{Executing: true})
		if err != nil {
			f.t.Fatalf("held heartbeat: %v", err)
		}
		return beat.CancelRequested
	})
	if err := <-cancelled; err != nil {
		f.t.Fatal(err)
	}
	f.report(award, &controllerwire.JobResult{Outcome: "cancelled"})
	detail := f.awaitStatus(retry, "cancelled")
	if len(detail.Attempts) != 1 || detail.Attempts[0].Outcome != "cancelled" {
		f.t.Fatalf("cancelled retry attempts: %+v", detail.Attempts)
	}
	f.t.Logf("retry=%s actor-two cancellation reached running attempt and settled cancelled", retry)
}

func testFeatureChildren(f *featureController) {
	for _, ending := range []string{"parent_cancelled", "parent_failed", "creating_job_ended"} {
		jobs := []nodeproto.JobSpec{{ID: "parent", Children: []nodeproto.ChildPipeline{{Pipeline: "child"}}}}
		if ending == "creating_job_ended" {
			jobs = append(jobs, nodeproto.JobSpec{ID: "keep-parent-running", Requires: []string{"keep-parent"}})
		}
		parent := f.plan(runspec.Request{}, jobs)
		parentAward := f.mustClaim(f.slow, parent, "parent")
		f.start(parentAward)
		host := f.host(parentAward)
		principal, err := host.Principal(f.ctx)
		if err != nil {
			f.t.Fatal(err)
		}
		process, err := host.ProcessClient(f.ctx, principal, "parent")
		if err != nil {
			f.t.Fatal(err)
		}
		child, err := process.StartChild(f.ctx, "child", nil, "", "", false)
		if err != nil {
			f.t.Fatalf("start child: %v", err)
		}
		f.accept(child, []nodeproto.JobSpec{{ID: "child"}})
		childAward := f.mustClaim(f.fast, child, "child")
		f.start(childAward)
		if detail := f.awaitStatus(child, "running"); len(detail.Attempts) != 1 {
			f.t.Fatalf("child never executed: %+v", detail)
		}
		switch ending {
		case "parent_cancelled":
			_, err = f.client(f.second).Cancel(f.ctx, parent, controllerwire.CancelRequest{Reason: "actor two cancels parent"})
			if err != nil {
				f.t.Fatal(err)
			}
			f.report(parentAward, &controllerwire.JobResult{Outcome: "cancelled"})
			f.awaitStatus(parent, "cancelled")
		case "parent_failed":
			f.report(parentAward, &controllerwire.JobResult{Outcome: "failed"})
			f.awaitStatus(parent, "failed")
		case "creating_job_ended":
			f.report(parentAward, &controllerwire.JobResult{Outcome: "success"})
			detail, err := f.control.ReadRun(f.ctx, parent)
			if err != nil || detail.Status != "running" {
				f.t.Fatalf("parent must remain live after creating job ends: %s %v", detail.Status, err)
			}
		}
		started := time.Now()
		f.await("orphaned child cancellation by recovery sweep", func() bool {
			beat, err := f.client(childAward.Capability).Renew(f.ctx, childAward.UnitID, controllerwire.RenewalRequest{Executing: true})
			if err != nil {
				f.t.Fatalf("child heartbeat: %v", err)
			}
			return beat.CancelRequested
		})
		observed := time.Now()
		if elapsed := observed.Sub(started); elapsed > 12*time.Second {
			f.t.Fatalf("child cancellation exceeded recovery interval: %s", elapsed)
		}
		f.report(childAward, &controllerwire.JobResult{Outcome: "cancelled"})
		f.awaitStatus(child, "cancelled")
		if ending == "creating_job_ended" {
			keep := f.mustClaim(f.slow, parent, "keep-parent-running", "keep-parent")
			f.finish(keep, "success")
			f.awaitStatus(parent, "success")
		}
		retry := f.retryCLI(child)
		retriedAward := f.mustClaim(f.fast, retry, "child")
		f.start(retriedAward)
		// safety: this crosses a second recovery sweep, which must exempt the manual retry's inherited parent lineage.
		f.delay(11 * time.Second)
		beat, err := f.client(retriedAward.Capability).Renew(f.ctx, retriedAward.UnitID, controllerwire.RenewalRequest{Executing: true})
		if err != nil || beat.CancelRequested {
			f.t.Fatalf("manual retry did not survive %s: cancelled=%t error=%v", ending, beat.CancelRequested, err)
		}
		f.report(retriedAward, f.publish(retriedAward, "survived"))
		f.awaitStatus(retry, "success")
		f.t.Logf("%s parent=%s child=%s cancelled after %s; manual retry=%s survived sweep and succeeded", ending, parent, child, observed.Sub(started), retry)
	}
}

func (f *featureController) poll(raw string, labels ...string) {
	f.t.Helper()
	slots := 2
	response, err := f.client(raw).Claim(f.ctx, controllerwire.ClaimRequest{MaxConcurrent: 2, FreeSlots: &slots, RunnerVersion: featureVersion, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: labels})
	if err != nil || response.Claim != nil {
		f.t.Fatalf("idle runner registration: awarded=%t error=%v", response.Claim != nil, err)
	}
}

func (f *featureController) mustClaim(raw, run, job string, labels ...string) controllerwire.ClaimedUnit {
	f.t.Helper()
	var award *controllerwire.ClaimedUnit
	f.await("job "+job+" award", func() bool { award = f.claim(raw, run, job, labels...); return award != nil })
	return *award
}

func (f *featureController) delay(duration time.Duration) {
	f.t.Helper()
	select {
	case <-f.ctx.Done():
		f.t.Fatal(f.ctx.Err())
	// sleepcheck:external-boundary placement holds and recovery sweeps run on the external controller's real clock
	case <-time.After(duration):
	}
}

func (f *featureController) retryCLI(source string) string {
	f.t.Helper()
	if !f.profileReady {
		profile := exec.CommandContext(f.ctx, f.cli, "profiles", "add", "--profile", "scenario", "--controller", f.url, "--token-stdin", "-o", "json")
		profile.Env = f.env
		profile.Stdin = strings.NewReader(f.member + "\n")
		if output, err := profile.CombinedOutput(); err != nil {
			f.t.Fatalf("configure isolated profile: %v %s", err, output)
		}
		f.profileReady = true
	}
	cmd := exec.CommandContext(f.ctx, f.cli, "runs", "retry", "--run", source, "--failed", "--profile", "scenario", "--agent", "feature-e2e", "--reason", "repeat deterministic feature scenario", "-o", "json")
	cmd.Env = f.env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("runs retry --failed: %v %s %s", err, output, stderr.String())
	}
	var result struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.RunID == "" {
		f.t.Fatalf("retry receipt: %s %v", output, err)
	}
	return result.RunID
}

func (f *featureController) publish(award controllerwire.ClaimedUnit, value string) *controllerwire.JobResult {
	f.t.Helper()
	host := f.host(award)
	principal, err := host.Principal(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	storage, err := host.BindStorage(f.ctx, principal)
	if err != nil {
		f.t.Fatalf("bind storage: %v", err)
	}
	identity := storage.Authority.Upload.Value
	body, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		f.t.Fatal(err)
	}
	output, err := (blob.OutputData{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: identity, Pipeline: award.Request.Request.Pipeline}).Put(f.ctx, body)
	if err != nil {
		f.t.Fatalf("publish output: %v", err)
	}
	result := &controllerwire.JobResult{Outcome: "success", Output: &output.Key}
	if slices.Contains([]string{"a", "b", "c", "d"}, award.JobID) {
		workspace := f.t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "payload.txt"), []byte("artifact-"+value), 0o600); err != nil {
			f.t.Fatal(err)
		}
		manifest, err := artifacts.Capture(f.ctx, artifacts.Data{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: identity, Pipeline: award.Request.Request.Pipeline}, workspace, []string{"payload.txt"}, func(message string) { f.t.Error(message) })
		if err != nil {
			f.t.Fatalf("publish artifacts: %v", err)
		}
		result.Manifest = &manifest.Key
	}
	return result
}

func (f *featureController) verifyReused(award controllerwire.ClaimedUnit, ids []string) {
	f.t.Helper()
	detail, err := f.control.ReadRun(f.ctx, award.RunID)
	if err != nil {
		f.t.Fatal(err)
	}
	if detail.Retry == nil || !reflect.DeepEqual(detail.Retry.Reused, ids) {
		f.t.Fatalf("reuse selection: %+v", detail.Retry)
	}
	for _, job := range detail.Jobs {
		if slices.Contains(ids, job.ID) && (job.Outcome != "success" || job.Attempt != 0 || job.StartedAt != nil) {
			f.t.Fatalf("reused job executed: %+v", job)
		}
	}
	host := f.host(award)
	principal, err := host.Principal(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	process, err := host.ProcessClient(f.ctx, principal, award.JobID)
	if err != nil {
		f.t.Fatal(err)
	}
	storage, err := host.BindStorage(f.ctx, principal)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, id := range ids {
		output, err := process.Output(f.ctx, award.RunID, id)
		var value map[string]string
		if err != nil || json.Unmarshal(output, &value) != nil || value["value"] != id {
			f.t.Fatalf("reused %s output=%s error=%v", id, output, err)
		}
		producer, err := host.ReadNode(f.ctx, runspec.NodeRef{RunID: runspec.RunID(award.RunID), NodeID: runspec.NodeID(id)})
		if err != nil || producer.ManifestRecord == nil {
			f.t.Fatalf("reused %s manifest: %v", id, err)
		}
		workspace := f.t.TempDir()
		count, err := (nodehost.ArtifactRecords{Blobs: storage.Bytes, Records: storage.Records}).Stage(f.ctx, blob.RecordLookup{Authority: storage.Authority, Key: *producer.ManifestRecord}, workspace, id)
		if err != nil || count != 1 {
			f.t.Fatalf("stage reused %s artifacts: count=%d error=%v", id, count, err)
		}
		data, err := os.ReadFile(filepath.Join(workspace, id, "payload.txt"))
		if err != nil || string(data) != "artifact-"+id {
			f.t.Fatalf("reused %s artifact=%q error=%v", id, data, err)
		}
	}
}
