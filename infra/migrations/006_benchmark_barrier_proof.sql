ALTER TABLE benchmark_jobs ADD COLUMN IF NOT EXISTS barrier_misses bigint NOT NULL DEFAULT 0 CHECK (barrier_misses >= 0);
