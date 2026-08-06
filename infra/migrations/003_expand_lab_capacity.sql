ALTER TABLE lab_runs DROP CONSTRAINT IF EXISTS lab_runs_simulated_users_check;
ALTER TABLE lab_runs ADD CONSTRAINT lab_runs_simulated_users_check CHECK (simulated_users BETWEEN 2 AND 10000);
