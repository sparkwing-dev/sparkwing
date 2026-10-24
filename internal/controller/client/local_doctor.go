package client

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

// SweepLocalSessions invokes the elected local controller's session sweep.
func SweepLocalSessions(ctx context.Context, options EngineOptions, dryRun bool) ([]localhostwire.LocalDoctorFinding, error) {
	transport, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	var result localhostwire.LocalDoctor
	err = transport.request(ctx, "POST", "/api/v1/local/doctor", "/api/v1/local/doctor", "", struct {
		DryRun bool `json:"dry_run"`
	}{dryRun}, &result)
	if result.Failure != nil {
		err = errors.Join(err, result.Failure)
	}
	return result.Findings, err
}
