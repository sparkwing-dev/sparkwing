package cronspec

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Override preserves explicit empty zones, zero catch-up and empty args.
// Unset fields retain the repository declaration, not a process-local default.
type Override struct {
	Cron    *string            `json:"cron,omitempty"`
	TZ      *string            `json:"tz,omitempty"`
	Overlap *string            `json:"overlap,omitempty"`
	CatchUp *string            `json:"catch_up,omitempty"`
	Args    *map[string]string `json:"args,omitempty"`
}

// Validate checks a cadence edit without applying defaults.
func (input Override) Validate() error {
	if input.Cron == nil && input.TZ == nil && input.Overlap == nil && input.CatchUp == nil && input.Args == nil {
		return errors.New("at least one override field is required")
	}
	if input.Cron != nil {
		if _, err := Parse(*input.Cron); err != nil {
			return fmt.Errorf("invalid schedule cron: %w; example: \"0 * * * *\" (hourly)", err)
		}
	}
	if input.TZ != nil && *input.TZ != "" && *input.TZ != "local" {
		if _, err := time.LoadLocation(*input.TZ); err != nil {
			return errors.New("invalid schedule time zone")
		}
	}
	if input.Overlap != nil && *input.Overlap != "skip" && *input.Overlap != "queue" {
		return errors.New("overlap must be skip or queue")
	}
	if input.CatchUp != nil {
		d, err := time.ParseDuration(*input.CatchUp)
		if err != nil || d < 0 {
			return errors.New("catch-up must be a nonnegative duration")
		}
	}
	if input.Args != nil {
		if *input.Args == nil {
			return errors.New("schedule args must be an object")
		}
		for key := range *input.Args {
			if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") {
				return errors.New("schedule argument names must be nonempty")
			}
		}
	}
	return nil
}
