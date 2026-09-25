package controller

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	defaultUsageWeeks = 12
	maxUsageWeeks     = 104
)

type usageMetricsResponse struct {
	GeneratedAt   time.Time             `json:"generated_at"`
	ExcludedTeams []store.Team          `json:"excluded_teams"`
	Weeks         []store.UsageWeek     `json:"weeks"`
	FirstGreen    store.FirstGreenStats `json:"time_to_first_green"`
}

// handleUsageMetrics reports the deployment's weekly traction to its
// operator. The default team is the operator's own and never counts;
// exclude_team names more, such as a team the operator signed up to test.
func (s *Server) handleUsageMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	weeks := defaultUsageWeeks
	if v := q.Get("weeks"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxUsageWeeks {
			writeError(w, http.StatusBadRequest, errors.New("weeks must be an integer from 1 to 104"))
			return
		}
		weeks = n
	}
	exclude := []store.Team{store.DefaultTeam}
	for _, name := range q["exclude_team"] {
		if team := store.NormalizeTeam(store.Team(name)); team != "" {
			exclude = append(exclude, team)
		}
	}
	now := time.Now().UTC()
	metrics, err := s.store.AsOperator().UsageMetrics(r.Context(), now, weeks, exclude)
	if err != nil {
		s.writeInternalError(w, r, "usage metrics", err)
		return
	}
	writeJSON(w, http.StatusOK, usageMetricsResponse{
		GeneratedAt: now, ExcludedTeams: exclude, Weeks: metrics.Weeks, FirstGreen: metrics.FirstGreen,
	})
}
