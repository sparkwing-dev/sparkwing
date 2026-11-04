CREATE OR REPLACE VIEW warm_pool_desired AS
WITH jobs AS (
    SELECT c.*, b.settled_at FROM cloud_job_capacity c
    JOIN billing_awards b ON b.team=c.team AND b.reservation_id=c.reservation_id
    WHERE b.unit_kind='node'
), starts AS (
    SELECT requested_class AS class, claim_to_start_ns,
           ROW_NUMBER() OVER (PARTITION BY requested_class ORDER BY claim_to_start_ns) AS position,
           COUNT(*) OVER (PARTITION BY requested_class) AS total
    FROM jobs
    WHERE started_at >= DATABASE_TIMESTAMP - 3600000000000
      AND started_at <= DATABASE_TIMESTAMP AND claim_to_start_ns IS NOT NULL
), medians AS (
    SELECT class, AVG(claim_to_start_ns / 1000000000.0) AS median_seconds
    FROM starts WHERE position IN ((total + 1) / 2, (total + 2) / 2) GROUP BY class
), waits AS (
    SELECT requested_class AS class, (COUNT(*) + 1) / 2 AS desired,
           SUM(CASE WHEN started_at IS NULL AND settled_at IS NULL THEN 1 ELSE 0 END) AS waiting
    FROM jobs WHERE waited_for_fresh_node=1
      AND COALESCE(started_at,claimed_at) >= DATABASE_TIMESTAMP - 1800000000000
      AND COALESCE(started_at,claimed_at) <= DATABASE_TIMESTAMP
    GROUP BY requested_class
), demand AS (
    SELECT s.*, CASE WHEN o.observed_at >= DATABASE_TIMESTAMP - 120000000000
                    THEN COALESCE(o.warm, 0) ELSE 0 END AS warm,
           COALESCE(w.desired, 0) AS wait_desired, COALESCE(w.waiting, 0) AS waiting,
           COALESCE(m.median_seconds, 0) AS median_seconds
    FROM warm_pool_settings s LEFT JOIN warm_pool_observations o ON o.class=s.class
    LEFT JOIN waits w ON w.class=s.class LEFT JOIN medians m ON m.class=s.class
), targets AS (
    SELECT *, CASE WHEN waiting > 0 AND median_seconds > start_time_target_seconds
                   THEN warm + 1 ELSE 0 END AS latency_desired FROM demand
), clamped AS (
    SELECT *, CASE WHEN wait_desired > latency_desired THEN wait_desired ELSE latency_desired END AS auto_desired
    FROM targets
)
SELECT class AS hardware_class, warm, CASE mode WHEN 'off' THEN 0 WHEN 'manual' THEN manual_count
    ELSE CASE WHEN auto_desired < auto_min THEN auto_min
              WHEN auto_desired > auto_max THEN auto_max ELSE auto_desired END END AS desired_replicas
FROM clamped
