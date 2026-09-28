package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A runner scale step written in vCPU-second credits of $0.00005 keeps its
// dollar value in the $0.001 credit: 1,000,000 of the first, fifty dollars,
// are 50,000 of the second.
func TestSchemaV75RestatesTheRunnerScaleStepSQLite(t *testing.T) {
	assertV75RestatesTheRunnerScaleStep(t, storetest.NewSQLite)
}

func TestSchemaV75RestatesTheRunnerScaleStepPostgres(t *testing.T) {
	assertV75RestatesTheRunnerScaleStep(t, storetest.NewPostgres)
}

func assertV75RestatesTheRunnerScaleStep(t *testing.T, newTarget func(*testing.T) *storetest.Target) {
	cases := []struct {
		name string
		old  string
		want string
	}{
		{name: "a step", old: "1000000", want: "50000"},
		{name: "half a credit rounds up", old: "30", want: "2"},
		{name: "a sliver keeps scaling on", old: "9", want: "1"},
		{name: "the old ceiling", old: "200000000000", want: "10000000000"},
		{name: "scaling off", old: "0", want: "0"},
		{name: "unreadable", old: "fifty", want: "fifty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := newTarget(t)
			seeded, err := target.TryOpen()
			if err != nil {
				t.Fatalf("Open#1: %v", err)
			}
			ctx := context.Background()
			writeStepSetting(t, seeded, tc.old)
			// safety: the step on a store already at v75 is in today's credit,
			// so opening it must leave the step alone.
			_ = seeded.Close()
			current, err := target.TryOpen()
			if err != nil {
				t.Fatalf("Open#2: %v", err)
			}
			if got := readStepSetting(t, current); got != tc.old {
				t.Fatalf("step on a current store = %q, want %q unchanged", got, tc.old)
			}
			for _, statement := range []string{
				`DELETE FROM sparkwing_requirements WHERE name = 'credit-value-v1'`,
				`DELETE FROM sparkwing_schema_version WHERE version >= 75`,
			} {
				if _, err := current.DB().ExecContext(ctx, statement); err != nil {
					t.Fatalf("stamp v74 with %q: %v", statement, err)
				}
			}
			_ = current.Close()

			upgraded, err := target.TryOpen()
			if err != nil {
				t.Fatalf("Open#3 (upgrade): %v", err)
			}
			if got := readStepSetting(t, upgraded); got != tc.want {
				t.Fatalf("step after upgrade = %q, want %q", got, tc.want)
			}
			if v, err := upgraded.CurrentSchemaVersion(ctx); err != nil || v != store.ExpectedSchemaVersion() {
				t.Fatalf("schema after upgrade = %d, %v; want %d", v, err, store.ExpectedSchemaVersion())
			}
			requirements, err := upgraded.Requirements(ctx)
			if err != nil || !slices.Contains(requirements, "credit-value-v1") {
				t.Fatalf("upgraded requirements = %v, %v", requirements, err)
			}
			older := slices.DeleteFunc(slices.Clone(store.KnownRequirements()),
				func(name string) bool { return name == "credit-value-v1" })
			if missing := store.MissingRequirements(older, requirements); !slices.Contains(missing, "credit-value-v1") {
				t.Fatalf("a controller predating v75 accepts the restated step: missing %v", missing)
			}
			_ = upgraded.Close()

			reopened, err := target.TryOpen()
			if err != nil {
				t.Fatalf("Open#4: %v", err)
			}
			defer func() { _ = reopened.Close() }()
			if got := readStepSetting(t, reopened); got != tc.want {
				t.Fatalf("step after a second open = %q, want %q", got, tc.want)
			}
		})
	}
}
