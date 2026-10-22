package client

import (
	"context"
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.ScheduleManager = (*engineClient)(nil)

// NewScheduleManager uses the shared authenticated transport and route policy
// for schedule observations, cadence edits and idempotent launches.
func NewScheduleManager(options EngineOptions) (controllerwire.ScheduleManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ListSchedules(ctx context.Context, q controllerwire.ScheduleQuery) (controllerwire.ScheduleOverview, error) {
	var result controllerwire.ScheduleOverview
	if q.Next < 0 {
		return result, scheduleInputError("next count must be nonnegative")
	}
	query := url.Values{}
	if q.All {
		query.Set("all", "true")
	}
	if q.Next > 0 {
		query.Set("next", strconv.Itoa(q.Next))
	}
	path := "/api/v1/schedules"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	if err := c.request(ctx, "GET", "/api/v1/schedules", path, "", nil, &result); err != nil {
		return result, err
	}
	if !validScheduleHealth(result.Health) {
		return result, scheduleProtocolError("controller returned invalid schedule health")
	}
	seen := map[string]bool{}
	for _, row := range result.Schedules {
		if !validSchedule(row) || seen[row.ID] {
			return result, scheduleProtocolError("controller returned invalid or repeated schedules")
		}
		seen[row.ID] = true
	}
	for _, row := range result.Upcoming {
		if !seen[row.Schedule] || !validSelector(row.Name) || !validRunnerTimestamp(row.At, false) {
			return result, scheduleProtocolError("controller returned an invalid upcoming schedule")
		}
	}
	return result, nil
}

func (c *engineClient) ReadSchedule(ctx context.Context, name string, q controllerwire.ScheduleReadQuery) (controllerwire.DetailView, error) {
	var result controllerwire.DetailView
	if !validSelector(name) || q.Fires < 0 || q.Next < 0 {
		return result, scheduleInputError("schedule and nonnegative history/next counts are required")
	}
	query := url.Values{"fires": {strconv.Itoa(q.Fires)}}
	if q.Next > 0 {
		query.Set("next", strconv.Itoa(q.Next))
	}
	err := c.request(ctx, "GET", "/api/v1/schedules/{schedule}", schedulePath(name)+"?"+query.Encode(), "", nil, &result)
	if err != nil {
		return result, err
	}
	if !matchesSchedule(result.Schedule, name) {
		return result, scheduleProtocolError("controller returned a conflicting schedule")
	}
	for _, fire := range result.Fires {
		if fire.ScheduleID != result.Schedule.ID || !validSelector(fire.ID) || !validRunnerTimestamp(fire.DueAt, false) || !validRunnerTimestamp(fire.DecidedAt, false) {
			return result, scheduleProtocolError("controller returned invalid schedule history")
		}
	}
	for _, at := range result.Upcoming {
		if !validRunnerTimestamp(at, false) {
			return result, scheduleProtocolError("controller returned an invalid due instant")
		}
	}
	return result, nil
}

func (c *engineClient) SetSchedulePaused(ctx context.Context, name string, pause bool) (controllerwire.ScheduleView, error) {
	action := "resume"
	if pause {
		action = "pause"
	}
	row, err := c.scheduleEdit(ctx, "POST", name, "/"+action, nil)
	if err == nil && row.Paused != pause {
		err = scheduleProtocolError("controller did not confirm the requested schedule state")
	}
	return row, err
}

func (c *engineClient) OverrideSchedule(ctx context.Context, name string, input controllerwire.ScheduleOverride) (controllerwire.ScheduleView, error) {
	if err := input.Validate(); err != nil {
		return controllerwire.ScheduleView{}, scheduleInputError(err.Error())
	}
	row, err := c.scheduleEdit(ctx, "PUT", name, "/override", input)
	if err == nil && !confirmsScheduleOverride(row.Override, input) {
		err = scheduleProtocolError("controller did not confirm the requested schedule override")
	}
	return row, err
}

func (c *engineClient) ResetSchedule(ctx context.Context, name string) (controllerwire.ScheduleView, error) {
	row, err := c.scheduleEdit(ctx, "DELETE", name, "/override", nil)
	if err == nil && len(row.Override.Fields) > 0 {
		err = scheduleProtocolError("controller did not confirm schedule override reset")
	}
	return row, err
}

func (c *engineClient) scheduleEdit(ctx context.Context, method, name, suffix string, input any) (controllerwire.ScheduleView, error) {
	var result controllerwire.ScheduleEnvelope
	if !validSelector(name) {
		return result.Schedule, scheduleInputError("schedule selector is required")
	}
	err := c.request(ctx, method, "/api/v1/schedules/{schedule}"+suffix, schedulePath(name)+suffix, "", input, &result)
	if err == nil && !matchesSchedule(result.Schedule, name) {
		err = scheduleProtocolError("controller returned a conflicting schedule")
	}
	return result.Schedule, err
}

func (c *engineClient) RunSchedule(ctx context.Context, name, key string) (controllerwire.ScheduleRun, error) {
	var result controllerwire.ScheduleRun
	if !validSelector(name) || !validSelector(key) {
		return result, scheduleInputError("schedule launch requires a selector and idempotency key")
	}
	err := c.request(ctx, "POST", "/api/v1/schedules/{schedule}/run", schedulePath(name)+"/run", key, nil, &result)
	if err == nil && (!validSelector(result.RunID) || !matchesSchedule(result.Schedule, name)) {
		err = scheduleProtocolError("controller returned an invalid schedule launch")
	}
	return result, err
}

func (c *engineClient) DeleteSchedule(ctx context.Context, name string) (controllerwire.ScheduleDeletion, error) {
	var result controllerwire.ScheduleDeletion
	if !validSelector(name) {
		return result, scheduleInputError("schedule selector is required")
	}
	err := c.request(ctx, "DELETE", "/api/v1/schedules/{schedule}", schedulePath(name), "", nil, &result)
	if err == nil && !validSelector(result.ScheduleID) {
		err = scheduleProtocolError("controller returned an invalid schedule deletion")
	}
	return result, err
}

func schedulePath(name string) string { return "/api/v1/schedules/" + url.PathEscape(name) }
func matchesSchedule(row controllerwire.ScheduleView, name string) bool {
	name = strings.TrimSpace(name)
	return validSchedule(row) && (row.ID == name || row.Name == name || row.Pipeline == name || row.ScheduleName != "" && row.Pipeline+"/"+row.ScheduleName == name)
}

func validSchedule(row controllerwire.ScheduleView) bool {
	if !validRunnerTimestamp(row.ArmedAt, true) || !validRunnerTimestamp(row.UpdatedAt, true) || !validRunnerTimestamp(row.Override.SetAt, true) {
		return false
	}
	for _, stamp := range []*string{row.LastFiredAt, row.NextDueAt} {
		if stamp != nil && !validRunnerTimestamp(*stamp, false) {
			return false
		}
	}
	return validSelector(row.ID) && validSelector(row.Name) && validSelector(row.Pipeline) && validSelector(row.State) && (row.Where == "local" || row.Where == "controller") && row.CatchUpNS >= 0 && row.Effective.CatchUpNS >= 0
}

func scheduleInputError(message string) error {
	return &apierr.Error{Code: apierr.BadRequest, Message: message}
}

func scheduleProtocolError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}

var _ controllerwire.ScheduleRepositoryManager = (*engineClient)(nil)

// NewScheduleRepositoryManager uses the common controller connection for
// atomic replacement and withdrawal of a repository's schedule declarations.
func NewScheduleRepositoryManager(options EngineOptions) (controllerwire.ScheduleRepositoryManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) WriteSchedules(ctx context.Context, input controllerwire.ScheduleRepoRequest) (controllerwire.ScheduleRepoWritten, error) {
	var result controllerwire.ScheduleRepoWritten
	if input.Mode != controllerwire.ScheduleReplace && input.Mode != controllerwire.ScheduleUpsert {
		return result, scheduleInputError("schedule write mode must be replace or upsert")
	}
	if !validSelector(input.RepoURL) {
		return result, scheduleInputError("repository selector is required")
	}
	seen := map[string]bool{}
	for _, declaration := range input.Schedules {
		key := declaration.Pipeline + "\x00" + declaration.Name
		if !validSelector(declaration.Pipeline) || declaration.Name != "" && !validSelector(declaration.Name) || seen[key] {
			return result, scheduleInputError("invalid or repeated schedule declaration")
		}
		seen[key] = true
		override := controllerwire.ScheduleOverride{Cron: &declaration.Cron}
		if declaration.TZ != "" {
			override.TZ = &declaration.TZ
		}
		if declaration.Overlap != "" {
			override.Overlap = &declaration.Overlap
		}
		if declaration.CatchUp != "" {
			override.CatchUp = &declaration.CatchUp
		}
		if err := override.Validate(); err != nil {
			return result, scheduleInputError(err.Error())
		}
	}
	err := c.request(ctx, "PUT", "/api/v1/schedules/repos", "/api/v1/schedules/repos", "", input, &result)
	if err != nil {
		return result, err
	}
	if input.Mode == controllerwire.ScheduleUpsert && len(result.Withdrawn) > 0 {
		return result, scheduleProtocolError("controller withdrew schedules during a selective update")
	}
	identities := map[string]bool{}
	for _, row := range result.Schedules {
		if !validSchedule(row) || identities[row.ID] || row.RepoPath != input.RepoURL {
			return result, scheduleProtocolError("controller returned invalid or conflicting repository schedules")
		}
		identities[row.ID] = true
	}
	return result, nil
}

func (c *engineClient) DeleteSchedulesForRepo(ctx context.Context, repo string) (controllerwire.ScheduleRepoDeletion, error) {
	var result controllerwire.ScheduleRepoDeletion
	if !validSelector(repo) {
		return result, scheduleInputError("repository selector is required")
	}
	path := "/api/v1/schedules/repos?" + url.Values{"repo_url": {repo}}.Encode()
	err := c.request(ctx, "DELETE", "/api/v1/schedules/repos", path, "", nil, &result)
	if err == nil && (result.RepoURL != repo || result.Removed < 0) {
		err = scheduleProtocolError("controller returned a conflicting schedule repository deletion")
	}
	return result, err
}

func confirmsScheduleOverride(got controllerwire.OverrideView, want controllerwire.ScheduleOverride) bool {
	fields := map[string]bool{}
	for _, field := range got.Fields {
		fields[field] = true
	}
	if want.Cron != nil && (!fields["cron"] || got.Cron != *want.Cron) {
		return false
	}
	if want.TZ != nil && (!fields["tz"] || got.TZ != *want.TZ) {
		return false
	}
	if want.Overlap != nil && (!fields["overlap"] || got.Overlap != *want.Overlap) {
		return false
	}
	if want.CatchUp != nil {
		duration, _ := time.ParseDuration(*want.CatchUp)
		if !fields["catch_up"] || got.CatchUpNS != int64(duration) {
			return false
		}
	}
	return want.Args == nil || fields["args"] && maps.Equal(got.Args.Values(), controllerwire.DisplayScheduleArgs(*want.Args).Values())
}

func validScheduleHealth(h controllerwire.HealthView) bool {
	for _, count := range []int{h.Schedules, h.Armed, h.Paused, h.Undeclared, h.Locked, h.Following, h.Ahead, h.MissingBinary, h.StaleOverride} {
		if count < 0 {
			return false
		}
	}
	return validRunnerTimestamp(h.LastTick.At, true)
}
