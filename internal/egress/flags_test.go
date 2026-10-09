package egress_test

import (
	"strings"
	"testing"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func TestBindDefaultsToUnlimited(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, egress.ControllerSurfaces)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.Budgeted() {
		t.Fatalf("config = %+v, want every budget unlimited", cfg)
	}
}

func TestBindReadsTheFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, egress.ControllerSurfaces)
	if err := fs.Parse([]string{"--egress-daily-alarm-bytes=900", "--egress-max-downloads=4", "--egress-max-log-streams=3"}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.GlobalDailyAlarmBytes != 900 || cfg.MaxStreamsPerPrincipal != 3 || cfg.MaxDownloadsPerPrincipal != 4 {
		t.Fatalf("config = %+v, want the flag values", cfg)
	}
}

func TestBindIgnoresTheEnvironment(t *testing.T) {
	t.Setenv("SPARKWING_CONTROLLER_EGRESS_DAILY_ALARM_BYTES", "not a number")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, egress.ControllerSurfaces)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := read(); err != nil || cfg.GlobalDailyAlarmBytes != 0 {
		t.Fatalf("read = %+v, %v; want the environment ignored", cfg, err)
	}
}

// safety: a service that cannot tell its callers apart must not offer a
// per-principal cap, because its refusal would fall on all of them.
func TestACacheRegistersNoPerPrincipalBudget(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	read := egress.Bind(fs, egress.CacheSurfaces)
	for _, arg := range []string{"--egress-max-log-streams=2", "--egress-max-downloads=2"} {
		if err := fs.Parse([]string{arg}); err == nil {
			t.Errorf("a service with no per-principal budget accepted %s", arg)
		}
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.MaxStreamsPerPrincipal != 0 || cfg.MaxDownloadsPerPrincipal != 0 {
		t.Fatalf("config = %+v, want only the daily alarm", cfg)
	}
}

func TestBindRefusesANegativeBudget(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, egress.ControllerSurfaces)
	if err := fs.Parse([]string{"--egress-daily-alarm-bytes=-1"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := read()
	if err == nil {
		t.Fatal("a negative budget was accepted")
	}
	if !strings.Contains(err.Error(), egress.FlagDailyAlarmBytes) {
		t.Errorf("error %q does not name the flag", err)
	}
}

// TestBind_NamedReportsAZeroOnTheCommandLine covers a caller that fills unset
// budgets from a profile: zero is unlimited and a value in its own right, so
// a budget set to zero has to read as named, not as unset.
func TestBind_NamedReportsAZeroOnTheCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want egress.Named
	}{
		{name: "nothing named"},
		{name: "zero on the command line", args: []string{"--egress-max-downloads=0"}, want: egress.Named{MaxDownloads: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("controller", flag.ContinueOnError)
			read := egress.Bind(fs, egress.ControllerSurfaces)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			_, named, err := read()
			if err != nil {
				t.Fatalf("read = %v, want nil", err)
			}
			if named != tc.want {
				t.Errorf("named = %+v, want %+v", named, tc.want)
			}
		})
	}
}
