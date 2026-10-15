package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the app's connection pool. It connects as plumbline_app, which has
// no BYPASSRLS: every tenant-owned row is visible only inside WithTenant.
type Store struct {
	Pool *pgxpool.Pool
}

// Options for Open.
type Options struct {
	// Role, when set, is assumed (SET ROLE) on every new connection. Local
	// development and tests connect as the owner and assume plumbline_app,
	// which has no login of its own until ops grants one.
	Role string
}

// Open connects a pool to url. Connection strings are never logged.
func Open(ctx context.Context, url string, o Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("store: the database URL does not parse") // never echo it: it holds a password
	}
	if o.Role != "" {
		role := o.Role
		cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
			_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
			return err
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close closes the pool.
func (s *Store) Close() { s.Pool.Close() }

// ErrNoTenant: WithTenant was called without a tenant.
var ErrNoTenant = errors.New("store: no tenant")

// WithTenant runs fn in a transaction where app.tenant_id is the given tenant
// (transaction-local, so a pooled connection never carries it to the next
// caller). Row-level security limits fn to that tenant's rows. The
// transaction commits if fn returns nil.
func (s *Store) WithTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return ErrNoTenant
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::uuid::text, true)`, tenantID); err != nil {
			return fmt.Errorf("store: set tenant: %w", err)
		}
		return fn(tx)
	})
}

// Public runs fn in a transaction with no tenant: only the SECURITY DEFINER
// lookups (agent_lookup, enrolment_claim, member_lookup, verify_fetch) and
// global tables are reachable.
func (s *Store) Public(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, fn)
}
