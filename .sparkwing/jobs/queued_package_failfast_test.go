package jobs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestQueuedLandStopsPendingPackagesAndJoinsRunningFailures(t *testing.T) {
	if goStepParallelism(affectedPushHost()) != 7 {
		t.Skip("seven-slot queued-Land fixture needs a host with the normal seven-core allowance")
	}
	root := gateFixtureRepo(t)
	var mu sync.Mutex
	started := map[string]bool{}
	allRunning := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "wait-running" {
			select {
			case <-allRunning:
			case <-r.Context().Done():
			}
			return
		}
		mu.Lock()
		started[name] = true
		n := 0
		for key := range started {
			if strings.HasPrefix(key, "running-") {
				n++
			}
		}
		if n == 6 {
			select {
			case <-allRunning:
			default:
				close(allRunning)
			}
		}
		mu.Unlock()
		if strings.HasPrefix(name, "running-") {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}))
	t.Cleanup(func() { unblock(); server.Close() })
	request := fmt.Sprintf(`func signal(t *testing.T, path string) { t.Helper(); client := &http.Client{Timeout: 30*time.Second}; response, err := client.Get(%q+path); if err != nil { t.Fatal(err) }; response.Body.Close() }
`, server.URL)
	imports := "import (\"testing\"; \"net/http\"; \"time\")\n"
	writeGoFile(t, filepath.Join(root, "a_fail", "fixture_test.go"), "package fixture\n"+imports+request+`
func TestAFailure(t *testing.T) { signal(t,"/wait-running"); t.Fatal("queued fixture A failed") }
func TestZLaterBody(t *testing.T) { signal(t,"/A-later"); t.Fatal("queued fixture A later failure") }
`)
	for i := range 6 {
		name := fmt.Sprintf("c%d_running", i)
		writeGoFile(t, filepath.Join(root, name, "fixture_test.go"), "package fixture\n"+imports+request+fmt.Sprintf(`
func TestAAlreadyRunning(t *testing.T) { signal(t,%q) }
func TestZLaterFailure(t *testing.T) { signal(t,%q); t.Fatal(%q) }
`, fmt.Sprintf("/running-%d", i), fmt.Sprintf("/C%d-later", i), fmt.Sprintf("queued fixture C%d later failure", i)))
	}
	writeGoFile(t, filepath.Join(root, "z_pending", "fixture_test.go"), "package fixture\n"+imports+request+`func TestPending(t *testing.T) { signal(t,"/B-started") }
`+criticalTestMarker+`
func TestQueuedCritical(t *testing.T) { if testing.Short() { t.Skip("full critical body") }; signal(t,"/critical-started") }
`)
	gitCommitAll(t, root, "queued package fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.26.0\n")
	gitCommitAll(t, root, "select every fixture package")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Prove queued package failure admission"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, unblock)
	defer stop()
	observed := make(chan struct{})
	var observedOnce sync.Once
	err := runObservedQueuedStep(ctx, host, plan, nil, func(record nodeproto.FrameLog) {
		if strings.Contains(record.Message, "queued fixture A failed") {
			observedOnce.Do(func() { close(observed); unblock() })
		}
	})
	cancel()
	select {
	case <-observed:
	default:
		t.Error("A's genuine emitted failure was not observed before releasing running packages")
	}
	if err == nil || !strings.Contains(err.Error(), "queued fixture A failed") {
		t.Fatalf("queued affected step did not report A's failure: %v", err)
	}
	mu.Lock()
	if started["B-started"] {
		t.Error("pending package B started after A failed")
	}
	if started["critical-started"] {
		t.Error("critical package started after a short-package failure")
	}
	if !started["A-later"] {
		t.Error("A's later test body was suppressed by per-test fail-fast")
	}
	for i := range 6 {
		if !started[fmt.Sprintf("C%d-later", i)] || !strings.Contains(err.Error(), fmt.Sprintf("queued fixture C%d later failure", i)) {
			t.Errorf("already-running C%d did not finish and report its later failure: %v", i, err)
		}
	}
	mu.Unlock()
	var summary string
	var summaries []string
	for _, record := range host.Records() {
		if record.Event == "summary" {
			summaries = append(summaries, record.Msg)
		}
		if strings.Contains(record.Msg, "**Queued affected tests stopped admitting packages**") {
			summary = record.Msg
		}
	}
	if len(summaries) != 1 || !strings.Contains(summaries[len(summaries)-1], "**Queued affected tests stopped admitting packages**") || !strings.Contains(summaries[len(summaries)-1], "**go test timing**") {
		t.Errorf("final persisted step summary must retain cutoff, rerun and timing together; got %d summary writes: %q", len(summaries), summaries)
	}
	for _, inventory := range []string{"Skipped short packages:\n\n- `.`: `fixture/internal`\n- `.`: `fixture/z_pending`", "Skipped critical packages:\n\n- `.`: `fixture/z_pending`"} {
		if !strings.Contains(summary, inventory) {
			t.Errorf("cutoff summary omitted exact skipped inventory %q:\n%s", inventory, summary)
		}
	}
	_, fenced, ok := strings.Cut(summary, "```sh\n")
	if !ok {
		t.Fatalf("cutoff summary omitted a runnable shell block: %s", summary)
	}
	rerun, _, ok := strings.Cut(fenced, "```")
	if !ok {
		t.Fatal("cutoff summary did not close the shell block")
	}
	output, rerunErr := exec.CommandContext(t.Context(), "bash", "-c", rerun).CombinedOutput()
	if rerunErr == nil || !strings.Contains(string(output), "queued fixture A failed") {
		t.Fatalf("rerun failed to preserve selected failures: %v\n%s", rerunErr, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if !started["B-started"] || !started["critical-started"] {
		t.Fatalf("published rerun omitted pending short or critical bodies: %v\n%s", started, output)
	}
}

func TestQueuedLandRunsEveryShortModuleBeforeAnyCritical(t *testing.T) {
	root := gateFixtureRepo(t)
	markers := t.TempDir()
	for _, module := range []struct{ dir, name, pkg string }{{"internal", "root", "internal"}, {".sparkwing", "pipeline", "pipelines"}, {"sparkwing", "sdk", "sdk"}} {
		if module.dir == "sparkwing" {
			writeGoFile(t, filepath.Join(root, module.dir, "go.mod"), "module fixture-sdk\n\ngo 1.26.0\n")
		}
		body := fmt.Sprintf(`package %s
import ("os"; "path/filepath"; "testing")
func TestShortBody(t *testing.T) { if !testing.Short() { t.Skip("short body") }; if err:=os.WriteFile(filepath.Join(%q,%q),[]byte("done"),0600);err!=nil { t.Fatal(err) } }
`+criticalTestMarker+`
func TestCriticalBody(t *testing.T) {
 if testing.Short() { t.Skip("full critical body") }
 for _, name := range []string{"root-short","pipeline-short","sdk-short"} { if _,err:=os.Stat(filepath.Join(%q,name));err!=nil { t.Fatalf("critical preceded %%s: %%v",name,err) } }
 if err:=os.WriteFile(filepath.Join(%q,%q),[]byte("done"),0600);err!=nil { t.Fatal(err) }
}
`, module.pkg, markers, module.name+"-short", markers, markers, module.name+"-critical")
		writeGoFile(t, filepath.Join(root, module.dir, "fixture_test.go"), body)
	}
	gitCommitAll(t, root, "successful queued module fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	for _, dir := range []string{"internal", ".sparkwing", "sparkwing"} {
		writeGoFile(t, filepath.Join(root, dir, "selected.go"), "package "+map[string]string{"internal": "internal", ".sparkwing": "pipelines", "sparkwing": "sdk"}[dir]+"\n")
	}
	gitCommitAll(t, root, "select all three modules")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Prove all shorts precede critical bodies"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	if _, err := host.RunStep(t.Context(), plan, "affected-tests", "test"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root", "pipeline", "sdk"} {
		for _, phase := range []string{"short", "critical"} {
			if _, err := os.Stat(filepath.Join(markers, name+"-"+phase)); err != nil {
				t.Errorf("%s %s body missing: %v", name, phase, err)
			}
		}
	}
}

func TestGateAndMainPushContinueAfterAffectedPackageFailure(t *testing.T) {
	for _, entry := range []string{"gate", "main-hook"} {
		t.Run(entry, func(t *testing.T) {
			root := gateFixtureRepo(t)
			if entry == "gate" {
				configurePublishedSDKFixture(t, root)
			}
			markers := t.TempDir()
			writeGoFile(t, filepath.Join(root, "a_fail", "fixture_test.go"), "package fixture\nimport \"testing\"\nfunc TestFailure(t *testing.T) { t.Fatal(\"collect-all fixture failed\") }\n")
			writeGoFile(t, filepath.Join(root, "z_later", "fixture_test.go"), fmt.Sprintf(`package fixture
import ("os"; "path/filepath"; "testing")
func TestLaterShort(t *testing.T) { if !testing.Short() { t.Skip("short body") }; if err:=os.WriteFile(filepath.Join(%q,"short"),[]byte("done"),0600);err!=nil { t.Fatal(err) } }
`+criticalTestMarker+`
func TestLaterCritical(t *testing.T) { if testing.Short() { t.Skip("full critical body") }; if err:=os.WriteFile(filepath.Join(%q,"critical"),[]byte("done"),0600);err!=nil { t.Fatal(err) } }
`, markers, markers))
			gitCommitAll(t, root, "collect-all fixture")
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			base := gitOutput(t, root, "rev-parse", "HEAD")
			mod := "module fixture\n\ngo 1.26.0\n// Select all fixture packages.\n"
			if entry == "gate" {
				mod += "require " + sdkModulePath + " v0.1.0\n"
			}
			writeGoFile(t, filepath.Join(root, "go.mod"), mod)
			gitCommitAll(t, root, "select both packages")
			job := "gate"
			if entry == "main-hook" {
				head := gitOutput(t, root, "rev-parse", "HEAD")
				t.Setenv(gitenv.PushRefsVar, "refs/heads/proof "+strings.TrimSpace(head)+" refs/heads/main "+strings.TrimSpace(base)+"\n")
				job = "affected-tests"
			}
			host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
				if entry == "gate" {
					return (&Gate{}).Plan(ctx, plan, GateArgs{}, sparkwing.RunContext{Pipeline: "gate"})
				}
				return (&PrePush{}).Plan(ctx, plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
			})
			if entry == "gate" {
				bin := t.TempDir()
				writeDeliveryTool(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\nprintf 'fixture frontend dependency install\\n'\n")
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				for _, step := range []string{"vet", "build", "build-published-sdk", "frontend-deps"} {
					if _, err := host.RunStep(t.Context(), plan, job, step); err != nil {
						t.Fatalf("Gate prerequisite %s: %v", step, err)
					}
				}
			}
			_, err := host.RunStep(t.Context(), plan, job, "test")
			if err == nil || !strings.Contains(err.Error(), "collect-all fixture failed") {
				t.Fatalf("collect-all entry failed to report initial failure: %v", err)
			}
			for _, phase := range []string{"short", "critical"} {
				if _, err := os.Stat(filepath.Join(markers, phase)); err != nil {
					t.Errorf("%s stopped admitting %s bodies after failure: %v", entry, phase, err)
				}
			}
		})
	}
}

func TestQueuedLandReportsTestFailureEvenWhenTestMainExitsZero(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "hidden", "fixture_test.go"), `package fixture
import ("os"; "testing")
func TestHiddenFailure(t *testing.T) { t.Fatal("hidden test failure body") }
func TestMain(m *testing.M) { m.Run(); os.Exit(0) }
`)
	gitCommitAll(t, root, "hidden failure fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.26.0\n")
	gitCommitAll(t, root, "select hidden failure")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Report failures despite a hidden process exit"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	_, err := host.RunStep(t.Context(), plan, "affected-tests", "test")
	if err == nil || !strings.Contains(err.Error(), "TestHiddenFailure") {
		t.Fatalf("TestMain's zero exit hid the inner failure: %v", err)
	}
}

func TestQueuedLandStopsPendingPackagesAfterBuildFailure(t *testing.T) {
	if goStepParallelism(affectedPushHost()) != 7 {
		t.Skip("seven-slot queued-Land fixture needs a host with the normal seven-core allowance")
	}
	root := gateFixtureRepo(t)
	var mu sync.Mutex
	started := map[string]bool{}
	release := make(chan struct{})
	allRunning := make(chan struct{})
	var runningOnce sync.Once
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		started[r.URL.Path] = true
		count := 0
		for path := range started {
			if strings.HasPrefix(path, "/C") {
				count++
			}
		}
		if count == 6 {
			runningOnce.Do(func() { close(allRunning) })
		}
		mu.Unlock()
		if r.URL.Path != "/B" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}))
	t.Cleanup(func() { unblock(); server.Close() })
	writeGoFile(t, filepath.Join(root, "a_build_failure", "fixture_test.go"), "package fixture\nimport \"testing\"\nfunc TestBuildFailure(t *testing.T) { missingBuildSymbol(t) }\n")
	for i, name := range []string{"c0_running", "c1_running", "c2_running", "c3_running", "c4_running", "c5_running", "z_pending"} {
		path := fmt.Sprintf("/C%d", i)
		if name == "z_pending" {
			path = "/B"
		}
		writeGoFile(t, filepath.Join(root, name, "fixture_test.go"), fmt.Sprintf(`package fixture
import ("net/http"; "testing"; "time")
func TestBody(t *testing.T) { client:=&http.Client{Timeout:30*time.Second}; response,err:=client.Get(%q);if err!=nil { t.Fatal(err) };response.Body.Close() }
`, server.URL+path))
	}
	gitCommitAll(t, root, "build failure fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.26.0\n")
	gitCommitAll(t, root, "select build failure")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Stop package admission after build failure"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, unblock)
	defer stop()
	err := runObservedQueuedStep(ctx, host, plan, func(ctx context.Context, request nodeproto.ExecRequest) error {
		if strings.Contains(strings.Join(request.Argv, " "), "go test -json ") && strings.Contains(strings.Join(request.Argv, " "), "fixture/a_build_failure") {
			select {
			case <-allRunning:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, func(record nodeproto.FrameLog) {
		if strings.Contains(record.Message, "missingBuildSymbol") {
			unblock()
		}
	})
	cancel()
	if err == nil || !strings.Contains(err.Error(), "missingBuildSymbol") {
		t.Fatalf("queued step omitted build failure: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if started["/B"] {
		t.Error("pending package started after build failure")
	}
	for i := range 6 {
		if !started[fmt.Sprintf("/C%d", i)] {
			t.Errorf("admitted package C%d was cancelled after its sibling's build failure", i)
		}
	}
}

func TestQueuedLandCancellationJoinsItsTestProcess(t *testing.T) {
	testQueuedLandCancellationJoinsItsTestProcess(t, queuedProcessSnapshot)
}

func TestQueuedLandCancellationAcceptsTerminalSnapshots(t *testing.T) {
	for _, state := range []string{"Z", "X", "x"} {
		t.Run(state, func(t *testing.T) {
			var before queuedTestProcess
			reads := 0
			inspect := func(pid int) (queuedTestProcess, bool, error) {
				process, found, err := queuedProcessSnapshot(pid)
				reads++
				if reads == 1 {
					before = process
					return process, found, err
				}
				// safety: a real survivor must still fail; inject only after actual cleanup has ended execution.
				if err != nil || found && !process.terminated() {
					return process, found, err
				}
				t.Logf("terminal snapshot hook: actual=%+v present=%t supplied_state=%s", process, found, state)
				before.State = state
				return before, true, nil
			}
			testQueuedLandCancellationJoinsItsTestProcess(t, inspect)
			if reads != 2 {
				t.Fatalf("process snapshots=%d, want 2", reads)
			}
		})
	}
}

type queuedTestProcess struct {
	PID, Group, Session int
	State, Birth        string
}

func (p queuedTestProcess) terminated() bool {
	return p.State == "Z" || p.State == "X" || p.State == "x"
}

func queuedProcessSnapshot(pid int) (queuedTestProcess, bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return queuedTestProcess{}, false, nil
	}
	if err != nil {
		return queuedTestProcess{}, false, err
	}
	// safety: Linux comm may contain spaces and parentheses, so numeric fields follow its last closing parenthesis.
	at := strings.LastIndexByte(string(data), ')')
	if at < 0 {
		return queuedTestProcess{}, false, fmt.Errorf("malformed process stat for %d: %q", pid, data)
	}
	fields := strings.Fields(string(data[at+1:]))
	if len(fields) < 20 {
		return queuedTestProcess{}, false, fmt.Errorf("short process stat for %d: %q", pid, data)
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return queuedTestProcess{}, false, err
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return queuedTestProcess{}, false, err
	}
	return queuedTestProcess{PID: pid, Group: group, Session: session, State: fields[0], Birth: fields[19]}, true, nil
}

func testQueuedLandCancellationJoinsItsTestProcess(t *testing.T, inspect func(int) (queuedTestProcess, bool, error)) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("process identity proof uses Linux /proc")
	}
	root := gateFixtureRepo(t)
	started := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pid, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		started <- pid
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	writeGoFile(t, filepath.Join(root, "blocked", "fixture_test.go"), fmt.Sprintf(`package fixture
import ("fmt"; "net/http"; "os"; "testing")
func TestBlocked(t *testing.T) { response,err:=http.Get(fmt.Sprintf(%q,os.Getpid()));if err!=nil { t.Fatal(err) };response.Body.Close() }
`, server.URL+"/%d"))
	gitCommitAll(t, root, "cancellation fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.26.0\n")
	gitCommitAll(t, root, "select blocked test")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Join owned processes on cancellation"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deadline, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	done := make(chan error, 1)
	go func() { _, err := host.RunStep(ctx, plan, "affected-tests", "test"); done <- err }()
	var pid int
	select {
	case pid = <-started:
	case err := <-done:
		t.Fatalf("test process did not start: %v", err)
	case <-deadline.Done():
		t.Fatal("test process did not reach its blocking body")
	}
	before, found, err := inspect(pid)
	if err != nil || !found || before.Birth == "" {
		t.Fatalf("cannot capture test process identity %d: snapshot=%+v present=%t err=%v", pid, before, found, err)
	}
	cancel()
	var stepErr error
	select {
	case stepErr = <-done:
		if stepErr == nil {
			t.Error("cancelled test step succeeded")
		}
	case <-deadline.Done():
		t.Fatal("cancelled step did not join its package command")
	}
	after, found, err := inspect(pid)
	if err != nil {
		t.Fatalf("cannot inspect cancelled process %d: before=%+v err=%v step_error=%v", pid, before, err, stepErr)
	}
	if found && before.Birth == after.Birth && !after.terminated() {
		t.Fatalf("cancelled step returned with test process %d still alive: before=%+v after=%+v step_error=%v", pid, before, after, stepErr)
	}
}

type queuedStepObserver struct {
	sdkserve.Services
	observe func(nodeproto.FrameLog)
}

func (s queuedStepObserver) Emit(record nodeproto.FrameLog) {
	s.Services.Emit(record)
	s.observe(record)
}

func runObservedQueuedStep(ctx context.Context, host sparkwingtest.Host, plan *sparkwing.Plan, beforeExec func(context.Context, nodeproto.ExecRequest) error, observe func(nodeproto.FrameLog)) error {
	ctx = host.Context(ctx)
	if beforeExec != nil {
		realExec, ok := sdkserve.HostExec(ctx)
		if !ok {
			return fmt.Errorf("test host has no command executor")
		}
		ctx = sdkserve.WithHostExec(ctx, func(ctx context.Context, request nodeproto.ExecRequest) (sdkserve.HostExecResult, error) {
			if err := beforeExec(ctx, request); err != nil {
				return sdkserve.HostExecResult{}, err
			}
			return realExec(ctx, request)
		})
	}
	services := queuedStepObserver{Services: sdkserve.ServicesFrom(ctx), observe: observe}
	ctx = sdkserve.DefaultOperations.Context(ctx, nodeproto.RunContext{}, nodeproto.RunnerIdentity{}, services)
	result, err := sdkserve.DefaultOperations.RunStep(ctx, plan, nodeproto.Request{Job: "affected-tests", Step: "test", Attempt: 1, Settled: []string{}})
	if err != nil {
		return err
	}
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func TestQueuedLandReportsCancellationAfterFinalCommandJoined(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "last", "fixture_test.go"), "package fixture\nimport \"testing\"\nfunc TestLast(t *testing.T) { t.Log(\"last real body completed\") }\n")
	gitCommitAll(t, root, "final command cancellation fixture")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = newTestHost(t, sparkwingtest.Options{}).Context(ctx)
	realExec, ok := sdkserve.HostExec(ctx)
	if !ok {
		t.Fatal("real test host command executor missing")
	}
	joined := make(chan struct{})
	ctx = sdkserve.WithHostExec(ctx, func(ctx context.Context, request nodeproto.ExecRequest) (sdkserve.HostExecResult, error) {
		reply, err := realExec(ctx, request)
		if err == nil && reply.Done.ExitCode == 0 && strings.Contains(strings.Join(request.Argv, " "), "go test -json ") {
			close(joined)
			cancel()
		}
		return reply, err
	})
	_, err := runQueuedTestPackages(ctx, []affectedTestPlan{{module: ".", packages: []string{"fixture/last"}}}, currentHost(), t.TempDir(), &goTestTimings{})
	select {
	case <-joined:
	default:
		t.Fatal("final real command did not join successfully before cancellation")
	}
	if err == nil {
		t.Fatal("queued package runner reported success after final-command drain cancellation")
	}
}

func TestQueuedLandPreservesCriticalNameUnionAcrossPackages(t *testing.T) {
	root := gateFixtureRepo(t)
	marker := filepath.Join(t.TempDir(), "unmarked-collision-body")
	writeGoFile(t, filepath.Join(root, "a", "fixture_test.go"), `package fixture
import "testing"
`+criticalTestMarker+`
func TestOne(t *testing.T) {}
`)
	writeGoFile(t, filepath.Join(root, "b", "fixture_test.go"), fmt.Sprintf(`package fixture
import ("os"; "testing")
`+criticalTestMarker+`
func TestTwo(t *testing.T) {}
func TestOne(t *testing.T) { if testing.Short() { t.Skip("full collision body") }; if err:=os.WriteFile(%q,[]byte("done"),0600);err!=nil { t.Fatal(err) } }
`, marker))
	gitCommitAll(t, root, "critical name collision fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.26.0\n")
	gitCommitAll(t, root, "select critical collision packages")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Preserve the full critical name union"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	if _, err := host.RunStep(t.Context(), plan, "affected-tests", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("queued critical packages narrowed the original module-wide name union: %v", err)
	}
}
