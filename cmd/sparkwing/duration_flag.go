package main

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	flag "github.com/spf13/pflag"
)

type lookbackDurationValue time.Duration

func (v *lookbackDurationValue) Set(raw string) error {
	d, err := orchestrator.ParseLooseDuration(raw)
	if err != nil {
		return err
	}
	*v = lookbackDurationValue(d)
	return nil
}

func (v *lookbackDurationValue) String() string {
	return time.Duration(*v).String()
}

func (v *lookbackDurationValue) Type() string { return "duration" }

func lookbackDuration(fs *flag.FlagSet, name string, value time.Duration, usage string) *time.Duration {
	v := lookbackDurationValue(value)
	fs.Var(&v, name, usage)
	return (*time.Duration)(&v)
}
