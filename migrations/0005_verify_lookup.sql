-- 0005: the public verify API's lookup (P5-1, D-044). Unlike 0001's
-- verify_fetch it tells an unknown link (no row) from a revoked or expired one
-- (a status), and it returns the digest the issuer stored for the previous
-- pack in the chain, so the verify page's chain check compares the pack's
-- claim with the issuer's record rather than with itself. A view is logged
-- only when a pack is returned.

BEGIN;

CREATE OR REPLACE FUNCTION verify_lookup(p_token_hash bytea, p_ip_hash bytea, p_ua_family text)
RETURNS TABLE (status text, envelope jsonb, digest text, prev_envelope_digest text, seq_no int, pack_id text,
               verifier_org text, expires_at timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = public AS $$
DECLARE l share_links; p packs; prev text;
BEGIN
  SELECT * INTO l FROM share_links WHERE token_hash = p_token_hash;
  IF NOT FOUND THEN RETURN; END IF;
  IF l.revoked_at IS NOT NULL THEN
    RETURN QUERY SELECT 'revoked'::text, NULL::jsonb, NULL::text, NULL::text, NULL::int, NULL::text, l.verifier_org, l.expires_at; RETURN;
  END IF;
  IF l.expires_at <= now() THEN
    RETURN QUERY SELECT 'expired'::text, NULL::jsonb, NULL::text, NULL::text, NULL::int, NULL::text, l.verifier_org, l.expires_at; RETURN;
  END IF;

  IF l.pack_id IS NOT NULL THEN
    SELECT * INTO p FROM packs WHERE id = l.pack_id;
  ELSE
    SELECT * INTO p FROM packs WHERE ai_system_id = l.ai_system_id ORDER BY seq_no DESC LIMIT 1;
  END IF;
  IF NOT FOUND THEN
    RETURN QUERY SELECT 'no_pack'::text, NULL::jsonb, NULL::text, NULL::text, NULL::int, NULL::text, l.verifier_org, l.expires_at; RETURN;
  END IF;
  SELECT q.digest INTO prev FROM packs q WHERE q.ai_system_id = p.ai_system_id AND q.seq_no = p.seq_no - 1;

  INSERT INTO verifier_views (tenant_id, share_link_id, pack_digest, ip_hash, user_agent_family)
  VALUES (l.tenant_id, l.id, p.digest, p_ip_hash, p_ua_family);

  RETURN QUERY SELECT 'ok'::text, p.envelope, p.digest, prev, p.seq_no, p.pack_id, l.verifier_org, l.expires_at;
END $$;

REVOKE ALL ON FUNCTION verify_lookup(bytea, bytea, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION verify_lookup(bytea, bytea, text) TO plumbline_app;

COMMIT;
