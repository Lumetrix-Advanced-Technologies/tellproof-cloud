-- 0002: what a production pack needs beyond the agent's batches (P4-5, D-041).
-- Registers are evidence inputs and append-only: a correction or a later
-- acknowledgement is a new row (for incidents, the newest row per incident_ref wins).

BEGIN;

-- ---------------------------------------------------------------- the system's pack profile
ALTER TABLE ai_systems
  ADD COLUMN timezone    text  NOT NULL DEFAULT 'Australia/Sydney',  -- periods are whole local days
  ADD COLUMN profiles    jsonb NOT NULL DEFAULT '[]'::jsonb,         -- review profiles the pack answers
  ADD COLUMN providers   jsonb NOT NULL DEFAULT '{}'::jsonb,         -- display names by provider id
  ADD COLUMN limitations jsonb NOT NULL DEFAULT '[]'::jsonb,         -- the vendor's own stated limits
  ADD COLUMN pack_prefix text  NOT NULL DEFAULT 'PL' CHECK (pack_prefix ~ '^[A-Z0-9]{2,12}$');

-- ---------------------------------------------------------------- registers (append-only)
CREATE TABLE register_changes (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  ai_system_id  uuid NOT NULL REFERENCES ai_systems(id),
  change_date   date NOT NULL,                       -- local date in the system's timezone
  kind          text NOT NULL CHECK (kind IN ('model', 'prompt', 'guardrail', 'config', 'provider')),
  what          text NOT NULL CHECK (length(what) BETWEEN 1 AND 500),
  recorded_by   uuid REFERENCES members(id),
  recorded_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX register_changes_system ON register_changes (ai_system_id, change_date);

CREATE TABLE register_incidents (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  ai_system_id  uuid NOT NULL REFERENCES ai_systems(id),
  incident_ref  text NOT NULL CHECK (incident_ref ~ '^[A-Za-z0-9._-]{1,40}$'),
  opened        timestamptz NOT NULL,
  acknowledged  timestamptz CHECK (acknowledged IS NULL OR acknowledged >= opened),
  resolved      timestamptz CHECK (resolved IS NULL OR resolved >= opened),
  summary       text NOT NULL CHECK (length(summary) BETWEEN 1 AND 500),
  recorded_by   uuid REFERENCES members(id),
  recorded_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX register_incidents_system ON register_incidents (ai_system_id, opened);

CREATE TABLE register_evals (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id       uuid NOT NULL REFERENCES tenants(id),
  ai_system_id    uuid NOT NULL REFERENCES ai_systems(id),
  suite           text NOT NULL CHECK (length(suite) BETWEEN 1 AND 128),
  model           text NOT NULL CHECK (length(model) BETWEEN 1 AND 128),
  prompt_version  text NOT NULL DEFAULT '' CHECK (length(prompt_version) <= 128),
  ran_at          timestamptz NOT NULL,
  pass_rate       double precision NOT NULL CHECK (pass_rate >= 0 AND pass_rate <= 1),
  recorded_by     uuid REFERENCES members(id),
  recorded_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX register_evals_system ON register_evals (ai_system_id, ran_at);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['register_changes', 'register_incidents', 'register_evals']
  LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION forbid_mutation()', t || '_append_only', t);
    EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation()', t || '_no_truncate', t);
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', t);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO plumbline_app', t);
  END LOOP;
END $$;

COMMIT;
