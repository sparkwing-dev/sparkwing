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

// safety: an oversized database is a warning an operator reads, not a
// controller that stopped working, so this field never changes the status the
// health route reports.
func (s *Server) storageHealth() map[string]any {
	size, limit, alarm, sampled := s.storage.read()
	if !sampled {
		return map[string]any{"sampled": false}
	}
	out := map[string]any{
		"sampled":     true,
		"total_bytes": size.TotalBytes,
		"sampled_at":  size.SampledAt.UTC().Format(time.RFC3339),
		"alarm":       alarm,
	}
	if limit > 0 {
		out["alarm_bytes"] = limit
	}
	if len(size.Tables) > 0 {
		out["largest_tables"] = size.Tables[:min(len(size.Tables), 5)]
	}
	return out
}

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
	settings, err := s.store.StorageSettings(ctx)
	if err != nil {
		s.logger.Error("storage settings read failed", "err", err)
		return
	}
	if settings.RetentionOn() {
		swept, serr := s.store.SweepRetention(ctx, time.Now().UTC())
		switch {
		case serr != nil:
			s.logger.Error("retention sweep failed", "err", serr)
		case swept.Events > 0 || swept.NodeMetrics > 0:
			s.logger.Info("compacted retained rows",
				"events", swept.Events, "node_metrics", swept.NodeMetrics,
				"event_retention_days", settings.EventRetentionDays,
				"node_metric_retention_days", settings.NodeMetricRetentionDays)
		}
	}
	size, err := s.store.DatabaseSize(ctx)
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

func (s *Server) reportLargestTeams(ctx context.Context, settings store.StorageSettings) {
	if settings.DefaultTier == "" {
		quotas, err := s.store.ListStorageQuotas(ctx)
		if err != nil {
			s.logger.Error("storage quota list failed", "err", err)
			return
		}
		if len(quotas) == 0 {
			return
		}
	}
	month := store.StorageMonth(time.Now().UTC())
	top, err := s.store.TopStorageTeams(ctx, month, storageReportTeams)
	if err != nil {
		s.logger.Error("largest-teams report failed", "err", err)
		return
	}
	for _, team := range top {
		s.logger.Info("storage by team",
			"month", month, "principal", team.Principal,
			"bytes", team.Bytes, "objects", team.Objects, "runs", team.Runs)
	}
}

// safety: the report goes to a log line per team, so it names the teams worth
// reading about rather than every team that stored a byte.
const storageReportTeams = 10

// safety: a refusal answers the request here, so a caller that reads false
// has already had its response written and must not write another.
func (s *Server) reserveStorage(w http.ResponseWriter, r *http.Request, runID string, bytes, objects int64) bool {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || principal == nil || principal.Name == "" {
		return true
	}
	err := s.store.ReserveStorage(r.Context(), principal.Name, runID, bytes, objects, time.Now().UTC())
	if err == nil {
		return true
	}
	if errors.Is(err, store.ErrStorageQuota) {
		s.logger.Warn("storage quota refused a write",
			"principal", principal.Name, "run_id", runID,
			"bytes", bytes, "objects", objects, "reason", err.Error())
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return false
	}
	writeError(w, http.StatusInternalServerError, err)
	return false
}

type storageQuotaJSON struct {
	Principal        string `json:"principal"`
	Tier             string `json:"tier,omitempty"`
	RetentionDays    int64  `json:"retention_days"`
	MaxBytesPerRun   int64  `json:"max_bytes_per_run"`
	MaxBytesPerMonth int64  `json:"max_bytes_per_month"`
	MaxObjectsPerRun int64  `json:"max_objects_per_run"`
}

type storageUsageJSON struct {
	Month        string `json:"month"`
	MonthBytes   int64  `json:"month_bytes"`
	MonthObjects int64  `json:"month_objects"`
}

type storageTeamJSON struct {
	Principal string `json:"principal"`
	Bytes     int64  `json:"bytes"`
	Objects   int64  `json:"objects"`
	Runs      int64  `json:"runs"`
}

type storageSettingsJSON struct {
	EventRetentionDays      int64  `json:"event_retention_days"`
	NodeMetricRetentionDays int64  `json:"node_metric_retention_days"`
	BackupRetentionDays     int64  `json:"backup_retention_days"`
	DatabaseAlarmBytes      int64  `json:"database_alarm_bytes"`
	BackupAlarmBytes        int64  `json:"backup_alarm_bytes"`
	DefaultTier             string `json:"default_tier,omitempty"`
}

type storageStateJSON struct {
	Settings storageSettingsJSON `json:"settings"`
	Quota    storageQuotaJSON    `json:"quota"`
	Usage    storageUsageJSON    `json:"usage"`
	Database *store.DatabaseSize `json:"database,omitempty"`
	Teams    []storageTeamJSON   `json:"largest_teams,omitempty"`
}

func (s *Server) handleStorageShow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.store.StorageSettings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	name := ""
	admin := false
	if p, ok := PrincipalFromContext(ctx); ok && p != nil {
		name, admin = p.Name, p.HasScope(ScopeAdmin)
	}
	quota, err := s.store.StorageQuotaFor(ctx, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	usage, err := s.store.StorageUsageFor(ctx, name, "", now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := storageStateJSON{
		Settings: storageSettingsToJSON(settings),
		Quota:    storageQuotaToJSON(quota),
		Usage: storageUsageJSON{
			Month: usage.Month, MonthBytes: usage.MonthBytes, MonthObjects: usage.MonthObjects,
		},
	}
	if admin {
		if size, _, _, sampled := s.storage.read(); sampled {
			out.Database = &size
		}
		teams, terr := s.store.TopStorageTeams(ctx, store.StorageMonth(now), storageReportTeams)
		if terr != nil {
			writeError(w, http.StatusInternalServerError, terr)
			return
		}
		for _, team := range teams {
			out.Teams = append(out.Teams, storageTeamJSON{
				Principal: team.Principal, Bytes: team.Bytes,
				Objects: team.Objects, Runs: team.Runs,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetStorageSettings(w http.ResponseWriter, r *http.Request) {
	var body storageSettingsJSON
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	settings := store.StorageSettings{
		EventRetentionDays:      body.EventRetentionDays,
		NodeMetricRetentionDays: body.NodeMetricRetentionDays,
		BackupRetentionDays:     body.BackupRetentionDays,
		DatabaseAlarmBytes:      body.DatabaseAlarmBytes,
		BackupAlarmBytes:        body.BackupAlarmBytes,
		DefaultTier:             body.DefaultTier,
	}
	if err := s.store.SetStorageSettings(r.Context(), settings); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("storage settings updated",
		"event_retention_days", settings.EventRetentionDays,
		"node_metric_retention_days", settings.NodeMetricRetentionDays,
		"backup_retention_days", settings.BackupRetentionDays,
		"default_tier", settings.DefaultTier)
	writeJSON(w, http.StatusOK, storageSettingsToJSON(settings))
}

func (s *Server) handleSetStorageQuota(w http.ResponseWriter, r *http.Request) {
	principal := r.PathValue("principal")
	if principal == "" {
		writeError(w, http.StatusBadRequest, errors.New("principal required"))
		return
	}
	var body storageQuotaJSON
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	quota := store.StorageQuota{
		Principal:        principal,
		Tier:             body.Tier,
		RetentionDays:    body.RetentionDays,
		MaxBytesPerRun:   body.MaxBytesPerRun,
		MaxBytesPerMonth: body.MaxBytesPerMonth,
		MaxObjectsPerRun: body.MaxObjectsPerRun,
	}
	if err := s.store.SetStorageQuota(r.Context(), quota); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stored, err := s.store.StorageQuotaFor(r.Context(), principal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("storage quota updated", "principal", principal, "tier", stored.Tier,
		"max_bytes_per_run", stored.MaxBytesPerRun, "max_bytes_per_month", stored.MaxBytesPerMonth,
		"max_objects_per_run", stored.MaxObjectsPerRun)
	writeJSON(w, http.StatusOK, storageQuotaToJSON(stored))
}

func storageSettingsToJSON(in store.StorageSettings) storageSettingsJSON {
	return storageSettingsJSON{
		EventRetentionDays:      in.EventRetentionDays,
		NodeMetricRetentionDays: in.NodeMetricRetentionDays,
		BackupRetentionDays:     in.BackupRetentionDays,
		DatabaseAlarmBytes:      in.DatabaseAlarmBytes,
		BackupAlarmBytes:        in.BackupAlarmBytes,
		DefaultTier:             in.DefaultTier,
	}
}

func storageQuotaToJSON(in store.StorageQuota) storageQuotaJSON {
	return storageQuotaJSON{
		Principal:        in.Principal,
		Tier:             in.Tier,
		RetentionDays:    in.RetentionDays,
		MaxBytesPerRun:   in.MaxBytesPerRun,
		MaxBytesPerMonth: in.MaxBytesPerMonth,
		MaxObjectsPerRun: in.MaxObjectsPerRun,
	}
}
