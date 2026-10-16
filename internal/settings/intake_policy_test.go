package settings

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestIntakeOptionsPreservePresenceAndRefuseForgedPolicyOrigins(t *testing.T) {
	options := runspec.Options{Priority: declaration(0, runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "priority"})}
	result, err := ResolveIntake(options, OperatorInputs{}, Context{Local: true, NumCPU: 2})
	if err != nil || !result.Priority.Set || result.Priority.Value != 0 || result.Priority.Origin.Layer != runspec.LayerFlag {
		t.Fatalf("options=%+v err=%v", result, err)
	}
	options.Priority.Origin.Layer = runspec.LayerOperatorTeam
	if _, err := ResolveIntake(options, OperatorInputs{}, Context{Local: true, NumCPU: 2}); err == nil {
		t.Fatal("accepted forged operator provenance")
	}
	options.Priority.Origin.Layer = runspec.LayerFlag
	options.OwnerExecutionTimeout = declaration(7*time.Hour, runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "owner_execution"})
	if _, err := ResolveIntake(options, OperatorInputs{}, Context{NumCPU: 2}); err == nil {
		t.Fatal("request escaped trusted default owner ceiling")
	}
}

func FuzzIntakeNeverAcceptsOperatorProvenance(f *testing.F) {
	for _, layer := range []string{"operator.team", "operator.global", "operator.profile", "flag", "env", "code", ""} {
		f.Add(layer, 0)
	}
	f.Fuzz(func(t *testing.T, layer string, priority int) {
		options := runspec.Options{Priority: declaration(priority, runspec.SettingOrigin{Layer: runspec.Layer(layer), Key: "priority"})}
		resolved, err := ResolveIntake(options, OperatorInputs{}, Context{Local: true, NumCPU: 2})
		if err == nil && resolved.Priority.Origin.Layer != runspec.LayerFlag && resolved.Priority.Origin.Layer != runspec.LayerEnv && resolved.Priority.Origin.Layer != runspec.LayerSealed && resolved.Priority.Origin.Layer != runspec.LayerMachine && resolved.Priority.Origin.Layer != runspec.LayerDefault {
			t.Fatalf("accepted untrusted origin %+v", resolved.Priority.Origin)
		}
	})
}

func TestIntakeOwnerExecutionResolvesDefaultAgainstCurrentPolicy(t *testing.T) {
	duration := 2 * time.Hour
	for _, captured := range []time.Duration{0, time.Hour} {
		options := runspec.Options{OwnerExecutionTimeout: declaration(captured, runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "owner_execution"})}
		resolved, err := ResolveIntake(options, OperatorInputs{Global: OperatorValues{OwnerExecution: &duration}}, Context{NumCPU: 2})
		if err != nil || resolved.OwnerExecutionTimeout.Value != duration || resolved.OwnerExecutionTimeout.Origin.Layer != runspec.LayerOperatorGlobal {
			t.Fatalf("captured=%s timeout=%+v err=%v", captured, resolved.OwnerExecutionTimeout, err)
		}
		options.OwnerExecutionTimeout.Origin.Layer = runspec.LayerFlag
		resolved, err = ResolveIntake(options, OperatorInputs{Global: OperatorValues{OwnerExecution: &duration}}, Context{NumCPU: 2})
		if captured == 0 && err == nil || captured != 0 && (err != nil || resolved.OwnerExecutionTimeout.Value != captured) {
			t.Fatalf("explicit=%s timeout=%+v err=%v", captured, resolved.OwnerExecutionTimeout, err)
		}
	}
}
