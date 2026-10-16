package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	stressLightSequential = "light-sequential"
	stressLightParallel   = "light-parallel"
	stressMediumFanIn     = "medium-fan-in"
	stressCPUHeavy        = "cpu-heavy-parallel"
	stressHeavyParallel   = "heavy-parallel"
	stressMatrix          = "matrix"
)

var admissionStressSink atomic.Uint64

// AdmissionStressArgs select a repeatable local-admission workload.
type AdmissionStressArgs struct {
	Class string `flag:"class" desc:"Admission class: critical, interactive, normal, or batch. Default: batch"`
}

type AdmissionStress struct {
	sparkwing.Base
	workload string
}

type (
	AdmissionStressMatrix          struct{ AdmissionStress }
	AdmissionStressLightSequential struct{ AdmissionStress }
	AdmissionStressLightParallel   struct{ AdmissionStress }
	AdmissionStressMediumFanIn     struct{ AdmissionStress }
	AdmissionStressCPUHeavy        struct{ AdmissionStress }
	AdmissionStressHeavy           struct{ AdmissionStress }
)

func (AdmissionStress) ShortHelp() string {
	return "Controlled CPU, memory, sleep, and DAG workloads for admission testing"
}

func (AdmissionStress) Help() string {
	return "Runs reproducible synthetic workloads with explicit resource charges so local admission modes can be compared under the same pressure. Profiles cover short sequential work, short parallel work, medium CPU fan-out/fan-in, CPU-heavy low-memory fan-out, combined CPU-and-memory fan-out, or a matrix that runs all five phases. The default admission class is batch so stress work yields to hooks; --class makes priority and aging experiments explicit. Run multiple instances concurrently to exercise inter-run contention. This pipeline is intentionally manual and is not part of gate or release checks."
}

func (AdmissionStress) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run every workload phase as batch pressure", Command: "sparkwing run admission-stress"},
		{Comment: "Model a short latency-sensitive run", Command: "sparkwing run admission-stress-light-sequential --class interactive"},
		{Comment: "Saturate CPU and memory while testing another run", Command: "sparkwing run admission-stress-heavy --class batch"},
	}
}

func (p AdmissionStress) Plan(_ context.Context, plan *sparkwing.Plan, in AdmissionStressArgs, _ sparkwing.RunContext) error {
	profile := p.workload
	if profile == "" {
		profile = stressMatrix
	}
	class, err := stressAdmissionClass(in.Class)
	if err != nil {
		return err
	}
	plan.AdmissionClass(class)
	if profile == stressLightSequential {
		plan.Resources(sparkwing.Cores(0.2))
	}

	specs, err := admissionStressProfile(profile)
	if err != nil {
		return err
	}
	nodes := make(map[string]*sparkwing.JobNode, len(specs))
	for _, spec := range specs {
		hints := []sparkwing.ResourceHint{sparkwing.Cores(spec.Cores)}
		if spec.MemoryMiB > 0 {
			hints = append(hints, sparkwing.MemoryGB(float64(spec.MemoryMiB)/1024))
		}
		node := sparkwing.Job(plan, spec.ID, stressWork(spec)).Resources(hints...)
		for _, dep := range spec.Needs {
			node.Needs(nodes[dep])
		}
		nodes[spec.ID] = node
	}
	return nil
}

type admissionStressSpec struct {
	ID        string
	Needs     []string
	Duration  time.Duration
	Cores     float64
	MemoryMiB int
	Workers   int
}

func stressAdmissionClass(value string) (sparkwing.AdmissionClass, error) {
	switch strings.TrimSpace(value) {
	case "", string(sparkwing.AdmissionBatch):
		return sparkwing.AdmissionBatch, nil
	case string(sparkwing.AdmissionCritical):
		return sparkwing.AdmissionCritical, nil
	case string(sparkwing.AdmissionInteractive):
		return sparkwing.AdmissionInteractive, nil
	case string(sparkwing.AdmissionNormal):
		return sparkwing.AdmissionNormal, nil
	default:
		return "", fmt.Errorf("admission-stress: class must be critical, interactive, normal, or batch")
	}
}

func admissionStressProfile(profile string) ([]admissionStressSpec, error) {
	switch profile {
	case stressLightSequential:
		return lightSequentialSpecs("", nil), nil
	case stressLightParallel:
		return lightParallelSpecs("", nil), nil
	case stressMediumFanIn:
		return mediumFanInSpecs("", nil), nil
	case stressCPUHeavy:
		return cpuHeavySpecs("", nil), nil
	case stressHeavyParallel:
		return heavyParallelSpecs("", nil), nil
	case stressMatrix:
		lightSeq := lightSequentialSpecs("light-sequential-", nil)
		lightPar := lightParallelSpecs("light-parallel-", []string{lastSpecID(lightSeq)})
		medium := mediumFanInSpecs("medium-fan-in-", leafSpecIDs(lightPar))
		cpuHeavy := cpuHeavySpecs("cpu-heavy-parallel-", leafSpecIDs(medium))
		heavy := heavyParallelSpecs("heavy-parallel-", leafSpecIDs(cpuHeavy))
		return append(append(append(append(lightSeq, lightPar...), medium...), cpuHeavy...), heavy...), nil
	default:
		return nil, fmt.Errorf("admission-stress: workload must be light-sequential, light-parallel, medium-fan-in, cpu-heavy-parallel, heavy-parallel, or matrix")
	}
}

func cpuHeavySpecs(prefix string, needs []string) []admissionStressSpec {
	out := make([]admissionStressSpec, 0, 6)
	for i := 1; i <= 6; i++ {
		out = append(out, admissionStressSpec{
			ID: fmt.Sprintf("%sheavy-%d", prefix, i), Needs: needs,
			Duration: 4 * time.Second, Cores: 2, Workers: 2,
		})
	}
	return out
}

func lightSequentialSpecs(prefix string, firstNeeds []string) []admissionStressSpec {
	out := make([]admissionStressSpec, 0, 4)
	needs := firstNeeds
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("%slight-%d", prefix, i)
		out = append(out, admissionStressSpec{ID: id, Needs: needs, Duration: 250 * time.Millisecond, Cores: 0.1, MemoryMiB: 16})
		needs = []string{id}
	}
	return out
}

func lightParallelSpecs(prefix string, needs []string) []admissionStressSpec {
	out := make([]admissionStressSpec, 0, 8)
	for i := 1; i <= 8; i++ {
		out = append(out, admissionStressSpec{
			ID: fmt.Sprintf("%slight-%d", prefix, i), Needs: needs,
			Duration: 500 * time.Millisecond, Cores: 0.1, MemoryMiB: 16,
		})
	}
	return out
}

func mediumFanInSpecs(prefix string, firstNeeds []string) []admissionStressSpec {
	prepareID := prefix + "prepare"
	out := []admissionStressSpec{{ID: prepareID, Needs: firstNeeds, Duration: 100 * time.Millisecond, Cores: 0.1, MemoryMiB: 16}}
	fanout := make([]string, 0, 4)
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("%scompute-%d", prefix, i)
		out = append(out, admissionStressSpec{ID: id, Needs: []string{prepareID}, Duration: 2 * time.Second, Cores: 1, MemoryMiB: 128, Workers: 1})
		fanout = append(fanout, id)
	}
	return append(out, admissionStressSpec{ID: prefix + "join", Needs: fanout, Duration: 100 * time.Millisecond, Cores: 0.1, MemoryMiB: 16})
}

func heavyParallelSpecs(prefix string, needs []string) []admissionStressSpec {
	out := make([]admissionStressSpec, 0, 3)
	for i := 1; i <= 3; i++ {
		out = append(out, admissionStressSpec{
			ID: fmt.Sprintf("%sheavy-%d", prefix, i), Needs: needs,
			Duration: 4 * time.Second, Cores: 2, MemoryMiB: 512, Workers: 2,
		})
	}
	return out
}

func lastSpecID(specs []admissionStressSpec) string {
	return specs[len(specs)-1].ID
}

func leafSpecIDs(specs []admissionStressSpec) []string {
	needed := make(map[string]bool)
	for _, spec := range specs {
		for _, dep := range spec.Needs {
			needed[dep] = true
		}
	}
	var leaves []string
	for _, spec := range specs {
		if !needed[spec.ID] {
			leaves = append(leaves, spec.ID)
		}
	}
	return leaves
}

func stressWork(spec admissionStressSpec) func(context.Context) error {
	return func(ctx context.Context) error {
		memory := make([]byte, spec.MemoryMiB<<20)
		for i := 0; i < len(memory); i += 4096 {
			memory[i] = byte(i)
		}

		workCtx, cancel := context.WithTimeout(ctx, spec.Duration)
		defer cancel()
		if spec.Workers == 0 {
			<-workCtx.Done()
		} else {
			var wg sync.WaitGroup
			wg.Add(spec.Workers)
			for worker := 0; worker < spec.Workers; worker++ {
				go func(worker int) {
					defer wg.Done()
					seed := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", spec.ID, worker)))
					for workCtx.Err() == nil {
						seed = sha256.Sum256(seed[:])
					}
					admissionStressSink.Add(binary.LittleEndian.Uint64(seed[:8]))
				}(worker)
			}
			wg.Wait()
		}
		runtime.KeepAlive(memory)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
}

func init() {
	sparkwing.Register("admission-stress", func() sparkwing.Pipeline[AdmissionStressArgs] {
		return &AdmissionStressMatrix{AdmissionStress{workload: stressMatrix}}
	})
	sparkwing.Register("admission-stress-light-sequential", func() sparkwing.Pipeline[AdmissionStressArgs] {
		return &AdmissionStressLightSequential{AdmissionStress{workload: stressLightSequential}}
	})
	sparkwing.Register("admission-stress-light-parallel", func() sparkwing.Pipeline[AdmissionStressArgs] {
		return &AdmissionStressLightParallel{AdmissionStress{workload: stressLightParallel}}
	})
	sparkwing.Register("admission-stress-medium-fan-in", func() sparkwing.Pipeline[AdmissionStressArgs] {
		return &AdmissionStressMediumFanIn{AdmissionStress{workload: stressMediumFanIn}}
	})
	sparkwing.Register("admission-stress-cpu-heavy", func() sparkwing.Pipeline[AdmissionStressArgs] {
		return &AdmissionStressCPUHeavy{AdmissionStress{workload: stressCPUHeavy}}
	})
	sparkwing.Register("admission-stress-heavy", func() sparkwing.Pipeline[AdmissionStressArgs] {
		return &AdmissionStressHeavy{AdmissionStress{workload: stressHeavyParallel}}
	})
}
