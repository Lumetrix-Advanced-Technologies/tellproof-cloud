-- 0001_init.sql: Plumbline initial schema (Postgres 16 / Supabase).
-- Immutable once merged. Change the schema with a new numbered file.
-- Evidence tables are append-only (trigger forbid_mutation). Tenant tables use RLS keyed on app.tenant_id.

BEGIN;

-- ---------------------------------------------------------------- roles
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'plumbline_app') THEN
    CREATE ROLE plumbline_app NOLOGIN NOBYPASSRLS;  -- ops grants LOGIN and a password out of band
  END IF;
END $$;

-- ---------------------------------------------------------------- helpers
CREATE OR REPLACE FUNCTION forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% on %.% is not allowed: evidence is append-only', TG_OP, TG_TABLE_SCHEMA, TG_TABLE_NAME
    USING ERRCODE = 'insufficient_privilege';
END $$;

CREATE OR REPLACE FUNCTION current_tenant() RETURNS uuid LANGUAGE sql STABLE AS $$
  SELECT nullif(current_setting('app.tenant_id', true), '')::uuid
$$;

-- ---------------------------------------------------------------- tenants and people
CREATE TABLE tenants (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL,
  plan        text NOT NULL DEFAULT 'pilot' CHECK (plan IN ('pilot', 'founding', 'growth', 'scale')),
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE members (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  user_id     uuid NOT NULL,                       -- Supabase Auth subject
  email       text NOT NULL,
  role        text NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'member')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, user_id)
);

-- ---------------------------------------------------------------- AI systems and agents
CREATE TABLE ai_systems (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id),
  name             text NOT NULL,
  environment      text NOT NULL DEFAULT 'production',
  description      text NOT NULL DEFAULT '',
  declared_models  jsonb NOT NULL DEFAULT '[]'::jsonb,
  declared_regions jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name, environment)
);

CREATE TABLE enrolment_tokens (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  ai_system_id  uuid NOT NULL REFERENCES ai_systems(id),
  token_hash    bytea NOT NULL UNIQUE,              -- sha256 of the one-time token
  expires_at    timestamptz NOT NULL,
  used_at       timestamptz
);

CREATE TABLE agents (
  id            text PRIMARY KEY CHECK (id ~ '^agt_[A-Za-z0-9]{8,40}$'),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  ai_system_id  uuid NOT NULL REFERENCES ai_systems(id),
  public_key    bytea NOT NULL CHECK (length(public_key) = 32),   -- Ed25519
  hostname      text NOT NULL DEFAULT '',
  enrolled_at   timestamptz NOT NULL DEFAULT now(),
  last_seq      bigint NOT NULL DEFAULT 0,
  last_digest   text,
  revoked_at    timestamptz
);

-- ---------------------------------------------------------------- evidence (append-only)
CREATE TABLE evidence_batches (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  agent_id      text NOT NULL REFERENCES agents(id),
  seq           bigint NOT NULL CHECK (seq >= 1),
  prev_digest   text,
  digest        text NOT NULL UNIQUE CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
  window_start  timestamptz NOT NULL,
  window_end    timestamptz NOT NULL CHECK (window_end > window_start),
  payload       jsonb NOT NULL,                     -- the verified batch payload (aggregates only)
  envelope      jsonb NOT NULL,                     -- the DSSE envelope as received
  received_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (agent_id, seq)
);
CREATE INDEX evidence_batches_window ON evidence_batches (tenant_id, window_start);

CREATE TABLE objective_defs (                        -- global: loaded from spec/objectives.json
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  eo_id      text NOT NULL CHECK (eo_id ~ '^EO-[A-Z]+-[0-9]{2}$'),
  version    int  NOT NULL,
  definition jsonb NOT NULL,
  hash       text NOT NULL,
  loaded_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (eo_id, version)
);

CREATE TABLE framework_refs (                        -- global: loaded from spec/framework-refs.json
  id        text PRIMARY KEY,
  framework text NOT NULL,
  ref       text NOT NULL,
  title     text NOT NULL,                           -- our own short title, never licensed text
  verified  boolean NOT NULL DEFAULT false
);

CREATE TABLE evaluations (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id),
  ai_system_id        uuid NOT NULL REFERENCES ai_systems(id),
  period_start        timestamptz NOT NULL,
  period_end          timestamptz NOT NULL CHECK (period_end > period_start),
  objective_set_hash  text NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE objective_results (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id      uuid NOT NULL REFERENCES tenants(id),
  evaluation_id  uuid NOT NULL REFERENCES evaluations(id),
  eo_id          text NOT NULL,
  eo_version     int  NOT NULL,
  status         text NOT NULL CHECK (status IN ('pass', 'attention', 'fail')),
  measured       jsonb NOT NULL,
  batch_digests  text[] NOT NULL DEFAULT '{}',
  UNIQUE (evaluation_id, eo_id)
);

CREATE TABLE packs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  ai_system_id  uuid NOT NULL REFERENCES ai_systems(id),
  evaluation_id uuid NOT NULL REFERENCES evaluations(id),
  pack_id       text NOT NULL,
  seq_no        int  NOT NULL CHECK (seq_no >= 1),
  prev_digest   text CHECK (prev_digest IS NULL OR prev_digest ~ '^sha256:[0-9a-f]{64}$'),
  digest        text NOT NULL UNIQUE CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
  envelope      jsonb NOT NULL,
  kms_key_id    text NOT NULL,
  issued_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ai_system_id, seq_no),
  CHECK ((seq_no = 1) = (prev_digest IS NULL))
);

CREATE TABLE checkpoints (                           -- global and public
  day         date PRIMARY KEY,
  root        text NOT NULL CHECK (root ~ '^[0-9a-f]{64}$'),
  pack_count  int  NOT NULL,
  envelope    jsonb NOT NULL,
  git_commit  text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- sharing
CREATE TABLE share_links (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  pack_id       uuid REFERENCES packs(id),           -- a pinned pack, or
  ai_system_id  uuid REFERENCES ai_systems(id),      -- "latest pack for this system"
  token_hash    bytea NOT NULL UNIQUE,               -- sha256 of the 16-byte token; the token is shown once
  verifier_org  text NOT NULL,
  expires_at    timestamptz NOT NULL,
  revoked_at    timestamptz,
  created_by    uuid NOT NULL REFERENCES members(id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  CHECK ((pack_id IS NULL) <> (ai_system_id IS NULL))
);

CREATE TABLE verifier_views (
  id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id          uuid NOT NULL REFERENCES tenants(id),
  share_link_id      uuid NOT NULL REFERENCES share_links(id),
  pack_digest        text NOT NULL,
  viewed_at          timestamptz NOT NULL DEFAULT now(),
  ip_hash            bytea,                          -- sha256(daily salt || ip), never the raw IP
  user_agent_family  text
);
CREATE INDEX verifier_views_link ON verifier_views (share_link_id, viewed_at);

-- ---------------------------------------------------------------- integrations and jobs
CREATE TABLE integrations (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  kind          text NOT NULL CHECK (kind IN ('vanta')),
  secret_ref    text NOT NULL,                       -- name of the secret in Fly secrets or Vault, never the secret
  config        jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_sync_at  timestamptz,
  last_error    text,
  UNIQUE (tenant_id, kind)
);

CREATE TABLE jobs (                                  -- global work queue, claimed with FOR UPDATE SKIP LOCKED
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind        text NOT NULL CHECK (kind IN ('evaluate', 'issue_pack', 'checkpoint', 'vanta_sync', 'prune_jobs')),
  tenant_id   uuid REFERENCES tenants(id),
  payload     jsonb NOT NULL DEFAULT '{}'::jsonb,
  run_at      timestamptz NOT NULL DEFAULT now(),
  locked_at   timestamptz,
  locked_by   text,
  attempts    int NOT NULL DEFAULT 0,
  last_error  text,
  done_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_ready ON jobs (run_at) WHERE done_at IS NULL AND locked_at IS NULL;

-- ---------------------------------------------------------------- append-only triggers
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['evidence_batches', 'objective_defs', 'evaluations', 'objective_results', 'packs', 'checkpoints', 'verifier_views']
  LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION forbid_mutation()', t || '_append_only', t);
    EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation()', t || '_no_truncate', t);
  END LOOP;
END $$;

-- ---------------------------------------------------------------- row-level security
-- Not FORCEd: the table owner (migrations, SECURITY DEFINER lookups) bypasses RLS; plumbline_app does not.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['members', 'ai_systems', 'enrolment_tokens', 'agents', 'evidence_batches', 'evaluations',
                           'objective_results', 'packs', 'share_links', 'verifier_views', 'integrations']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', t);
  END LOOP;
END $$;
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants USING (id = current_tenant()) WITH CHECK (id = current_tenant());

-- ---------------------------------------------------------------- lookups that happen before the tenant is known
-- These run as the function owner and return only what the caller needs to continue.

CREATE OR REPLACE FUNCTION agent_lookup(p_agent_id text)
RETURNS TABLE (tenant_id uuid, ai_system_id uuid, public_key bytea, last_seq bigint, last_digest text, revoked boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT a.tenant_id, a.ai_system_id, a.public_key, a.last_seq, a.last_digest, a.revoked_at IS NOT NULL
  FROM agents a WHERE a.id = p_agent_id
$$;

CREATE OR REPLACE FUNCTION enrolment_claim(p_token_hash bytea)
RETURNS TABLE (tenant_id uuid, ai_system_id uuid)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = public AS $$
  UPDATE enrolment_tokens SET used_at = now()
  WHERE token_hash = p_token_hash AND used_at IS NULL AND expires_at > now()
  RETURNING tenant_id, ai_system_id
$$;

CREATE OR REPLACE FUNCTION member_lookup(p_user_id uuid)
RETURNS TABLE (member_id uuid, tenant_id uuid, role text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT m.id, m.tenant_id, m.role FROM members m WHERE m.user_id = p_user_id
$$;

-- Public verify: resolves a share token to an envelope, logs the view, returns the previous pack's digest for the chain check.
CREATE OR REPLACE FUNCTION verify_fetch(p_token_hash bytea, p_ip_hash bytea, p_ua_family text)
RETURNS TABLE (envelope jsonb, digest text, prev_digest text, seq_no int, pack_id text, verifier_org text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = public AS $$
DECLARE l share_links; p packs;
BEGIN
  SELECT * INTO l FROM share_links
  WHERE token_hash = p_token_hash AND revoked_at IS NULL AND expires_at > now();
  IF NOT FOUND THEN RETURN; END IF;

  IF l.pack_id IS NOT NULL THEN
    SELECT * INTO p FROM packs WHERE id = l.pack_id;
  ELSE
    SELECT * INTO p FROM packs WHERE ai_system_id = l.ai_system_id ORDER BY seq_no DESC LIMIT 1;
  END IF;
  IF NOT FOUND THEN RETURN; END IF;

  INSERT INTO verifier_views (tenant_id, share_link_id, pack_digest, ip_hash, user_agent_family)
  VALUES (l.tenant_id, l.id, p.digest, p_ip_hash, p_ua_family);

  RETURN QUERY SELECT p.envelope, p.digest, p.prev_digest, p.seq_no, p.pack_id, l.verifier_org;
END $$;

REVOKE ALL ON FUNCTION agent_lookup(text), enrolment_claim(bytea), member_lookup(uuid), verify_fetch(bytea, bytea, text) FROM PUBLIC;

-- ---------------------------------------------------------------- grants for the app role
GRANT USAGE ON SCHEMA public TO plumbline_app;
GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO plumbline_app;
GRANT UPDATE ON tenants, members, ai_systems, enrolment_tokens, agents, share_links, integrations, jobs TO plumbline_app;
GRANT DELETE ON jobs, enrolment_tokens TO plumbline_app;
GRANT EXECUTE ON FUNCTION agent_lookup(text), enrolment_claim(bytea), member_lookup(uuid), verify_fetch(bytea, bytea, text), current_tenant() TO plumbline_app;

COMMIT;
