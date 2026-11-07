//go:build unix

package nodehost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// perf: 80-byte lines make bulk output exercise the masked line presenter the way build logs do.
const hostExecBenchLine = "sparkwing host exec benchmark output line 0123456789 abcdefghijklmnopqrstuvwxy"

type hostExecBenchResult struct {
	SamplesNS []int64 `json:"samples_ns"`
	WallNS    int64   `json:"wall_ns"`
	Bytes     int64   `json:"bytes"`
	SDKCPUNS  int64   `json:"sdk_cpu_ns"`
	Errors    int     `json:"errors"`
	FirstErr  string  `json:"first_error,omitempty"`
}

type hostExecBenchJob struct {
	sparkwing.Produces[hostExecBenchResult]
}

func (hostExecBenchJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", runHostExecBench), nil
}

func benchArgv() []string {
	if os.Getenv("HOSTEXEC_BENCH_SIZE") == "bulk" {
		return []string{"sh", "-c", `yes "$L" | head -c "$N"`}
	}
	return []string{"/usr/bin/true"}
}

func processCPU() int64 {
	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	return usage.Utime.Nano() + usage.Stime.Nano()
}

//nolint:contextcheck // sparkwing.Cmd carries the step context it was built with.
func runHostExecBench(ctx context.Context) (hostExecBenchResult, error) {
	variant := os.Getenv("HOSTEXEC_BENCH_VARIANT")
	count, _ := strconv.Atoi(os.Getenv("HOSTEXEC_BENCH_COUNT"))
	workers, _ := strconv.Atoi(os.Getenv("HOSTEXEC_BENCH_CONC"))
	argv := benchArgv()
	env := map[string]string{"L": hostExecBenchLine, "N": os.Getenv("HOSTEXEC_BENCH_BYTES")}
	one := func() (int64, error) {
		switch variant {
		case "direct":
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Env = os.Environ()
			for k, v := range env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			counter := &countingWriter{}
			cmd.Stdout, cmd.Stderr = counter, io.Discard
			err := cmd.Run()
			return counter.n, err
		case "logged":
			_, err := sparkwing.Exec(ctx, argv[0], argv[1:]...).EnvMap(env).Run()
			return 0, err
		default:
			res, err := sparkwing.Exec(ctx, argv[0], argv[1:]...).EnvMap(env).Capture()
			return int64(len(res.Stdout)), err
		}
	}
	for range min(32, count) {
		if _, err := one(); err != nil {
			return hostExecBenchResult{}, err
		}
		if os.Getenv("HOSTEXEC_BENCH_SIZE") == "bulk" {
			break
		}
	}
	var result hostExecBenchResult
	samples := make([]int64, count)
	var next, bytes atomic.Int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	cpu, start := processCPU(), time.Now()
	for range workers {
		wg.Go(func() {
			for {
				i := next.Add(1) - 1
				if i >= int64(count) {
					return
				}
				began := time.Now()
				n, err := one()
				samples[i] = time.Since(began).Nanoseconds()
				bytes.Add(n)
				if err != nil {
					mu.Lock()
					result.Errors++
					if result.FirstErr == "" {
						result.FirstErr = err.Error()
					}
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	result.WallNS, result.SDKCPUNS = time.Since(start).Nanoseconds(), processCPU()-cpu
	result.SamplesNS, result.Bytes = samples, bytes.Load()
	return result, nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

type hostExecBenchSpec struct {
	Lang    string `json:"lang"`
	Variant string `json:"variant"`
	Size    string `json:"size"`
	Conc    int    `json:"concurrency"`
	Count   int    `json:"count"`
	Bytes   int64  `json:"bytes"`
	Out     string `json:"out"`
	Dir     string `json:"dir"`
}

// TestHostExecBenchmark measures one cell for design/rewrite/proposals/thin-sdk-step3/bench.py.
// D spawns directly; T and TC are today's SDK-run Run and Capture, whose output
// reaches the masked log through SDK frames; P1 and P2 are the same calls run by the host.
func TestHostExecBenchmark(t *testing.T) {
	raw := os.Getenv("HOSTEXEC_BENCH")
	if raw == "" {
		t.Skip("HOSTEXEC_BENCH names one benchmark cell")
	}
	var spec hostExecBenchSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	sdkVariant := map[string]string{"D": "direct", "P1": "logged", "P2": "captured"}[spec.Variant]
	logFile, err := os.Create(spec.Out + ".log")
	if err != nil {
		t.Fatal(err)
	}
	logs := bufio.NewWriterSize(logFile, 256<<10)
	var logMu sync.Mutex
	var logLines, logBytes int64
	sink := func(_ context.Context, log nodeproto.FrameLog) error {
		line, err := json.Marshal(log)
		if err != nil {
			return err
		}
		logMu.Lock()
		defer logMu.Unlock()
		logLines++
		logBytes += int64(len(log.Message))
		_, err = logs.Write(append(line, '\n'))
		return err
	}
	h := startHostExecHarnessWithSink(t, spec.Lang, "bench", spec.Dir, sink,
		"HOSTEXEC_SDK_CPUPROFILE="+os.Getenv("HOSTEXEC_SDK_CPUPROFILE"), "GODEBUG="+os.Getenv("HOSTEXEC_SDK_GODEBUG"),
		"HOSTEXEC_BENCH_VARIANT="+sdkVariant, "HOSTEXEC_BENCH_SIZE="+spec.Size, "HOSTEXEC_BENCH_COUNT="+strconv.Itoa(spec.Count),
		"HOSTEXEC_BENCH_CONC="+strconv.Itoa(spec.Conc), "HOSTEXEC_BENCH_BYTES="+strconv.FormatInt(spec.Bytes, 10))
	hostCPU := processCPU()
	began := time.Now()
	var run nodeproto.RunResult
	if err := h.call(t, nodeproto.Request{Op: nodeproto.OpRunStep, Job: "bench", Step: "run", Attempt: 1, Settled: []string{}}).Decode(&run); err != nil {
		t.Fatal(err)
	}
	stepNS, hostCPUNS := time.Since(began).Nanoseconds(), processCPU()-hostCPU
	if run.Outcome != "success" {
		t.Fatalf("bench step: %+v", run.Error)
	}
	if err := h.session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logs.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(run.Output, &result); err != nil {
		t.Fatal(err)
	}
	// safety: the repository is public, so results keep no local paths.
	recorded := spec
	recorded.Out, recorded.Dir = "", ""
	result["spec"], result["step_ns"], result["host_cpu_ns"] = recorded, stepNS, hostCPUNS
	result["log_records"], result["log_message_bytes"] = logLines, logBytes
	line, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.Out, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	fmt.Println("bench", spec.Lang, spec.Variant, spec.Size, spec.Conc, "wall_ms", int64(result["wall_ns"].(float64))/1e6)
}
