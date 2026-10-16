-- 0004: what the daily checkpoint reads across tenants (P4-7, D-043): pack
-- digests and issue times only, never envelopes or tenants.

BEGIN;

CREATE OR REPLACE FUNCTION checkpoint_digests(p_from timestamptz, p_to timestamptz)
RETURNS TABLE (digest text, issued_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT p.digest, p.issued_at FROM packs p
  WHERE p.issued_at >= p_from AND p.issued_at < p_to
  ORDER BY p.issued_at, p.digest
$$;

CREATE OR REPLACE FUNCTION first_pack_issued_at()
RETURNS timestamptz
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT min(issued_at) FROM packs
$$;

REVOKE ALL ON FUNCTION checkpoint_digests(timestamptz, timestamptz), first_pack_issued_at() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION checkpoint_digests(timestamptz, timestamptz), first_pack_issued_at() TO plumbline_app;

COMMIT;
