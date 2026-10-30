package settings

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestCloudSizeValidationAtRequestAndPlan(t *testing.T) {
	for _, selector := range []string{"sparkwing-3cpu", "linux, sparkwing-3cpu"} {
		t.Run(selector, func(t *testing.T) {
			assertRefused := func(err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "sparkwing-3cpu") {
					t.Fatalf("unsupported size error = %v", err)
				}
				for _, size := range match.CloudSizes() {
					if !strings.Contains(err.Error(), size.Label) {
						t.Errorf("error %q omits valid size %s", err, size.Label)
					}
				}
			}
			_, err := ResolveRequest(Inputs{Context: Context{Submitter: true, Local: true, NumCPU: 2}, Flags: Values{Options: runspec.Options{Requires: present([]string{selector})}}})
			assertRefused(err)
			_, err = ResolveRequest(Inputs{Context: Context{Submitter: true, Local: true, NumCPU: 2}, Flags: Values{Options: runspec.Options{Runner: present(runspec.RunnerSelection{Kind: runspec.RunnerLabel, Value: selector})}}})
			assertRefused(err)
			declarations, err := ProjectCheckoutDeclarations([]byte("defaults:\n  requires: ['"+selector+"']\n"), "sparkwing.yaml", "build")
			if err != nil {
				t.Fatal(err)
			}
			_, err = ResolvePlan(PlanInputs{Declarations: declarations, Context: Context{Local: true}})
			assertRefused(err)
			_, err = ResolvePlan(PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Requires: []string{selector}}}}, Context: Context{Local: true}})
			assertRefused(err)
			_, err = ResolveExpansion(ExpansionInputs{Spec: runspec.Spec{Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}}, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Requires: []string{selector}}}}, Context: Context{Local: true}})
			assertRefused(err)
		})
	}
}

func TestCloudSizeValidationPreservesCustomerLabelsAndAlternatives(t *testing.T) {
	for _, selector := range []string{"linux, sparkwing-2cpu", "sparkwing-2cpu, sparkwing-4cpu", "sparkwing-custom", "custom-3cpu", "pool=custom"} {
		_, err := ResolvePlan(PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Requires: []string{selector}}}}, Context: Context{Local: true}})
		if err != nil {
			t.Errorf("selector %q: %v", selector, err)
		}
	}
}
