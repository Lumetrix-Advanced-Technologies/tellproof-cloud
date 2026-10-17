package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store/storetest"
	"github.com/lumetrix-advanced-technologies/witnessmark/migrations"
)

const (
	tenantA = "00000000-0000-0000-0000-00000000000a"
	tenantB = "00000000-0000-0000-0000-00000000000b"
)

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func TestLoadMigrations(t *testing.T) {
	ms, err := store.LoadMigrations(migrations.Files)
	if err != nil || len(ms) == 0 || ms[0].Name != "0001_init.sql" {
		t.Fatalf("migrations = %v, %v", ms, err)
	}
	fsys := fstest.MapFS{"0002_b.sql": {Data: []byte("x")}, "0001_a.sql": {Data: []byte("y")}, "notes.md": {}, "tests/0003_t.sql": {}}
	ms, _ = store.LoadMigrations(fsys)
	if len(ms) != 2 || ms[0].Version != "0001" || ms[1].Version != "0002" {
		t.Errorf("order = %+v", ms)
	}
	if _, err := store.LoadMigrations(fstest.MapFS{"0001_a.sql": {}, "0001_b.sql": {}}); err == nil {
		t.Error("two migrations with one number were accepted")
	}
}

// Acceptance: migrations apply to a fresh database.
func TestMigrateFreshDatabase(t *testing.T) {
	db := storetest.EmptyDB(t)
	conn := connect(t, db)
	ctx := context.Background()
	ms, _ := store.LoadMigrations(migrations.Files)
	done, err := store.Migrate(ctx, conn, ms)
	if err != nil || len(done) != len(ms) {
		t.Fatalf("applied %v: %v", done, err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&n); err != nil || n < 16 {
		t.Errorf("%d tables after migrating (%v)", n, err)
	}
	if again, err := store.Migrate(ctx, conn, ms); err != nil || len(again) != 0 {
		t.Errorf("second run applied %v: %v", again, err)
	}
	changed := append([]store.Migration(nil), ms...)
	changed[0].Digest = strings.Repeat("0", 64)
	if _, err := store.Migrate(ctx, conn, changed); !errors.Is(err, store.ErrChanged) {
		t.Errorf("an edited migration: %v", err)
	}
}

func TestFailedMigrationLeavesNothing(t *testing.T) {
	db := storetest.MigratedDB(t)
	conn := connect(t, db)
	ctx := context.Background()
	ms, _ := store.LoadMigrations(migrations.Files)
	bad := store.Migration{Version: "9999", Name: "9999_bad.sql", Digest: "x",
		SQL: "BEGIN;\nCREATE TABLE half_done (id int);\nSELECT * FROM no_such_table;\nCOMMIT;\n"}
	if _, err := store.Migrate(ctx, conn, append(ms, bad)); err == nil {
		t.Fatal("a failing migration succeeded")
	}
	var exists bool
	_ = conn.QueryRow(ctx, `SELECT to_regclass('half_done') IS NOT NULL`).Scan(&exists)
	var recorded int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version = '9999'`).Scan(&recorded)
	if exists || recorded != 0 {
		t.Errorf("after a failed migration: table exists %v, recorded %d", exists, recorded)
	}
}

func seed(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO tenants (id, name) VALUES ($1, 'A'), ($2, 'B')`,
		`INSERT INTO ai_systems (tenant_id, name) VALUES ($1, 'Claims Agent'), ($2, 'Other'), ($2, 'Other 2')`,
		`INSERT INTO evaluations (id, tenant_id, ai_system_id, period_start, period_end, objective_set_hash)
		   SELECT '40000000-0000-0000-0000-00000000000a', $1, id, '2026-09-01', '2026-10-01', 'sha256:x' FROM ai_systems WHERE tenant_id = $1 AND $2::uuid IS NOT NULL`,
		`INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, digest, envelope, kms_key_id)
		   SELECT $1, id, '40000000-0000-0000-0000-00000000000a', 'KST-1', 1, 'sha256:' || repeat('a', 64), '{}', 'k1' FROM ai_systems WHERE tenant_id = $1 AND $2::uuid IS NOT NULL`,
	} {
		if _, err := conn.Exec(context.Background(), q, tenantA, tenantB); err != nil {
			t.Fatal(err)
		}
	}
}

func count(t *testing.T, tx pgx.Tx, table string) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Acceptance: tenant A cannot read tenant B's rows.
func TestTenantIsolation(t *testing.T) {
	db := storetest.MigratedDB(t)
	seed(t, connect(t, db))
	sep := "?"
	if strings.Contains(db, "?") {
		sep = "&"
	}
	s := storetest.App(t, db+sep+"pool_max_conns=1") // one connection: every transaction below reuses it
	ctx := context.Background()
	if err := s.WithTenant(ctx, tenantA, func(tx pgx.Tx) error {
		if n := count(t, tx, "ai_systems"); n != 1 {
			t.Errorf("tenant A sees %d systems", n)
		}
		if n := count(t, tx, "packs"); n != 1 {
			t.Errorf("tenant A sees %d packs", n)
		}
		if n := count(t, tx, "tenants"); n != 1 {
			t.Errorf("tenant A sees %d tenants", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.WithTenant(ctx, tenantB, func(tx pgx.Tx) error {
		if n := count(t, tx, "ai_systems"); n != 2 {
			t.Errorf("tenant B sees %d systems", n)
		}
		if n := count(t, tx, "packs"); n != 0 {
			t.Errorf("tenant B sees tenant A's %d packs", n)
		}
		return nil
	})
	// The tenant setting is transaction-local: the next caller on a pooled connection has none.
	_ = s.Public(ctx, func(tx pgx.Tx) error {
		if n := count(t, tx, "ai_systems"); n != 0 {
			t.Errorf("without a tenant the app sees %d systems", n)
		}
		return nil
	})
	err := s.WithTenant(ctx, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ai_systems (tenant_id, name) VALUES ($1, 'sneaky')`, tenantB)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("tenant A inserting for B: %v", err)
	}
	if err := s.WithTenant(ctx, "", func(pgx.Tx) error { return nil }); !errors.Is(err, store.ErrNoTenant) {
		t.Errorf("empty tenant: %v", err)
	}
	if err := s.WithTenant(ctx, "not-a-uuid", func(pgx.Tx) error { return nil }); err == nil {
		t.Error("a malformed tenant ID was accepted")
	}
}

// Acceptance: UPDATE on packs raises; evidence is append-only for everyone.
func TestEvidenceIsAppendOnly(t *testing.T) {
	db := storetest.MigratedDB(t)
	owner := connect(t, db)
	seed(t, owner)
	ctx := context.Background()
	for _, q := range []string{`UPDATE packs SET kms_key_id = 'k2'`, `DELETE FROM packs`, `TRUNCATE packs CASCADE`, `DELETE FROM evaluations`} {
		_, err := owner.Exec(ctx, q)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "42501" || !strings.Contains(pe.Message, "append-only") {
			t.Errorf("owner %q: %v", q, err)
		}
	}
	s := storetest.App(t, db)
	err := s.WithTenant(ctx, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE packs SET kms_key_id = 'k2'`)
		return err
	})
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "42501" {
		t.Errorf("app UPDATE packs: %v", err)
	}
}

func TestOpenNeverEchoesTheURL(t *testing.T) {
	_, err := store.Open(context.Background(), "postgres://u:hunter2@%zz/db", store.Options{})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v", err)
	}
}
