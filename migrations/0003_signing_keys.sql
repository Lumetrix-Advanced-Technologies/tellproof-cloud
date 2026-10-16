-- 0003: the issuer's signing keys, published as /.well-known/jwks.json (P4-6, D-042).
-- Global and public. A key is added once; it can then be retired (exp set) and
-- revoked (revoked_at set), each once. Nothing else about a key ever changes,
-- and keys are never deleted: old packs must stay verifiable.

BEGIN;

CREATE TABLE signing_keys (
  kid         text PRIMARY KEY CHECK (kid ~ '^[A-Za-z0-9_-]{8,64}$'),   -- RFC 7638 thumbprint
  public_jwk  jsonb NOT NULL,                                          -- {kty, crv, x, y}
  key_ref     text NOT NULL,                                           -- KMS key ARN, or local:<kid>
  nbf         timestamptz NOT NULL DEFAULT now(),
  exp         timestamptz CHECK (exp IS NULL OR exp > nbf),
  revoked_at  timestamptz,
  reason      text CHECK (reason IS NULL OR length(reason) <= 200),
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION signing_keys_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'signing keys are never deleted: old packs must stay verifiable' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF NEW.kid IS DISTINCT FROM OLD.kid OR NEW.public_jwk IS DISTINCT FROM OLD.public_jwk OR NEW.key_ref IS DISTINCT FROM OLD.key_ref
     OR NEW.nbf IS DISTINCT FROM OLD.nbf OR NEW.created_at IS DISTINCT FROM OLD.created_at
     OR (OLD.exp IS NOT NULL AND NEW.exp IS DISTINCT FROM OLD.exp)
     OR (OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.reason IS DISTINCT FROM OLD.reason))
     OR (OLD.revoked_at IS NULL AND NEW.revoked_at IS NULL AND NEW.reason IS DISTINCT FROM OLD.reason) THEN
    RAISE EXCEPTION 'a signing key can only be retired (exp) and revoked (revoked_at, reason), each once' USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER signing_keys_guard BEFORE UPDATE OR DELETE ON signing_keys FOR EACH ROW EXECUTE FUNCTION signing_keys_guard();
CREATE TRIGGER signing_keys_no_truncate BEFORE TRUNCATE ON signing_keys FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

GRANT SELECT ON signing_keys TO plumbline_app;

COMMIT;
