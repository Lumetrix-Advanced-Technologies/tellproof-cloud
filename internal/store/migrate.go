// Package store is the control plane's Postgres access (docs/architecture.md
// §4–5): migrations, and transactions scoped to one tenant so row-level
// security applies to every query the app role runs.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// Migration is one numbered file.
type Migration struct {
	Version string // "0001"
	Name    string // "0001_init.sql"
	SQL     string
	Digest  string // sha256 hex of the file
}

// LoadMigrations reads NNNN_name.sql files from fsys, in version order.
// Versions must be unique.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	ents, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[string]bool{}
	for _, e := range ents {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		if seen[m[1]] {
			return nil, fmt.Errorf("store: two migrations numbered %s", m[1])
		}
		seen[m[1]] = true
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{Version: m[1], Name: e.Name(), SQL: string(b), Digest: hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(out, func(a, b Migration) int { return strings.Compare(a.Version, b.Version) })
	return out, nil
}

// body returns the statements to run inside the runner's transaction: a file
// may wrap itself in BEGIN; … COMMIT; (so it also runs as-is under psql); that
// outer pair is left out so the file and its schema_migrations row commit
// together.
func body(sql string) string {
	lines := strings.Split(sql, "\n")
	first, last := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 || first == last {
		return sql
	}
	if strings.EqualFold(strings.TrimSpace(lines[first]), "BEGIN;") && strings.EqualFold(strings.TrimSpace(lines[last]), "COMMIT;") {
		lines[first], lines[last] = "", ""
	}
	return strings.Join(lines, "\n")
}

// ErrChanged: an applied migration's file no longer matches what was applied.
var ErrChanged = errors.New("store: an applied migration has changed; migrations are immutable, add a new numbered file")

// Migrate applies every migration not yet recorded in schema_migrations, each
// in its own transaction together with its record, under an advisory lock so
// two processes never migrate at once. It must run as the schema owner. It
// returns the names it applied.
func Migrate(ctx context.Context, conn *pgx.Conn, migrations []Migration) ([]string, error) {
	const lockKey = 0x706c756d626c696e // "plumblin"
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(lockKey)); err != nil {
		return nil, fmt.Errorf("store: migrate lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockKey))

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		name       text NOT NULL,
		sha256     text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("store: schema_migrations: %w", err)
	}
	applied := map[string]string{}
	rows, err := conn.Query(ctx, `SELECT version, sha256 FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v, d string
		if err := rows.Scan(&v, &d); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = d
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var done []string
	for _, m := range migrations {
		if d, ok := applied[m.Version]; ok {
			if d != m.Digest {
				return done, fmt.Errorf("%w (%s)", ErrChanged, m.Name)
			}
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// Simple protocol: a file is many statements.
			if _, err := tx.Exec(ctx, body(m.SQL), pgx.QueryExecModeSimpleProtocol); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, $2, $3)`, m.Version, m.Name, m.Digest)
			return err
		})
		if err != nil {
			return done, fmt.Errorf("store: migration %s: %w", m.Name, err)
		}
		done = append(done, m.Name)
	}
	return done, nil
}
