package store

import "context"

func migrateNullableSustainedCPU(ctx context.Context, tx *storeTx) error {
	for _, statement := range []string{
		`ALTER TABLE pipeline_profiles DROP COLUMN sustained_cores`,
		`ALTER TABLE pipeline_profiles DROP COLUMN prev_sustained_cores`,
		`ALTER TABLE pipeline_profiles ADD COLUMN sustained_cores REAL`,
		`ALTER TABLE pipeline_profiles ADD COLUMN prev_sustained_cores REAL`,
		`UPDATE pipeline_profiles SET p50_duration_ms=0, p99_duration_ms=0,
 peak_cores=0, peak_memory_bytes=0, sample_count=0, cpu_measured=0, samples_json=NULL,
 floor_cores=0, floor_memory_bytes=0, prev_peak_cores=0, prev_peak_memory_bytes=0`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
