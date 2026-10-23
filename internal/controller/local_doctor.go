package controller

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// WithLocalSessions binds the private controller's step session ledger.
func (s *Server) WithLocalSessions(directory string) *Server {
	s.localSessions = sessionledger.Open(directory)
	return s
}

func (s *Server) localDoctor(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	_, peer := localhostwire.LocalPeer(r.Context())
	if !peer || a.Principal.Class != capability.CredentialPeer || a.Target.Team != string(store.DefaultTeam) {
		return apierr.Authorization(apierr.ReasonBinding, "session sweep requires the verified local owner")
	}
	if s.localSessions == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "local session ledger is unavailable"}
	}
	tenant, err := s.tenantForTeam(r.Context(), store.DefaultTeam)
	if err != nil {
		return engineLookupError(err)
	}
	var request struct {
		DryRun bool `json:"dry_run"`
	}
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	s.localSessionMu.Lock()
	defer s.localSessionMu.Unlock()
	outcomes, sweepErr := s.localSessions.Sweep(r.Context(), sessionledger.SweepOptions{DryRun: request.DryRun})
	findings := []localhostwire.LocalDoctorFinding{}
	failure := sweepErr
	for _, outcome := range outcomes {
		switch outcome.Verdict {
		case sessionledger.VerdictLive:
			continue
		case sessionledger.VerdictWouldReap:
			findings = append(findings, localhostwire.LocalDoctorFinding{Code: "stray_step_session", Message: "step session would be reaped", Unit: outcome.Run})
			continue
		}
		for _, recordErr := range sessionledger.RecordOutcomes(r.Context(), tenant, "doctor", []sessionledger.Outcome{outcome}) {
			findings = append(findings, localhostwire.LocalDoctorFinding{Code: "unrecorded_step_outcome", Unit: outcome.Run, Message: fmt.Sprintf("step cleanup %s could not be recorded: %v", outcome.Verdict, recordErr)})
		}
		if outcome.Err != nil {
			failure = errors.Join(failure, outcome.Err)
			findings = append(findings, localhostwire.LocalDoctorFinding{Code: "step_cleanup_failed", Unit: outcome.Run, Message: outcome.Err.Error()})
		}
	}
	result := localhostwire.LocalDoctor{Findings: findings}
	if failure != nil {
		result.Failure = &apierr.Error{Code: apierr.Internal, Message: failure.Error()}
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
