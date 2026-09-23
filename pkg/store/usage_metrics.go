package store

import (
	"context"
	"slices"
	"sort"
	"time"
)

const usageWeek = 7 * 24 * time.Hour

// UsageWeekStart returns the Monday 00:00 UTC that begins the week holding at.
func UsageWeekStart(at time.Time) time.Time {
	day := at.UTC().Truncate(24 * time.Hour)
	offset := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -offset)
}

// UsageMetrics is the deployment's traction over whole UTC weeks, computed
// from the controller's own tables. It names no team, account or source.
type UsageMetrics struct {
	// Weeks is oldest first; the last entry is the week in progress.
	Weeks      []UsageWeek     `json:"weeks"`
	FirstGreen FirstGreenStats `json:"time_to_first_green"`
}

// UsageWeek is one Monday-to-Monday UTC week.
type UsageWeek struct {
	WeekStart time.Time `json:"week_start"`
	// ActiveTeams counts teams with at least one run started in the week.
	ActiveTeams int `json:"active_teams"`
	Runs        int `json:"runs"`
	// CloudRuns ran at least one node on a cloud runner; ConnectedRuns ran
	// every node on a machine the team connected.
	CloudRuns     int `json:"cloud_runs"`
	ConnectedRuns int `json:"connected_runs"`
	// TeamsByRuns is how many active teams started each band of runs.
	TeamsByRuns             RunsBands `json:"teams_by_runs"`
	MedianRunsPerActiveTeam float64   `json:"median_runs_per_active_team"`
	NewAccounts             int       `json:"new_accounts"`
	NewTeams                int       `json:"new_teams"`
}

// RunsBands buckets active teams by how many runs they started in a week.
type RunsBands struct {
	One            int `json:"runs_1"`
	TwoToNine      int `json:"runs_2_to_9"`
	TenToFortyNine int `json:"runs_10_to_49"`
	FiftyPlus      int `json:"runs_50_plus"`
}

func (b *RunsBands) add(runs int) {
	switch {
	case runs >= 50:
		b.FiftyPlus++
	case runs >= 10:
		b.TenToFortyNine++
	case runs >= 2:
		b.TwoToNine++
	default:
		b.One++
	}
}

// FirstGreenStats measures, for teams created in the window, the time from
// the team's creation to the finish of its first successful run.
type FirstGreenStats struct {
	TeamsCreated  int   `json:"teams_created"`
	TeamsGreen    int   `json:"teams_green"`
	MedianSeconds int64 `json:"median_seconds"`
	P90Seconds    int64 `json:"p90_seconds"`
	WithinHour    int   `json:"within_hour"`
	WithinDay     int   `json:"within_day"`
	WithinWeek    int   `json:"within_week"`
	Later         int   `json:"later"`
}

// UsageMetrics computes the weeks whole UTC weeks ending with the one that
// holds now. Rows of the excluded teams, such as the operator's own, count
// toward nothing.
func (o *Operator) UsageMetrics(ctx context.Context, now time.Time, weeks int, exclude []Team) (*UsageMetrics, error) {
	if weeks < 1 {
		weeks = 1
	}
	from := UsageWeekStart(now).Add(-time.Duration(weeks-1) * usageWeek)
	to := UsageWeekStart(now).Add(usageWeek)
	out := &UsageMetrics{Weeks: make([]UsageWeek, weeks)}
	for i := range out.Weeks {
		out.Weeks[i].WeekStart = from.Add(time.Duration(i) * usageWeek)
	}
	excluded := func(team string) bool { return slices.Contains(exclude, Team(team)) }
	if err := o.usageRuns(ctx, from, to, excluded, out.Weeks); err != nil {
		return nil, err
	}
	if err := o.usageNewTeams(ctx, from, to, excluded, out); err != nil {
		return nil, err
	}
	if err := o.usageNewAccounts(ctx, from, to, out.Weeks); err != nil {
		return nil, err
	}
	return out, nil
}

func weekIndex(from time.Time, ns int64) int {
	return int((ns - from.UnixNano()) / int64(usageWeek))
}

func (o *Operator) usageRuns(ctx context.Context, from, to time.Time, excluded func(string) bool, weeks []UsageWeek) (err error) {
	rows, err := o.s.query(ctx, `
SELECT team, wk, COUNT(*), SUM(cloud)
  FROM (SELECT team, (started_at - ?) / ? AS wk,
               CASE WHEN EXISTS (SELECT 1 FROM node_execution_attempts a
                                  WHERE a.run_id = runs.id AND a.executor_location = 'cloud')
                    THEN 1 ELSE 0 END AS cloud
          FROM runs
         WHERE started_at >= ? AND started_at < ?) AS weekly
 GROUP BY team, wk`, from.UnixNano(), int64(usageWeek), from.UnixNano(), to.UnixNano())
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	perWeek := make([][]int, len(weeks))
	for rows.Next() {
		var team string
		var wk, runs, cloud int64
		if err := rows.Scan(&team, &wk, &runs, &cloud); err != nil {
			return err
		}
		if excluded(team) || wk < 0 || int(wk) >= len(weeks) {
			continue
		}
		w := &weeks[wk]
		w.ActiveTeams++
		w.Runs += int(runs)
		w.CloudRuns += int(cloud)
		w.ConnectedRuns += int(runs - cloud)
		w.TeamsByRuns.add(int(runs))
		perWeek[wk] = append(perWeek[wk], int(runs))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, counts := range perWeek {
		weeks[i].MedianRunsPerActiveTeam = median(counts)
	}
	return nil
}

func median(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Ints(values)
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return float64(values[mid])
	}
	return float64(values[mid-1]+values[mid]) / 2
}

// safety: a green run counts from its finish, falling back to its start for
// a row with no finish time, because the team saw green when it finished.
func (o *Operator) usageNewTeams(ctx context.Context, from, to time.Time, excluded func(string) bool, out *UsageMetrics) (err error) {
	rows, err := o.s.query(ctx, `
SELECT t.name, t.created_at,
       (SELECT MIN(COALESCE(r.finished_at, r.started_at))
          FROM runs r
         WHERE r.team = t.name AND r.status = 'success') AS first_green
  FROM teams t
 WHERE t.created_at >= ? AND t.created_at < ?`, from.UnixNano(), to.UnixNano())
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	var waits []int64
	for rows.Next() {
		var name string
		var created int64
		var green *int64
		if err := rows.Scan(&name, &created, &green); err != nil {
			return err
		}
		if excluded(name) {
			continue
		}
		out.Weeks[weekIndex(from, created)].NewTeams++
		out.FirstGreen.TeamsCreated++
		if green == nil {
			continue
		}
		wait := max(0, *green-created)
		waits = append(waits, wait)
		switch d := time.Duration(wait); {
		case d <= time.Hour:
			out.FirstGreen.WithinHour++
		case d <= 24*time.Hour:
			out.FirstGreen.WithinDay++
		case d <= usageWeek:
			out.FirstGreen.WithinWeek++
		default:
			out.FirstGreen.Later++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out.FirstGreen.TeamsGreen = len(waits)
	if len(waits) > 0 {
		slices.Sort(waits)
		out.FirstGreen.MedianSeconds = int64(time.Duration(waits[(len(waits)-1)/2]) / time.Second)
		out.FirstGreen.P90Seconds = int64(time.Duration(waits[(len(waits)*9-1)/10]) / time.Second)
	}
	return nil
}

func (o *Operator) usageNewAccounts(ctx context.Context, from, to time.Time, weeks []UsageWeek) (err error) {
	rows, err := o.s.query(ctx, `
SELECT wk, COUNT(*)
  FROM (SELECT (created_at - ?) / ? AS wk
          FROM accounts
         WHERE created_at >= ? AND created_at < ?) AS weekly
 GROUP BY wk`, from.UnixNano(), int64(usageWeek), from.UnixNano(), to.UnixNano())
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var wk, n int64
		if err := rows.Scan(&wk, &n); err != nil {
			return err
		}
		if wk >= 0 && int(wk) < len(weeks) {
			weeks[wk].NewAccounts += int(n)
		}
	}
	return rows.Err()
}
