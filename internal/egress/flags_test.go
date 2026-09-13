package egress_test

import (
	"strings"
	"testing"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func envOf(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

func TestBindDefaultsToUnlimited(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, envOf(nil), egress.ServiceController, egress.ControllerSurfaces)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.Budgeted() {
		t.Fatalf("config = %+v, want every budget unlimited", cfg)
	}
}

func TestBindReadsTheEnvironmentThenTheFlags(t *testing.T) {
	env := envOf(map[string]string{
		egress.EnvName(egress.ServiceController, egress.EnvMonthlyBytes):    "500",
		egress.EnvName(egress.ServiceController, egress.EnvDailyAlarmBytes): "900",
		egress.EnvName(egress.ServiceController, egress.EnvMaxLogStreams):   "3",
		egress.EnvName(egress.ServiceController, egress.EnvMaxDownloads):    "4",
	})

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, env, egress.ServiceController, egress.ControllerSurfaces)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.PerPrincipalMonthlyBytes != 500 || cfg.GlobalDailyAlarmBytes != 900 {
		t.Fatalf("config from the environment = %+v", cfg)
	}
	if cfg.MaxStreamsPerPrincipal != 3 || cfg.MaxDownloadsPerPrincipal != 4 {
		t.Fatalf("concurrency caps from the environment = %+v", cfg)
	}

	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	read = egress.Bind(fs, env, egress.ServiceController, egress.ControllerSurfaces)
	if err := fs.Parse([]string{"--egress-monthly-bytes=1", "--egress-max-log-streams=0"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.PerPrincipalMonthlyBytes != 1 || cfg.MaxStreamsPerPrincipal != 0 || cfg.GlobalDailyAlarmBytes != 900 {
		t.Fatalf("config from the flags = %+v, want the flags to win where they were passed", cfg)
	}
}

// safety: one variable read by three processes on a shared ConfigMap is
// one cap applied three times, so each service must read its own.
func TestEachServiceReadsItsOwnVariables(t *testing.T) {
	env := envOf(map[string]string{
		egress.EnvName(egress.ServiceController, egress.EnvDailyAlarmBytes): "100",
		egress.EnvName(egress.ServiceLogs, egress.EnvDailyAlarmBytes):       "200",
		egress.EnvName(egress.ServiceCache, egress.EnvDailyAlarmBytes):      "300",
	})
	for _, tc := range []struct {
		svc      egress.Service
		surfaces egress.Surfaces
		want     int64
	}{
		{egress.ServiceController, egress.ControllerSurfaces, 100},
		{egress.ServiceLogs, egress.LogsSurfaces, 200},
		{egress.ServiceCache, egress.CacheSurfaces, 300},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		read := egress.Bind(fs, env, tc.svc, tc.surfaces)
		if err := fs.Parse(nil); err != nil {
			t.Fatal(err)
		}
		cfg, err := read()
		if err != nil {
			t.Fatalf("%s: read = %v", tc.svc, err)
		}
		if cfg.GlobalDailyAlarmBytes != tc.want {
			t.Errorf("%s daily alarm = %d, want %d", tc.svc, cfg.GlobalDailyAlarmBytes, tc.want)
		}
	}
	if got := egress.EnvName(egress.ServiceController, egress.EnvMonthlyBytes); got != "SPARKWING_CONTROLLER_EGRESS_MONTHLY_BYTES" {
		t.Errorf("EnvName = %q", got)
	}
}

// safety: a service that cannot tell its callers apart must not offer a
// per-principal cap, because its refusal would fall on all of them.
func TestACacheRegistersNoPerPrincipalBudget(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	read := egress.Bind(fs, envOf(map[string]string{
		egress.EnvName(egress.ServiceCache, egress.EnvMonthlyBytes):  "5",
		egress.EnvName(egress.ServiceCache, egress.EnvMaxLogStreams): "5",
	}), egress.ServiceCache, egress.CacheSurfaces)

	for _, arg := range []string{"--egress-monthly-bytes=2", "--egress-max-log-streams=2", "--egress-max-downloads=2"} {
		if err := fs.Parse([]string{arg}); err == nil {
			t.Errorf("a service with no per-principal budget accepted %s", arg)
		}
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.PerPrincipalMonthlyBytes != 0 || cfg.MaxStreamsPerPrincipal != 0 || cfg.MaxDownloadsPerPrincipal != 0 {
		t.Fatalf("config = %+v, want only the daily alarm", cfg)
	}
}

func TestBindRefusesAMalformedEnvironment(t *testing.T) {
	name := egress.EnvName(egress.ServiceLogs, egress.EnvMonthlyBytes)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, envOf(map[string]string{name: "a lot"}), egress.ServiceLogs, egress.LogsSurfaces)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	_, err := read()
	if err == nil {
		t.Fatal("a malformed budget parsed as a default")
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("error %q does not name the variable", err)
	}
}

func TestBindRefusesANegativeBudget(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, envOf(nil), egress.ServiceController, egress.ControllerSurfaces)
	if err := fs.Parse([]string{"--egress-daily-alarm-bytes=-1"}); err != nil {
		t.Fatal(err)
	}
	_, err := read()
	if err == nil {
		t.Fatal("a negative budget was accepted")
	}
	if !strings.Contains(err.Error(), egress.FlagDailyAlarmBytes) {
		t.Errorf("error %q does not name the flag", err)
	}
}
