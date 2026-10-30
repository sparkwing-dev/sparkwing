package controller

import (
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	runID := authorized.Target.RunID
	nodeID := authorized.Target.NodeID
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	a, err := tenant.GetApproval(r.Context(), runID, nodeID)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, approvalView(*a))
	return nil
}

func (s *Server) handleListApprovalsForRun(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	runID := authorized.Target.RunID
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	rows, err := tenant.ListApprovalsForRun(r.Context(), runID)
	if err != nil {
		return engineLookupError(err)
	}
	out := controllerwire.Approvals{Approvals: make([]controllerwire.Approval, 0, len(rows))}
	for _, row := range rows {
		out.Approvals = append(out.Approvals, approvalView(*row))
	}
	return writeListPage(w, r, authorized, "approvals", out.Approvals, func(row controllerwire.Approval) string {
		return fmt.Sprintf("%016x/%s/%s", uint64(row.RequestedAt.UnixNano())^(1<<63), row.RunID, row.JobID)
	}, nil)
}

func (s *Server) handleListPendingApprovals(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	rows, err := tenant.ListPendingApprovals(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	out := controllerwire.Approvals{Approvals: make([]controllerwire.Approval, 0, len(rows))}
	for _, row := range rows {
		out.Approvals = append(out.Approvals, approvalView(*row))
	}
	return writeListPage(w, r, authorized, "approvals", out.Approvals, func(row controllerwire.Approval) string {
		return fmt.Sprintf("%016x/%s/%s", uint64(row.RequestedAt.UnixNano())^(1<<63), row.RunID, row.JobID)
	}, nil)
}

func approvalView(a store.Approval) controllerwire.Approval {
	return controllerwire.Approval{
		RunID: a.RunID, JobID: a.NodeID, RequestedAt: a.RequestedAt,
		Message: a.Message, TimeoutMS: a.TimeoutMS, OnTimeout: a.OnTimeout, Approver: a.Approver,
		ResolvedAt: a.ResolvedAt, Resolution: a.Resolution, Comment: a.Comment,
	}
}
