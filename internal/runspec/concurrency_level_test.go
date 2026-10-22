package runspec

import "testing"

func FuzzConcurrencyKeySeparatesLevels(f *testing.F) {
	f.Add("X", "run:machine:job:global:X")
	f.Add("job:global:X", "")
	f.Fuzz(func(t *testing.T, name, identity string) {
		for _, scope := range []string{"global", "run", "box"} {
			plan := ConcurrencyKey("plan", scope, identity, name)
			job := ConcurrencyKey("job", scope, identity, name)
			if plan == job {
				t.Fatalf("plan and job alias: %q", plan)
			}
			for _, level := range []string{"plan", "job"} {
				key := ConcurrencyKey(level, scope, identity, name)
				gotLevel, gotScope, _, gotName, ok := ParseConcurrencyKey(key)
				if !ok || gotLevel != level || gotScope != scope || gotName != name {
					t.Fatalf("lost admission level in %q", key)
				}
			}
		}
	})
}
