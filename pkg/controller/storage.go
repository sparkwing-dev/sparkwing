package controller

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StorageMaintenanceInterval is how often the controller compacts retained
// rows and resamples the database size. Both are timer work: a size probe
// walks the whole database and a sweep deletes, so neither belongs on a
// request path.
const StorageMaintenanceInterval = time.Hour

// safety: the report goes to a log line per team, so it names the teams worth
// reading about rather than every team that stored a byte.
const storageReportTeams = 10

// safety: the health route reads the last size probe rather than taking one,
// because a size walk on a request path is what this field exists to avoid.
type storageSample struct {
	mu      sync.RWMutex
	size    store.DatabaseSize
	alarm   bool
	limit   int64
	sampled bool
}

func (s *storageSample) set(size store.DatabaseSize, limit int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size, s.limit, s.sampled = size, limit, true
	s.alarm = limit > 0 && size.TotalBytes > limit
}

func (s *storageSample) read() (store.DatabaseSize, int64, bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.size, s.limit, s.alarm, s.sampled
}

// safety: the health route answers without a token, so it says only whether
// the alarm stands; sizes and table names go to the admin storage route.
func (s *Server) storageHealth() map[string]any {
	_, _, alarm, sampled := s.storage.read()
	return map[string]any{"sampled": sampled, "alarm": alarm}
}

// MaintainStorage runs one compaction and size-sample pass immediately.
// ServeWith keeps this on a timer; a process that serves Handler directly
// owns no timer of its own and calls this to keep the sample fresh.
func (s *Server) MaintainStorage(ctx context.Context) { s.maintainStorage(ctx) }

// safety: compaction deletes and a size probe walks the database, so both run
// on this timer and neither runs on a request.
func (s *Server) runStorageMaintenance(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = StorageMaintenanceInterval
	}
	s.maintainStorage(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.maintainStorage(ctx)
		}
	}
}

func (s *Server) maintainStorage(ctx context.Context) {
	s.seedCloudRetention(ctx)
	settings, err := s.store.AsOperator().StorageSettings(ctx)
	if err != nil {
		s.logger.Error("storage settings read failed", "err", err)
		return
	}
	s.maintainRetainedData(ctx)
	size, err := s.store.AsOperator().DatabaseSize(ctx)
	if err != nil {
		s.logger.Error("database size probe failed", "err", err)
		return
	}
	s.storage.set(size, settings.DatabaseAlarmBytes)
	if settings.DatabaseAlarmBytes > 0 && size.TotalBytes > settings.DatabaseAlarmBytes {
		s.logger.Warn("database is larger than its alarm size",
			"total_bytes", size.TotalBytes, "alarm_bytes", settings.DatabaseAlarmBytes,
			"retention_on", settings.RetentionOn())
	}
	s.reportLargestTeams(ctx, settings)
}

func (s *Server) maintainRetainedData(ctx context.Context) {
	operator := s.store.AsOperator()
	lease, err := operator.AcquireSingletonLease(ctx, "maintenance", s.measureHolder(), StorageMaintenanceInterval)
	if errors.Is(err, store.ErrLockHeld) {
		return
	}
	if err != nil {
		s.logger.Error("storage maintenance lease failed", "err", err)
		return
	}
	if _, err := operator.PruneEgressUsage(ctx, lease); err != nil {
		s.logger.Error("egress usage prune failed", "err", err)
		return
	}
	retained, err := operator.MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 5000})
	if err != nil {
		s.logger.Error("storage retention failed", "err", err)
		return
	}
	if retained.Runs > 0 || retained.Metrics > 0 || retained.Records > 0 || retained.Invitations > 0 || retained.Sessions > 0 {
		s.logger.Info("expired retained data", "runs", retained.Runs, "metrics", retained.Metrics, "records", retained.Records, "invitations", retained.Invitations, "sessions", retained.Sessions)
	}
	if _, err := operator.SealAbandonedLogStreams(ctx, lease, 5000); err != nil {
		s.logger.Error("seal abandoned log streams failed", "err", err)
		return
	}
	if !s.Metering() {
		return
	}
	billed, err := operator.ChargeRetainedStorage(ctx, lease)
	if err != nil {
		s.logger.Error("charging retained storage failed", "err", err)
		return
	}
	for _, charge := range billed.Charges {
		s.logger.Info("charged retained storage",
			"team", charge.Team, "bytes", charge.StorageBytes,
			"seconds", charge.Seconds, "amount_micro", charge.AmountMicro)
	}
}

func (s *Server) reportLargestTeams(ctx context.Context, settings store.StorageSettings) {
	if settings.DefaultTier == "" {
		quotas, err := s.store.AsOperator().ListStorageQuotas(ctx)
		if err != nil {
			s.logger.Error("storage quota list failed", "err", err)
			return
		}
		if len(quotas) == 0 {
			return
		}
	}
	month := store.StorageMonth(time.Now().UTC())
	top, err := s.store.AsOperator().TopStorageTeams(ctx, month, storageReportTeams)
	if err != nil {
		s.logger.Error("largest-teams report failed", "err", err)
		return
	}
	for _, team := range top {
		s.logger.Info("storage by team",
			"month", month, "team", team.Team,
			"bytes", team.Bytes, "objects", team.Objects)
	}
}

// safety: the charge rides inside the store call's own transaction, so this
// only translates the refusal; nothing here may write on its own.
func writeStorageWriteRefusal(w http.ResponseWriter, logger interface{ Warn(string, ...any) }, err error) bool {
	switch {
	case errors.Is(err, store.ErrStorageQuota):
		logger.Warn("storage quota refused a write", "reason", err.Error())
		writeError(w, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, store.ErrInsufficientCredits):
		logger.Warn("a spent balance refused a write that would store more", "reason", err.Error())
		writeError(w, http.StatusPaymentRequired, err)
	default:
		return false
	}
	return true
}

// safety: the team a write is charged to comes from the authenticated
// principal and never from the request, so no caller can spend another's
// allowance. A signed-up team names its own principals, so two teams can both
// hold "agent:eddie"; its writes are charged to "team:<slug>" instead. The
// operator's team keeps one account per principal, as a self-hosted install
// always has.
func chargedPrincipal(r *http.Request) string {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil {
		return ""
	}
	if team := store.NormalizeTeam(p.Team); team != "" && team != store.DefaultTeam {
		return "team:" + string(team)
	}
	return p.Name
}

type storageQuotaJSON struct {
	Team                  store.Team `json:"team"`
	Tier                  string     `json:"tier,omitempty"`
	MaxBytesPerRun        int64      `json:"max_bytes_per_run"`
	MaxBytesPerMonth      int64      `json:"max_bytes_per_month"`
	MaxObjectsPerRun      int64      `json:"max_objects_per_run"`
	StorageAllowanceBytes int64      `json:"storage_allowance_bytes"`
}

type storageAllowanceJSON struct {
	StorageAllowanceBytes int64 `json:"storage_allowance_bytes"`
}

// safety: the quota route replaces every limit the body leaves out, so the
// allowance is not one of the fields it takes; its own route is the only
// writer and a quota rewrite cannot drop what a team asked to keep.
type setStorageQuotaJSON struct {
	Tier             string `json:"tier,omitempty"`
	MaxBytesPerRun   int64  `json:"max_bytes_per_run"`
	MaxBytesPerMonth int64  `json:"max_bytes_per_month"`
	MaxObjectsPerRun int64  `json:"max_objects_per_run"`
}

type storageUsageJSON struct {
	Month        string `json:"month"`
	MonthBytes   int64  `json:"month_bytes"`
	MonthObjects int64  `json:"month_objects"`
	// safety: historical usage survives deletion, so retained bytes come from committed inventory.
	RetainedBytes int64 `json:"retained_bytes"`
}

type storageTeamJSON struct {
	Team    store.Team `json:"team"`
	Bytes   int64      `json:"bytes"`
	Objects int64      `json:"objects"`
}

type storageSettingsJSON struct {
	RunRetentionDays        int64  `json:"run_retention_days"`
	NodeMetricRetentionDays int64  `json:"node_metric_retention_days"`
	DatabaseAlarmBytes      int64  `json:"database_alarm_bytes"`
	DefaultTier             string `json:"default_tier,omitempty"`
}

type storageStateJSON struct {
	Settings storageSettingsJSON `json:"settings"`
	Quota    storageQuotaJSON    `json:"quota"`
	Usage    storageUsageJSON    `json:"usage"`
	Database *store.DatabaseSize `json:"database,omitempty"`
	Alarm    bool                `json:"alarm"`
	Quotas   []storageQuotaJSON  `json:"quotas,omitempty"`
	Teams    []storageTeamJSON   `json:"largest_teams,omitempty"`
	Team     *teamStandingJSON   `json:"team,omitempty"`
}

func (s *Server) handleStorageShow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.store.AsOperator().StorageSettings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	admin := false
	if p, ok := PrincipalFromContext(ctx); ok && p != nil {
		admin = p.HasScope(ScopeAdmin)
	}
	quota, err := tenant.StorageQuota(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	month := store.StorageMonth(time.Now().UTC())
	usage, err := tenant.StorageUsageFor(ctx, "", month)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	retained, err := tenant.StorageRetainedBytes(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	size, _, alarm, sampled := s.storage.read()
	out := storageStateJSON{
		Settings: storageSettingsToJSON(settings),
		Quota:    storageQuotaToJSON(quota),
		Usage: storageUsageJSON{
			Month: usage.Month, MonthBytes: usage.MonthBytes, MonthObjects: usage.MonthObjects,
			RetainedBytes: retained,
		},
		Alarm: alarm,
	}
	if out.Team, err = s.callerStorageStanding(r); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if admin {
		if sampled {
			out.Database = &size
		}
		if err := s.appendAdminStorage(ctx, &out, month); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) appendAdminStorage(ctx context.Context, out *storageStateJSON, month string) error {
	quotas, err := s.store.AsOperator().ListStorageQuotas(ctx)
	if err != nil {
		return err
	}
	for _, q := range quotas {
		out.Quotas = append(out.Quotas, storageQuotaToJSON(q))
	}
	teams, err := s.store.AsOperator().TopStorageTeams(ctx, month, storageReportTeams)
	if err != nil {
		return err
	}
	for _, team := range teams {
		out.Teams = append(out.Teams, storageTeamJSON{
			Team: team.Team, Bytes: team.Bytes, Objects: team.Objects,
		})
	}
	return nil
}

func (s *Server) handleSetStorageSettings(w http.ResponseWriter, r *http.Request) {
	var body storageSettingsJSON
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	settings := store.StorageSettings{
		RunRetentionDays:        body.RunRetentionDays,
		NodeMetricRetentionDays: body.NodeMetricRetentionDays,
		DatabaseAlarmBytes:      body.DatabaseAlarmBytes,
		DefaultTier:             body.DefaultTier,
	}
	if err := s.store.AsOperator().SetStorageSettings(r.Context(), settings); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("storage settings updated",
		"run_retention_days", settings.RunRetentionDays,
		"node_metric_retention_days", settings.NodeMetricRetentionDays,
		"database_alarm_bytes", settings.DatabaseAlarmBytes,
		"default_tier", settings.DefaultTier)
	writeJSON(w, http.StatusOK, storageSettingsToJSON(settings))
}

func (s *Server) handleSetStorageQuota(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	var body setStorageQuotaJSON
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	quota := store.StorageQuota{
		Tier:             body.Tier,
		MaxBytesPerRun:   body.MaxBytesPerRun,
		MaxBytesPerMonth: body.MaxBytesPerMonth,
		MaxObjectsPerRun: body.MaxObjectsPerRun,
	}
	if err := tenant.SetStorageQuota(r.Context(), quota); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stored, err := tenant.StorageQuota(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("storage quota updated", "team", tenant.Team(), "tier", stored.Tier,
		"max_bytes_per_run", stored.MaxBytesPerRun, "max_bytes_per_month", stored.MaxBytesPerMonth,
		"max_objects_per_run", stored.MaxObjectsPerRun,
		"storage_allowance_bytes", stored.AllowanceBytes)
	writeJSON(w, http.StatusOK, storageQuotaToJSON(stored))
}

func storageSettingsToJSON(in store.StorageSettings) storageSettingsJSON {
	return storageSettingsJSON{
		RunRetentionDays:        in.RunRetentionDays,
		NodeMetricRetentionDays: in.NodeMetricRetentionDays,
		DatabaseAlarmBytes:      in.DatabaseAlarmBytes,
		DefaultTier:             in.DefaultTier,
	}
}

func storageQuotaToJSON(in store.StorageQuota) storageQuotaJSON {
	return storageQuotaJSON{
		Team:                  in.Team,
		Tier:                  in.Tier,
		MaxBytesPerRun:        in.MaxBytesPerRun,
		MaxBytesPerMonth:      in.MaxBytesPerMonth,
		MaxObjectsPerRun:      in.MaxObjectsPerRun,
		StorageAllowanceBytes: in.AllowanceBytes,
	}
}

// safety: independent writes keep quota replacements from discarding the retained allowance.
func (s *Server) handleSetStorageAllowance(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	var body storageAllowanceJSON
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stored, err := tenant.SetStorageAllowance(r.Context(), body.StorageAllowanceBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("storage allowance updated",
		"team", tenant.Team(), "storage_allowance_bytes", stored.AllowanceBytes)
	writeJSON(w, http.StatusOK, storageQuotaToJSON(stored))
}
