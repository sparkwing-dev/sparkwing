package cache

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// BearerPrincipal labels bytes this cache served to a caller holding its
// operator token, and AnonymousPrincipal labels the open dependency proxy's.
// A grant's bytes are charged to its team, under teamPrincipal.
//
// The team is the one caller the cache can tell apart, so the per-team
// daily cap is the one per-principal refusal it makes; the operator token
// and the operator's team stay unbudgeted, because every in-cluster runner
// shares them. The process-wide daily cap is the operator's backstop on this
// pod's bill whoever the callers are.
const BearerPrincipal = "bearer"

var egressMeter *egress.Meter

// safety: the package holds one server's state per process, so a second
// New replaces its configuration; an unchanged budget keeps the counters
// it has rather than restarting the day's total behind the alarm.
func setEgressMeter(cfg egress.Config) {
	if egressMeter != nil && egressMeter.Config() == cfg {
		return
	}
	egressMeter = egress.New(cfg)
}

const teamPrincipalPrefix = "team:"

func teamPrincipal(team string) string { return teamPrincipalPrefix + team }

func cacheEgressPrincipal(r *http.Request) string {
	if team := callerFrom(r).team; team != "" {
		return teamPrincipal(team)
	}
	if bearerToken(r) == "" {
		return egress.AnonymousPrincipal
	}
	return BearerPrincipal
}

// teamDownloads holds what one team's grants may download in a UTC day, by
// the tier teamTiers answers for the team.
var teamDownloads struct {
	free, funded int64
	tiers        *storagequota.Quota
}

func setTeamDownloadCaps(cfg Config) {
	teamDownloads.free, teamDownloads.funded = cfg.TeamDailyDownloadFreeBytes, cfg.TeamDailyDownloadFundedBytes
	teamDownloads.tiers = blobQuota
	if teamDownloads.tiers == nil {
		teamDownloads.tiers = storagequota.New(storagequota.Options{
			Share:  storagequota.CacheShare,
			Used:   func(string) int64 { return 0 },
			Lookup: standingLookup(cfg),
			Exempt: operatorTeam,
		})
	}
	if cfg.GrantKey != "" {
		log.Printf("sparkwing-cache caps what one team downloads in a UTC day at %d bytes free, %d bytes funded (0 means no cap)",
			teamDownloads.free, teamDownloads.funded)
	}
}

// teamDailyCap is what team may download today: the free cap until the
// controller answers that the team pays, and nothing for the operator.
func teamDailyCap(ctx context.Context, team string) egress.DailyCap {
	if operatorTeam(team) || teamDownloads.tiers == nil {
		return egress.DailyCap{}
	}
	if teamDownloads.tiers.Tier(ctx, team) == storagequota.TierFunded {
		return egress.DailyCap{Bytes: teamDownloads.funded, Remedy: "The cap resets at midnight UTC"}
	}
	remedy := "The cap resets at midnight UTC"
	if teamDownloads.funded > teamDownloads.free {
		remedy = fmt.Sprintf("Add credits to the team to raise it to %s a day, or wait for midnight UTC",
			egress.FormatBytes(teamDownloads.funded))
	}
	return egress.DailyCap{Bytes: teamDownloads.free, Remedy: remedy}
}

// metered counts what next sends and refuses it with 429 once the
// process-wide daily cap, or the caller's team's daily cap, is spent.
func metered(class egress.Class, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if egressMeter == nil || egress.Bodyless(r) {
			next(w, r)
			return
		}
		principal := cacheEgressPrincipal(r)
		daily := teamDailyCap(r.Context(), callerFrom(r).team)
		if err := egressMeter.CheckDaily(principal, daily); err != nil {
			writeEgressRefusal(w, err)
			return
		}
		egressMeter.HandleDaily(w, r, principal, class, daily, next)
	}
}

func writeEgressRefusal(w http.ResponseWriter, err error) {
	retryAfter := int64(60)
	var budget *egress.BudgetError
	if errors.As(err, &budget) {
		retryAfter = max(int64(math.Ceil(budget.RetryAfter.Seconds())), 1)
	}
	log.Printf("egress refused: %v", err)
	w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
	http.Error(w, err.Error(), http.StatusTooManyRequests)
}

// safety: the alarm is this pod's bill crossing a daily threshold, which
// no single download can answer for, so it reaches an operator through
// health and a warn line rather than as a refusal.
func egressHealth() (map[string]any, []string) {
	if egressMeter == nil {
		return map[string]any{"enabled": false}, nil
	}
	state := egressMeter.State()
	summary := map[string]any{
		"enabled":            true,
		"enforced":           state.DailyCapBytes > 0,
		"alarm":              state.Alarm,
		"global_day_bytes":   state.GlobalDayBytes,
		"global_month_bytes": state.GlobalMonthBytes,
		"daily_alarm_bytes":  state.DailyAlarmBytes,
		"daily_cap_bytes":    state.DailyCapBytes,
	}
	if !state.Alarm {
		return summary, nil
	}
	return summary, []string{fmt.Sprintf(
		"egress: this cache has sent %s today, at or past the %s daily threshold",
		egress.FormatBytes(state.GlobalDayBytes), egress.FormatBytes(state.DailyAlarmBytes))}
}

func logEgressBudgets(cfg egress.Config) {
	if cfg.GlobalDailyAlarmBytes > 0 {
		log.Printf("sparkwing-cache egress alarm: %d bytes per UTC day; the alarm refuses nothing",
			cfg.GlobalDailyAlarmBytes)
	}
	if cfg.GlobalDailyCapBytes > 0 {
		log.Printf("sparkwing-cache egress daily cap: %d bytes per UTC day; past it every metered download is refused with 429 until the day rolls",
			cfg.GlobalDailyCapBytes)
	}
}

// teamDownloadOther labels the teams outside the ones the team download
// metric names.
const teamDownloadOther = "(other)"

// teamDownloadSeries names the egress.TopConsumers teams that downloaded
// most today and folds the rest into teamDownloadOther, so the metric's
// series count does not grow with the number of teams.
func teamDownloadSeries(day map[string]int64) map[string]int64 {
	type teamBytes struct {
		team  string
		bytes int64
	}
	var teams []teamBytes
	for principal, n := range day {
		if team, ok := strings.CutPrefix(principal, teamPrincipalPrefix); ok {
			teams = append(teams, teamBytes{team, n})
		}
	}
	slices.SortFunc(teams, func(a, b teamBytes) int {
		if c := cmp.Compare(b.bytes, a.bytes); c != 0 {
			return c
		}
		return strings.Compare(a.team, b.team)
	})
	out := make(map[string]int64, egress.TopConsumers+1)
	for i, t := range teams {
		if i < egress.TopConsumers {
			out[t.team] = t.bytes
			continue
		}
		out[teamDownloadOther] += t.bytes
	}
	return out
}

func registerTeamDownloadMetric(meter metric.Meter) error {
	g, err := meter.Int64ObservableGauge("sparkwing.cache.team_download_bytes",
		metric.WithDescription("Bytes this cache served each team's grants today (UTC); the ten largest teams by name, the rest as (other)"),
		metric.WithUnit("By"))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for team, n := range teamDownloadSeries(egressMeter.PrincipalDayBytes()) {
			o.ObserveInt64(g, n, metric.WithAttributes(attribute.String("team", team)))
		}
		return nil
	}, g)
	return err
}
