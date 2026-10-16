-- 0007: the Vanta integration (P6-1, D-049). A vendor pastes the client ID
-- and secret of their own Vanta private integration in Settings. The
-- credentials, and the share-link tokens the sync puts in Vanta, are sealed
-- with AES-256-GCM under the server's INTEGRATION_KEY and stored in
-- integration_secrets; integrations.secret_ref names that row and never holds
-- the secret. vanta_uploads records which packs were uploaded as documents, so
-- each is sent once.

BEGIN;

CREATE TABLE integration_secrets (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  key_id      text NOT NULL,                        -- which INTEGRATION_KEY sealed it (a digest, never the key)
  ciphertext  bytea NOT NULL,                       -- nonce || AES-256-GCM sealed JSON
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE vanta_uploads (
  pack_id      uuid PRIMARY KEY REFERENCES packs(id),
  tenant_id    uuid NOT NULL REFERENCES tenants(id),
  uploaded_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE integration_secrets ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_secrets USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
ALTER TABLE vanta_uploads ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON vanta_uploads USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

-- The hourly sync runs before any tenant is set: it lists the tenants to visit
-- and then works inside each tenant's scope.
CREATE OR REPLACE FUNCTION vanta_tenants()
RETURNS TABLE (tenant_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT i.tenant_id FROM integrations i
  WHERE i.kind = 'vanta' AND coalesce((i.config->>'enabled')::boolean, false)
  ORDER BY i.last_sync_at NULLS FIRST, i.tenant_id
$$;

REVOKE ALL ON FUNCTION vanta_tenants() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION vanta_tenants() TO plumbline_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON integration_secrets TO plumbline_app;
GRANT SELECT, INSERT ON vanta_uploads TO plumbline_app;

COMMIT;
