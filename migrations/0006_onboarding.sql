-- 0006: onboarding (P5-3, D-046). A person signed in with Supabase but in no
-- organisation creates one: the tenant, their admin membership and the first AI
-- system, in one step. It runs before any tenant is set, so it is a SECURITY
-- DEFINER function; it does nothing for someone who is already a member.

BEGIN;

CREATE OR REPLACE FUNCTION onboard(p_user_id uuid, p_email text, p_org text, p_system text, p_environment text, p_timezone text)
RETURNS TABLE (tenant_id uuid, member_id uuid, ai_system_id uuid)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = public AS $$
DECLARE t uuid; m uuid; s uuid;
BEGIN
  SELECT mm.tenant_id, mm.id INTO t, m FROM members mm WHERE mm.user_id = p_user_id ORDER BY mm.tenant_id LIMIT 1;
  IF FOUND THEN
    RETURN QUERY SELECT t, m, NULL::uuid; RETURN;
  END IF;
  IF length(trim(p_org)) NOT BETWEEN 1 AND 200 OR length(trim(p_system)) NOT BETWEEN 1 AND 200 OR length(trim(p_environment)) NOT BETWEEN 1 AND 40 THEN
    RAISE EXCEPTION 'organisation, system and environment names are required' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO tenants (name) VALUES (trim(p_org)) RETURNING id INTO t;
  INSERT INTO members (tenant_id, user_id, email, role) VALUES (t, p_user_id, p_email, 'admin') RETURNING id INTO m;
  INSERT INTO ai_systems (tenant_id, name, environment, timezone) VALUES (t, trim(p_system), trim(p_environment), p_timezone) RETURNING id INTO s;
  RETURN QUERY SELECT t, m, s;
END $$;

REVOKE ALL ON FUNCTION onboard(uuid, text, text, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION onboard(uuid, text, text, text, text, text) TO plumbline_app;

COMMIT;
