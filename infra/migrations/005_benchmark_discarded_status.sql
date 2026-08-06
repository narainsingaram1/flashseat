ALTER TABLE benchmark_jobs DROP CONSTRAINT IF EXISTS benchmark_jobs_status_check;
ALTER TABLE benchmark_jobs ADD CONSTRAINT benchmark_jobs_status_check
  CHECK (status IN ('queued','running','succeeded','failed','discarded'));
