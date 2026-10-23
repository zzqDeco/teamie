CREATE TABLE IF NOT EXISTS runtime_context_recoveries (
 run_id uuid NOT NULL REFERENCES runtime_runs(id),
 model_step integer NOT NULL CHECK(model_step > 0),
 context_profile text NOT NULL CHECK(context_profile = 'teamie-context-v038'),
 request_hash text NOT NULL,
 streaming boolean NOT NULL,
 error_code text NOT NULL CHECK(error_code IN ('context_overflow','request_too_large')),
 decision_version integer NOT NULL CHECK(decision_version = 1),
 decision_json jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,model_step)
);
