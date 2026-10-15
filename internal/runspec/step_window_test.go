package runspec

import (
	"reflect"
	"testing"
	"testing/quick"
)

func TestStepWindowsRemainSealedAcrossAcceptanceAndRetry(t *testing.T) {
	base := fixture(t)
	property := func(start, stop, only string) bool {
		request := base.Request.Request
		origin := SettingOrigin{Layer: LayerFlag}
		request.Options.StartAt = Setting[string]{Optional: Optional[string]{Set: true, Value: start}, Origin: origin}
		request.Options.StopAt = Setting[string]{Optional: Optional[string]{Set: true, Value: stop}, Origin: origin}
		request.Options.Only = Setting[string]{Optional: Optional[string]{Set: true, Value: only}, Origin: origin}
		sealed, err := SealRequest(request, IntakeFacts{RunID: "source"})
		if err != nil {
			return false
		}
		resolved := base.Resolved
		resolved.Settings.Options = request.Options
		input := SealInput{Request: sealed, Resolved: resolved, Plan: []byte(`{"version":2,"jobs":[]}`)}
		spec, err := SealSpec(input)
		if err != nil {
			return false
		}
		retry, err := CarryRetry(RetryInput{Request: sealed, Spec: Optional[Spec]{Set: true, Value: spec}, Plan: input.Plan, SourceRunID: "source"})
		if err != nil || !reflect.DeepEqual(retry.Request.Request.Options, request.Options) || !reflect.DeepEqual(retry.Spec.Value.Resolved.Settings.Options, request.Options) {
			return false
		}
		for _, field := range []string{"StartAt", "StopAt", "Only"} {
			changed := input
			options := &changed.Resolved.Settings.Options
			selection := reflect.ValueOf(options).Elem().FieldByName(field).Addr().Interface().(*Setting[string])
			selection.Value += "changed"
			if _, err := SealSpec(changed); err == nil {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}
