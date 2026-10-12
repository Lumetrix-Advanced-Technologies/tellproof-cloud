-- Run against a fresh DB after 0001_init.sql, as the owner role: psql -f migrations/tests/0001_rls_and_append_only.sql
-- Expected: T1, T2, T5, T6, T9 raise errors; T3 = 0; T4 = 1; T7 returns KST-1 and logs one view (0 visible to the app role, 1 total); T8 = 0.
\set ON_ERROR_STOP 0
-- seed as owner
INSERT INTO tenants (id, name) VALUES ('00000000-0000-0000-0000-00000000000a','A'),('00000000-0000-0000-0000-00000000000b','B');
INSERT INTO members (id, tenant_id, user_id, email) VALUES ('10000000-0000-0000-0000-00000000000a','00000000-0000-0000-0000-00000000000a','20000000-0000-0000-0000-00000000000a','a@x');
INSERT INTO ai_systems (id, tenant_id, name) VALUES ('30000000-0000-0000-0000-00000000000a','00000000-0000-0000-0000-00000000000a','Claims Agent'),('30000000-0000-0000-0000-00000000000b','00000000-0000-0000-0000-00000000000b','Other');
INSERT INTO evaluations (id, tenant_id, ai_system_id, period_start, period_end, objective_set_hash) VALUES ('40000000-0000-0000-0000-00000000000a','00000000-0000-0000-0000-00000000000a','30000000-0000-0000-0000-00000000000a','2026-09-11','2026-10-11','sha256:x');
INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, prev_digest, digest, envelope, kms_key_id)
 VALUES ('00000000-0000-0000-0000-00000000000a','30000000-0000-0000-0000-00000000000a','40000000-0000-0000-0000-00000000000a','KST-1',1,NULL,'sha256:'||repeat('a',64),'{"payload":"x"}','k1');
INSERT INTO share_links (tenant_id, ai_system_id, token_hash, verifier_org, expires_at, created_by)
 VALUES ('00000000-0000-0000-0000-00000000000a','30000000-0000-0000-0000-00000000000a',sha256('tok'),'Bank X',now()+interval '30 days','10000000-0000-0000-0000-00000000000a');
\echo '--- T1 duplicate seq_no rejected'
INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, prev_digest, digest, envelope, kms_key_id)
 VALUES ('00000000-0000-0000-0000-00000000000a','30000000-0000-0000-0000-00000000000a','40000000-0000-0000-0000-00000000000a','KST-1b',1,NULL,'sha256:'||repeat('b',64),'{}','k1');
\echo '--- T2 owner UPDATE on packs rejected by trigger'
UPDATE packs SET kms_key_id='k2';
SET ROLE plumbline_app;
\echo '--- T3 app role, no tenant set: sees 0 packs'
SELECT count(*) AS packs_visible FROM packs;
BEGIN; SELECT set_config('app.tenant_id','00000000-0000-0000-0000-00000000000a', true);
\echo '--- T4 tenant A sees own systems only (expect 1)'
SELECT count(*) AS systems_visible FROM ai_systems;
\echo '--- T5 tenant A cannot insert for tenant B (expect RLS error)'
INSERT INTO ai_systems (tenant_id, name) VALUES ('00000000-0000-0000-0000-00000000000b','sneaky');
ROLLBACK;
\echo '--- T6 app role cannot DELETE packs (expect permission denied)'
DELETE FROM packs;
\echo '--- T7 public verify_fetch works without tenant, logs a view'
SELECT pack_id, seq_no, verifier_org FROM verify_fetch(sha256('tok'), NULL, 'firefox');
SELECT count(*) AS views_visible_without_tenant FROM verifier_views;
RESET ROLE;
SELECT count(*) AS views_total FROM verifier_views;
\echo '--- T8 wrong token returns nothing'
SET ROLE plumbline_app; SELECT count(*) FROM verify_fetch(sha256('nope'), NULL, NULL); RESET ROLE;
\echo '--- T9 seq 2 without prev rejected by check'
INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, prev_digest, digest, envelope, kms_key_id)
 VALUES ('00000000-0000-0000-0000-00000000000a','30000000-0000-0000-0000-00000000000a','40000000-0000-0000-0000-00000000000a','KST-2',2,NULL,'sha256:'||repeat('c',64),'{}','k1');
