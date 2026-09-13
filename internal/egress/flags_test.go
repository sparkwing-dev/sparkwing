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
	read := egress.Bind(fs, envOf(nil), egress.WithLogStreams)
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
		egress.EnvMonthlyBytes:    "500",
		egress.EnvDailyAlarmBytes: "900",
		egress.EnvMaxLogStreams:   "3",
	})

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, env, egress.WithLogStreams)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.PerPrincipalMonthlyBytes != 500 || cfg.GlobalDailyAlarmBytes != 900 || cfg.MaxStreamsPerPrincipal != 3 {
		t.Fatalf("config from the environment = %+v", cfg)
	}

	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	read = egress.Bind(fs, env, egress.WithLogStreams)
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

func TestBindWithoutLogStreamsRegistersNoStreamFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	read := egress.Bind(fs, envOf(map[string]string{egress.EnvMaxLogStreams: "5"}), egress.WithoutLogStreams)
	if err := fs.Parse([]string{"--egress-max-log-streams=2"}); err == nil {
		t.Fatal("a service that holds no stream open accepted the stream cap")
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := read()
	if err != nil {
		t.Fatalf("read = %v, want nil", err)
	}
	if cfg.MaxStreamsPerPrincipal != 0 {
		t.Fatalf("MaxStreamsPerPrincipal = %d, want 0", cfg.MaxStreamsPerPrincipal)
	}
}

func TestBindRefusesAMalformedEnvironment(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, envOf(map[string]string{egress.EnvMonthlyBytes: "a lot"}), egress.WithLogStreams)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	_, err := read()
	if err == nil {
		t.Fatal("a malformed budget parsed as a default")
	}
	if !strings.Contains(err.Error(), egress.EnvMonthlyBytes) {
		t.Errorf("error %q does not name the variable", err)
	}
}

func TestBindRefusesANegativeBudget(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	read := egress.Bind(fs, envOf(nil), egress.WithLogStreams)
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
